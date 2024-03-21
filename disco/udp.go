package disco

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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
	_ net.PacketConn = (*UDPConn)(nil)
	_ PeerStore      = (*UDPConn)(nil)
)

type UDPConn struct {
	*net.UDPConn
	closedSig             chan int
	peersOPs              chan *PeerOP
	datagrams             chan *Datagram
	stunResponse          chan []byte
	udpAddrSends          chan *PeerUDPAddrEvent
	peerKeepaliveInterval time.Duration
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
	return c.UDPConn.Close()
}

func (c *UDPConn) Datagrams() <-chan *Datagram {
	return c.datagrams
}

func (c *UDPConn) UDPAddrSends() <-chan *PeerUDPAddrEvent {
	return c.udpAddrSends
}

func (c *UDPConn) GenerateLocalAddrsSends(peerID peer.ID, stunServers []string) {
	c.peersOPs <- &PeerOP{
		Op:     OP_PEER_DELETE,
		PeerID: peerID,
	}
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
		if ctx, ok := c.FindPeer(peerID); !ok || !ctx.IPv4Ready() {
			c.requestSTUN(peerID, stunServers)
		}
	})
}

func (c *UDPConn) RunDiscoMessageSendLoop(peerID peer.ID, addr *net.UDPAddr) {
	c.peersOPs <- &PeerOP{
		Op:     OP_PEER_DISCO,
		PeerID: peerID,
		Addr:   addr,
	}
	defer slog.Debug("[UDP] DiscoPingExit", "peer", peerID, "addr", addr)
	c.discoPing(peerID, addr)
	interval := defaultDiscoConfig.ChallengesInitialInterval
	for i := 0; i <= defaultDiscoConfig.ChallengesRetry; i++ {
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
			c.UDPConn.WriteToUDP([]byte("_ping"+c.id), dst)
			time.Sleep(50 * time.Microsecond)
		}
		slog.Info("[UDP] PortScanExit", "peer", peerID, "addr", addr)
	}
}

func (c *UDPConn) discoPing(peerID peer.ID, peerAddr *net.UDPAddr) {
	slog.Debug("[UDP] DiscoPing", "peer", peerID, "addr", peerAddr)
	c.UDPConn.WriteToUDP([]byte("_ping"+c.id), peerAddr)
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
			return
		}

		// ping
		if n > 4 && string(buf[:5]) == "_ping" && n <= 260 {
			peerID := string(buf[5:n])
			c.peersOPs <- &PeerOP{
				Op:     OP_PEER_CONFIRM,
				PeerID: peer.ID(peerID),
				Addr:   peerAddr,
			}
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
		b := make([]byte, n)
		copy(b, buf[:n])
		c.datagrams <- &Datagram{
			PeerID: peerID,
			Data:   b,
		}
	}
}

func (c *UDPConn) runPeerOPLoop() {
	handlePeerOp := func(e *PeerOP) {
		c.peersIndexMutex.Lock()
		defer c.peersIndexMutex.Unlock()
		switch e.Op {
		case OP_PEER_DELETE:
			delete(c.peersIndex, e.PeerID)
		case OP_PEER_DISCO:
			if _, ok := c.peersIndex[e.PeerID]; !ok {
				peerCtx := PeerContext{
					exitSig:           make(chan struct{}),
					ping:              c.discoPing,
					keepaliveInterval: c.peerKeepaliveInterval,
					PeerID:            e.PeerID,
					States:            make(map[string]*PeerState),
					CreateTime:        time.Now(),
				}
				c.peersIndex[e.PeerID] = &peerCtx
				go peerCtx.Keepalive()
			}
			c.peersIndex[e.PeerID].AddAddr(e.Addr)
		case OP_PEER_CONFIRM:
			slog.Debug("[UDP] Heartbeat", "peer", e.PeerID, "addr", e.Addr)
			if peer, ok := c.peersIndex[e.PeerID]; ok {
				peer.Heartbeat(e.Addr)
			}
		case OP_PEER_HEALTHCHECK:
			for k, v := range c.peersIndex {
				v.Healthcheck()
				if len(v.States) == 0 {
					v.Close()
					delete(c.peersIndex, k)
				}
			}
		}
	}
	for {
		select {
		case <-c.closedSig:
			return
		case e := <-c.peersOPs:
			handlePeerOp(e)
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
		stunResp := <-c.stunResponse
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
			c.peersOPs <- &PeerOP{
				Op: OP_PEER_HEALTHCHECK,
			}
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

func ListenUDP(port int, disableIPv4, disableIPv6 bool, id peer.ID) (*UDPConn, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen udp error: %w", err)
	}
	ips, err := ListLocalIPs()
	if err != nil {
		return nil, fmt.Errorf("list local ips error: %w", err)
	}

	udpConn := UDPConn{
		id:                    id,
		UDPConn:               conn,
		closedSig:             make(chan int),
		peersOPs:              make(chan *PeerOP),
		datagrams:             make(chan *Datagram),
		udpAddrSends:          make(chan *PeerUDPAddrEvent, 10),
		stunResponse:          make(chan []byte, 10),
		peerKeepaliveInterval: 10 * time.Second,
		peersIndex:            make(map[peer.ID]*PeerContext),
		stunSessions:          cmap.New[STUNSession](),
	}

	for _, ip := range ips {
		addr := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
		if ip.To4() != nil {
			if disableIPv4 {
				continue
			}
		} else {
			if disableIPv6 {
				continue
			}
		}
		udpConn.localAddrs = append(udpConn.localAddrs, addr)
	}
	go udpConn.runPacketEventLoop()
	go udpConn.runPeerOPLoop()
	go udpConn.runSTUNEventLoop()
	go udpConn.runPeersHealthcheckLoop()
	return &udpConn, nil
}
