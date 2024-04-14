package disco

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/upnp"
	"tailscale.com/net/stun"
)

var (
	networkDetectInterval = 5 * time.Second

	_ net.PacketConn = (*UDPConn)(nil)
	_ PeerStore      = (*UDPConn)(nil)
)

func SetNetwotkDetectInterval(interval time.Duration) {
	networkDetectInterval = interval
}

type UDPConn struct {
	*net.UDPConn
	disco                 *Disco
	closedSig             chan int
	datagrams             chan *Datagram
	stunResponse          chan []byte
	udpAddrSends          chan *PeerUDPAddrEvent
	networkChanged        chan struct{}
	peerKeepaliveInterval time.Duration
	udpPort               int
	disableIPv4           bool
	disableIPv6           bool
	id                    peer.ID
	peersIndex            map[peer.ID]*PeerContext
	stunSessions          cmap.ConcurrentMap[string, STUNSession]

	localAddrs        []string
	upnpDeleteMapping func()

	peersIndexMutex sync.RWMutex
}

func (c *UDPConn) Close() error {
	if c.upnpDeleteMapping != nil {
		c.upnpDeleteMapping()
	}
	close(c.closedSig)
	close(c.datagrams)
	close(c.stunResponse)
	close(c.udpAddrSends)
	return c.UDPConn.Close()
}

func (c *UDPConn) Datagrams() <-chan *Datagram {
	return c.datagrams
}

func (c *UDPConn) UDPAddrSends() <-chan *PeerUDPAddrEvent {
	return c.udpAddrSends
}

func (c *UDPConn) NetworkChangedEvents() <-chan struct{} {
	return c.networkChanged
}

func (c *UDPConn) GenerateLocalAddrsSends(peerID peer.ID, stunServers []string) {
	// UPnP
	go func() {
		nat, err := upnp.Discover()
		if err != nil {
			slog.Debug("UPnP is disabled", "err", err)
			return
		}
		externalIP, err := nat.GetExternalAddress()
		if err != nil {
			slog.Debug("UPnP is disabled", "err", err)
			return
		}
		udpPort := int(netip.MustParseAddrPort(c.UDPConn.LocalAddr().String()).Port())

		for i := 0; i < 20; i++ {
			mappedPort, err := nat.AddPortMapping("udp", udpPort+i, udpPort, "peerguard", 24*3600)
			if err != nil {
				continue
			}
			c.upnpDeleteMapping = func() { nat.DeletePortMapping("udp", mappedPort, udpPort) }
			c.udpAddrSends <- &PeerUDPAddrEvent{
				PeerID: peerID,
				Addr:   &net.UDPAddr{IP: externalIP, Port: mappedPort},
			}
			return
		}
	}()
	// LAN
	for _, addr := range c.localAddrs {
		uaddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			slog.Error("Resolve local udp addr error", "err", err)
			continue
		}
		c.udpAddrSends <- &PeerUDPAddrEvent{
			PeerID: peerID,
			Addr:   uaddr,
		}
	}
	// WAN
	time.AfterFunc(time.Second, func() {
		if _, ok := c.FindPeer(peerID); !ok {
			c.requestSTUN(peerID, stunServers)
		}
	})
}

func (c *UDPConn) tryPeerContext(peerID peer.ID) *PeerContext {
	if !c.peersIndexMutex.TryRLock() {
		return nil
	}
	ctx, ok := c.peersIndex[peerID]
	c.peersIndexMutex.RUnlock()
	if ok {
		return ctx
	}
	c.peersIndexMutex.Lock()
	defer c.peersIndexMutex.Unlock()
	if ctx, ok := c.peersIndex[peerID]; ok {
		return ctx
	}
	peerCtx := PeerContext{
		exitSig:           make(chan struct{}),
		ping:              c.discoPing,
		keepaliveInterval: c.peerKeepaliveInterval,
		PeerID:            peerID,
		States:            make(map[string]*PeerState),
		CreateTime:        time.Now(),
	}
	c.peersIndex[peerID] = &peerCtx
	go peerCtx.RunKeepaliveLoop()
	return &peerCtx
}

