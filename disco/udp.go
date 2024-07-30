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
	"sync/atomic"
	"time"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/upnp"
	"golang.org/x/time/rate"
	"tailscale.com/net/stun"
)

var (
	ErrUDPConnNotReady = errors.New("udpConn not ready yet")

	_ PeerStore = (*UDPConn)(nil)
)

type UDPConfig struct {
	Port                  int
	DisableIPv4           bool
	DisableIPv6           bool
	ID                    peer.ID
	PeerKeepaliveInterval time.Duration
	DiscoMagic            func() []byte
}

type UDPConn struct {
	rawConn      atomic.Pointer[net.UDPConn]
	cfg          UDPConfig
	disco        *Disco
	closedSig    chan int
	datagrams    chan *Datagram
	stunResponse chan []byte
	udpAddrSends chan *PeerUDPAddr
	peersIndex   map[peer.ID]*PeerContext

	stunSessionManager stunSessionManager

	upnpDeleteMapping func()

	peersIndexMutex sync.RWMutex
}

func (c *UDPConn) Close() error {
	if c.upnpDeleteMapping != nil {
		c.upnpDeleteMapping()
	}
	if conn := c.rawConn.Load(); conn != nil {
		conn.Close()
	}
	close(c.closedSig)
	close(c.datagrams)
	close(c.stunResponse)
	close(c.udpAddrSends)
	return nil
}

// SetReadBuffer sets the size of the operating system's
// receive buffer associated with the connection.
func (c *UDPConn) SetReadBuffer(bytes int) error {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return ErrUDPConnNotReady
	}
	return udpConn.SetReadBuffer(bytes)
}

// SetWriteBuffer sets the size of the operating system's
// transmit buffer associated with the connection.
func (c *UDPConn) SetWriteBuffer(bytes int) error {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return ErrUDPConnNotReady
	}
	return udpConn.SetWriteBuffer(bytes)
}

func (c *UDPConn) Datagrams() <-chan *Datagram {
	return c.datagrams
}

func (c *UDPConn) UDPAddrSends() <-chan *PeerUDPAddr {
	return c.udpAddrSends
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
		udpConn := c.rawConn.Load()
		if udpConn == nil {
			slog.Error("UPnP discover", "err", ErrUDPConnNotReady)
			return
		}
		udpPort := int(netip.MustParseAddrPort(udpConn.LocalAddr().String()).Port())

		for i := 0; i < 20; i++ {
			mappedPort, err := nat.AddPortMapping("udp", udpPort+i, udpPort, "peerguard", 24*3600)
			if err != nil {
				continue
			}
			c.upnpDeleteMapping = func() { nat.DeletePortMapping("udp", mappedPort, udpPort) }
			c.udpAddrSends <- &PeerUDPAddr{
				ID:   peerID,
				Addr: &net.UDPAddr{IP: externalIP, Port: mappedPort},
			}
			return
		}
	}()
	// LAN
	for _, addr := range c.localAddrs() {
		uaddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			slog.Error("Resolve local udp addr error", "err", err)
			continue
		}
		c.udpAddrSends <- &PeerUDPAddr{
			ID:   peerID,
			Addr: uaddr,
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
		keepaliveInterval: c.cfg.PeerKeepaliveInterval,
		PeerID:            peerID,
		States:            make(map[string]*PeerState),
		CreateTime:        time.Now(),
	}
	c.peersIndex[peerID] = &peerCtx
	go peerCtx.RunKeepaliveLoop()
	return &peerCtx
}

func (c *UDPConn) RunDiscoMessageSendLoop(peerID peer.ID, addr *net.UDPAddr) {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return
	}
	slog.Log(context.Background(), -2, "RecvPeerAddr", "peer", peerID, "udp", addr)
	defer slog.Debug("[UDP] DiscoExit", "peer", peerID, "addr", addr)
	c.discoPing(peerID, addr)
	interval := defaultDiscoConfig.ChallengesInitialInterval + time.Duration(rand.Intn(50)*int(time.Millisecond))
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

	if ctx, ok := c.FindPeer(peerID); (ok && ctx.Ready()) || (addr.IP.To4() == nil) || addr.IP.IsPrivate() {
		return
	}

	slog.Info("[UDP] PortScanning", "peer", peerID, "addr", addr)
	scan := func(round int) {
		limit := defaultDiscoConfig.PortScanCount / 10
		rl := rate.NewLimiter(rate.Limit(limit), limit)
		for port := addr.Port + defaultDiscoConfig.PortScanOffset; port <= addr.Port+defaultDiscoConfig.PortScanCount; port++ {
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
				slog.Info("[UDP] PortScanHit", "peer", peerID, "round", round, "port", p)
				return
			}
			if err := rl.Wait(context.Background()); err != nil {
				slog.Error("[UDP] PortScanRateLimiter", "err", err)
				return
			}
			udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), &net.UDPAddr{IP: addr.IP, Port: p})
		}
	}
	for i := range 2 {
		scan(i + 1)
	}
	slog.Info("[UDP] PortScanExit", "peer", peerID, "addr", addr)
}