func (c *UDPConn) RunDiscoMessageSendLoop(peerID peer.ID, addr *net.UDPAddr) {
	defer slog.Debug("[UDP] DiscoExit", "peer", peerID, "addr", addr)
	c.discoPing(peerID, addr)
	interval := defaultDiscoConfig.ChallengesInitialInterval + time.Duration(rand.Intn(100)*int(time.Millisecond))
	for i := 0; i < defaultDiscoConfig.ChallengesRetry; i++ {
		time.Sleep(interval)
		select {
		case <-c.closedSig:
			return
		default:
		}
		c.discoPing(peerID, addr)
		interval = time.Duration(float64(interval) * defaultDiscoConfig.ChallengesBackoffRate)
		if c.findPeerID(addr) != "" {
			return
		}
	}

	if ctx, ok := c.FindPeer(peerID); (!ok || !ctx.Ready()) && addr.IP.To4() != nil && !addr.IP.IsPrivate() {
		slog.Info("[UDP] PortScanning", "peer", peerID, "addr", addr)
		for port := addr.Port + 1; port <= addr.Port+defaultDiscoConfig.PortScanCount; port++ {
			select {
			case <-c.closedSig:
				return
			default:
			}
			p := port % 65536
			if p <= 1024 {
				continue
			}
			if ctx, ok := c.FindPeer(peerID); ok && ctx.Ready() {
				slog.Info("[UDP] PortScanHit", "peer", peerID, "cursor", p)
				return
			}
			dst := &net.UDPAddr{IP: addr.IP, Port: p}
			c.UDPConn.WriteToUDP(c.disco.NewPing(c.id), dst)
			time.Sleep(100 * time.Microsecond)
		}
		slog.Info("[UDP] PortScanExit", "peer", peerID, "addr", addr)
	}
}

func (c *UDPConn) discoPing(peerID peer.ID, peerAddr *net.UDPAddr) {
	slog.Debug("[UDP] DiscoPing", "peer", peerID, "addr", peerAddr)
	c.UDPConn.WriteToUDP(c.disco.NewPing(c.id), peerAddr)
}

func (c *UDPConn) updateLocalNetworkAddrs() []string {
	ips, err := ListLocalIPs()
	if err != nil {
		slog.Error("ListLocalIPsFailed", "details", err)
		return nil
	}
	var detectIPs []string
	for _, ip := range ips {
		addr := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", c.udpPort))
		if ip.To4() != nil {
			if c.disableIPv4 {
				continue
			}
		} else {
			if c.disableIPv6 {
				continue
			}
		}
		if slices.Contains(detectIPs, addr) {
			continue
		}
		detectIPs = append(detectIPs, addr)
	}
	slices.Sort(detectIPs)
	if !slices.Equal(c.localAddrs, detectIPs) {
		srcAddrsSize := len(c.localAddrs)
		c.localAddrs = detectIPs
		if len(c.networkChanged) < cap(c.networkChanged) && srcAddrsSize > 0 {
			c.networkChanged <- struct{}{}
		}
		return detectIPs
	}
	return nil
}

func (c *UDPConn) runNetworkChangedDetector() {
	detectTicker := time.NewTicker(max(networkDetectInterval, time.Second))
	for {
		select {
		case <-c.closedSig:
			detectTicker.Stop()
			return
		case <-detectTicker.C:
			if detectAddrs := c.updateLocalNetworkAddrs(); detectAddrs != nil {
				slog.Info("NetworkChanged", "addrs", detectAddrs)
			}
		}
	}
}

func (c *UDPConn) runPacketEventLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		n, peerAddr, err := c.UDPConn.ReadFromUDP(buf)
		if err != nil {
			if !strings.Contains(err.Error(), ErrUseOfClosedConnection.Error()) {
				slog.Error("read from udp error", "err", err)
			}
			time.Sleep(10 * time.Millisecond) // avoid busy wait
			continue
		}

		// ping
		if peerID := c.disco.ParsePing(buf[:n]); peerID.Len() > 0 {
			c.tryPeerContext(peerID).Heartbeat(peerAddr)
			continue
		}

		// stun response
		if stun.Is(buf[:n]) {
			b := make([]byte, n)
			copy(b, buf[:n])
			c.stunResponse <- b
			continue
		}

		// datagram
		peerID := c.findPeerID(peerAddr)
		if peerID.Len() == 0 {
			slog.Error("RecvButPeerNotReady", "addr", peerAddr)
			continue
		}
		c.tryPeerContext(peerID).Heartbeat(peerAddr)
		b := make([]byte, n)
		copy(b, buf[:n])
		c.datagrams <- &Datagram{
			PeerID: peerID,
			Data:   b,
		}
	}
}

func (c *UDPConn) runSTUNEventLoop() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		stunResp, ok := <-c.stunResponse
		if !ok {
			return
		}
		txid, saddr, err := stun.ParseResponse(stunResp)
		if err != nil {
			slog.Error("Skipped invalid stun response", "err", err.Error())
			continue
		}

		tx, ok := c.stunSessions.Get(string(txid[:]))
		if !ok {
			slog.Debug("Skipped unknown stun response", "txid", hex.EncodeToString(txid[:]))
			continue
		}

		if !saddr.IsValid() {
			slog.Error("Skipped invalid UDP addr", "addr", saddr)
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", saddr.String())
		if err != nil {
			slog.Error("Skipped resolve udp addr error", "err", err)
			continue
		}
		c.udpAddrSends <- &PeerUDPAddrEvent{
			PeerID: tx.PeerID,
			Addr:   addr,
		}
	}
}