func (c *UDPConn) discoPing(peerID peer.ID, peerAddr *net.UDPAddr) {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return
	}
	slog.Debug("[UDP] DiscoPing", "peer", peerID, "addr", peerAddr)
	udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), peerAddr)
}

func (c *UDPConn) localAddrs() []string {
	ips, err := ListLocalIPs()
	if err != nil {
		slog.Error("ListLocalIPsFailed", "details", err)
		return nil
	}
	var detectIPs []string
	for _, ip := range ips {
		addr := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", c.cfg.Port))
		if ip.To4() != nil {
			if c.cfg.DisableIPv4 {
				continue
			}
		} else {
			if c.cfg.DisableIPv6 {
				continue
			}
		}
		if slices.Contains(detectIPs, addr) {
			continue
		}
		detectIPs = append(detectIPs, addr)
	}
	slog.Log(context.Background(), -2, "LocalAddrs", "addrs", detectIPs)
	return detectIPs
}

func (c *UDPConn) runPacketEventLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		udpConn := c.rawConn.Load()
		if udpConn == nil {
			continue
		}
		n, peerAddr, err := udpConn.ReadFromUDP(buf)
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
		c.datagrams <- &Datagram{PeerID: peerID, Data: b}
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

		tx, ok := c.stunSessionManager.Get(string(txid[:]))
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
		c.udpAddrSends <- &PeerUDPAddr{ID: tx.peerID, Addr: addr}
	}
}

func (c *UDPConn) runPeersHealthcheckLoop() {
	ticker := time.NewTicker(c.cfg.PeerKeepaliveInterval/2 + time.Second)
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
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return
	}
	txID := stun.NewTxID()
	c.stunSessionManager.Set(string(txID[:]), peerID)
	for _, stunServer := range stunServers {
		uaddr, err := net.ResolveUDPAddr("udp", stunServer)
		if err != nil {
			slog.Error("Invalid STUN addr", "addr", stunServer, "err", err.Error())
			continue
		}
		_, err = udpConn.WriteToUDP(stun.Request(txID), uaddr)
		if err != nil {
			slog.Error("Request STUN server failed", "err", err.Error())
			continue
		}
		time.Sleep(5 * time.Second)
		if _, ok := c.FindPeer(peerID); ok {
			c.stunSessionManager.Remove(string(txID[:]))
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
			if time.Since(state.LastActiveTime) > 2*c.cfg.PeerKeepaliveInterval {
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

func (c *UDPConn) WriteToUDP(p []byte, peerID peer.ID) (int, error) {
	if peer, ok := c.FindPeer(peerID); ok {
		if addr := peer.Select(); addr != nil {
			udpConn := c.rawConn.Load()
			if udpConn == nil {
				return 0, ErrUDPConnNotReady
			}
			slog.Log(context.Background(), -3, "[UDP] WriteTo", "peer", peerID, "addr", addr)
			return udpConn.WriteToUDP(p, addr)
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

func (c *UDPConn) Peers() (peers []PeerState) {
	c.peersIndexMutex.RLock()
	defer c.peersIndexMutex.RUnlock()
	for _, v := range c.peersIndex {
		v.statesMutex.RLock()
		for _, state := range v.States {
			peers = append(peers, *state)
		}
		v.statesMutex.RUnlock()
	}
	return
}

func (c *UDPConn) RestartListener() error {
	if udpConn := c.rawConn.Load(); udpConn != nil {
		udpConn.Close()
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: c.cfg.Port})
	if err != nil {
		return fmt.Errorf("listen udp error: %w", err)
	}
	c.rawConn.Store(conn)
	return nil
}

func ListenUDP(cfg UDPConfig) (*UDPConn, error) {
	if cfg.ID.Len() == 0 {
		return nil, errors.New("peer id is required")
	}
	if cfg.PeerKeepaliveInterval < time.Second {
		cfg.PeerKeepaliveInterval = 10 * time.Second
	}

	udpConn := UDPConn{
		cfg:                cfg,
		disco:              &Disco{Magic: cfg.DiscoMagic},
		closedSig:          make(chan int),
		datagrams:          make(chan *Datagram),
		udpAddrSends:       make(chan *PeerUDPAddr, 10),
		stunResponse:       make(chan []byte, 10),
		peersIndex:         make(map[peer.ID]*PeerContext),
		stunSessionManager: stunSessionManager{sessions: make(map[string]stunSession)},
	}

	if err := udpConn.RestartListener(); err != nil {
		return nil, err
	}

	go udpConn.runSTUNEventLoop()
	go udpConn.runPacketEventLoop()
	go udpConn.runPeersHealthcheckLoop()
	return &udpConn, nil
}