func (c *UDPConn) runPeersHealthcheckLoop() {
	ticker := time.NewTicker(c.peerKeepaliveInterval/2 + time.Second)
	for {
		select {
		case <-c.closedSig:
			ticker.Stop()
			return
		case <-ticker.C:
			c.peersIndexMutex.Lock()
			for k, v := range c.peersIndex {
				v.Healthcheck()
				if len(v.States) == 0 {
					v.Close()
					delete(c.peersIndex, k)
				}
			}
			c.peersIndexMutex.Unlock()
		}
	}
}

func (c *UDPConn) requestSTUN(peerID peer.ID, stunServers []string) {
	txID := stun.NewTxID()
	c.stunSessions.Set(string(txID[:]), STUNSession{PeerID: peerID, CTime: time.Now()})
	for _, stunServer := range stunServers {
		uaddr, err := net.ResolveUDPAddr("udp", stunServer)
		if err != nil {
			slog.Error("Invalid STUN addr", "addr", stunServer, "err", err.Error())
			continue
		}
		_, err = c.UDPConn.WriteToUDP(stun.Request(txID), uaddr)
		if err != nil {
			slog.Error("Request STUN server failed", "err", err.Error())
			continue
		}
		time.Sleep(5 * time.Second)
		if _, ok := c.FindPeer(peerID); ok {
			c.stunSessions.Remove(string(txID[:]))
			break
		}
	}
}

func (c *UDPConn) findPeerID(udpAddr *net.UDPAddr) peer.ID {
	if udpAddr == nil {
		return ""
	}
	c.peersIndexMutex.RLock()
	defer c.peersIndexMutex.RUnlock()
	var candidates []PeerState
peerSeek:
	for _, v := range c.peersIndex {
		for _, state := range v.States {
			if !state.Addr.IP.Equal(udpAddr.IP) || state.Addr.Port != udpAddr.Port {
				continue
			}
			if time.Since(state.LastActiveTime) > 2*c.peerKeepaliveInterval {
				continue peerSeek
			}
			candidates = append(candidates, *state)
			continue peerSeek
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	slices.SortFunc(candidates, func(c1, c2 PeerState) int {
		if c1.LastActiveTime.After(c2.LastActiveTime) {
			return -1
		}
		return 1
	})
	return candidates[0].PeerID
}

// FindPeer is used to find ready peer context by peer id
func (c *UDPConn) FindPeer(peerID peer.ID) (*PeerContext, bool) {
	c.peersIndexMutex.RLock()
	defer c.peersIndexMutex.RUnlock()
	if peer, ok := c.peersIndex[peerID]; ok && peer.Ready() {
		return peer, true
	}
	return nil, false
}

func (n *UDPConn) WriteToUDP(p []byte, peerID peer.ID) (int, error) {
	if peer, ok := n.FindPeer(peerID); ok {
		if addr := peer.Select(); addr != nil {
			slog.Log(context.Background(), -3, "[UDP] WriteTo", "peer", peerID, "addr", addr)
			return n.UDPConn.WriteToUDP(p, addr)
		}
	}
	return 0, ErrUseOfClosedConnection
}

func (c *UDPConn) Broadcast(b []byte) (peerCount int, err error) {
	c.peersIndexMutex.RLock()
	peerCount = len(c.peersIndex)
	peers := make([]peer.ID, 0, peerCount)
	for k := range c.peersIndex {
		peers = append(peers, k)
	}
	c.peersIndexMutex.RUnlock()

	var errs []error
	for _, peer := range peers {
		_, err := c.WriteToUDP(b, peer)
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		err = errors.Join(errs...)
	}
	return
}

// SetKeepAlivePeriod set udp keepalive period
func (c *UDPConn) SetKeepAlivePeriod(period time.Duration) {
	c.peerKeepaliveInterval = max(period, time.Second)
}

// SetDiscoMagic set disco magic bytes provider function
func (c *UDPConn) SetDiscoMagic(magic func() []byte) {
	c.disco.Magic = magic
}

func ListenUDP(port int, disableIPv4, disableIPv6 bool, id peer.ID) (*UDPConn, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen udp error: %w", err)
	}

	udpConn := UDPConn{
		id:                    id,
		UDPConn:               conn,
		disco:                 &Disco{},
		closedSig:             make(chan int),
		datagrams:             make(chan *Datagram),
		udpAddrSends:          make(chan *PeerUDPAddrEvent, 10),
		stunResponse:          make(chan []byte, 10),
		networkChanged:        make(chan struct{}, 1),
		peerKeepaliveInterval: 10 * time.Second,
		udpPort:               port,
		disableIPv4:           disableIPv4,
		disableIPv6:           disableIPv6,
		peersIndex:            make(map[peer.ID]*PeerContext),
		stunSessions:          cmap.New[STUNSession](),
	}
	udpConn.updateLocalNetworkAddrs()
	go udpConn.runSTUNEventLoop()
	go udpConn.runPacketEventLoop()
	go udpConn.runPeersHealthcheckLoop()
	go udpConn.runNetworkChangedDetector()
	return &udpConn, nil
}
