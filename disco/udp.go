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

	peersIndex      map[peer.ID]*peerkeeper
	peersIndexMutex sync.RWMutex

	stunSessionManager stunSessionManager

	upnpDeleteMapping func()

	natType NATType
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
				Type: UPnP,
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
		natType := Internal

		if uaddr.IP.IsGlobalUnicast() && !uaddr.IP.IsPrivate() {
			if uaddr.IP.To4() != nil {
				natType = IP4
			} else {
				natType = IP6
			}
		}
		c.udpAddrSends <- &PeerUDPAddr{
			ID:   peerID,
			Addr: uaddr,
			Type: natType,
		}
	}
	// WAN
	time.AfterFunc(time.Second, func() {
		if _, ok := c.findPeer(peerID); !ok {
			c.RequestSTUN(peerID, stunServers)
		}
	})
}

func (c *UDPConn) tryGetPeerkeeper(peerID peer.ID) *peerkeeper {
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
	pkeeper := peerkeeper{
		peerID:     peerID,
		states:     make(map[string]*PeerState),
		createTime: time.Now(),

		exitSig:           make(chan struct{}),
		ping:              c.discoPing,
		keepaliveInterval: c.cfg.PeerKeepaliveInterval,
	}
	c.peersIndex[peerID] = &pkeeper
	go pkeeper.run()
	return &pkeeper
}

func (c *UDPConn) RunDiscoMessageSendLoop(udpAddr PeerUDPAddr) {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return
	}
	slog.Log(context.Background(), -2, "RecvPeerAddr", "peer", udpAddr.ID, "udp", udpAddr.Addr, "nat", udpAddr.Type.String())
	defer slog.Debug("[UDP] DiscoExit", "peer", udpAddr.ID, "addr", udpAddr.Addr)
	c.discoPing(udpAddr.ID, udpAddr.Addr)
	interval := defaultDiscoConfig.ChallengesInitialInterval + time.Duration(rand.Intn(50)*int(time.Millisecond))
	for i := 0; i < defaultDiscoConfig.ChallengesRetry; i++ {
		time.Sleep(interval)
		select {
		case <-c.closedSig:
			return
		default:
		}
		c.discoPing(udpAddr.ID, udpAddr.Addr)
		interval = time.Duration(float64(interval) * defaultDiscoConfig.ChallengesBackoffRate)
		if c.findPeerID(udpAddr.Addr) != "" {
			return
		}
	}

	if ctx, ok := c.findPeer(udpAddr.ID); (ok && ctx.ready()) || (udpAddr.Addr.IP.To4() == nil) || udpAddr.Addr.IP.IsPrivate() {
		return
	}

	if slices.Contains([]NATType{Easy, IP4, IP6, UPnP}, udpAddr.Type) {
		return
	}

	slog.Info("[UDP] PortScanning", "peer", udpAddr.ID, "addr", udpAddr.Addr)
	scan := func(round int) bool {
		limit := defaultDiscoConfig.PortScanCount / 5
		rl := rate.NewLimiter(rate.Limit(limit), limit)
		for port := udpAddr.Addr.Port + defaultDiscoConfig.PortScanOffset; port <= udpAddr.Addr.Port+defaultDiscoConfig.PortScanCount; port++ {
			select {
			case <-c.closedSig:
				return false
			default:
			}
			p := port % 65536
			if p <= 1024 {
				continue
			}
			if ctx, ok := c.findPeer(udpAddr.ID); ok && ctx.ready() {
				slog.Info("[UDP] PortScanHit", "peer", udpAddr.ID, "round", round, "port", p)
				return true
			}
			if err := rl.Wait(context.Background()); err != nil {
				slog.Error("[UDP] PortScanRateLimiter", "err", err)
				return false
			}
			udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), &net.UDPAddr{IP: udpAddr.Addr.IP, Port: p})
		}
		return false
	}
	for i := range 2 {
		if scan(i + 1) {
			break
		}
	}
	slog.Info("[UDP] PortScanExit", "peer", udpAddr.ID, "addr", udpAddr.Addr)
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
			c.tryGetPeerkeeper(peerID).heartbeat(peerAddr)
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
		c.tryGetPeerkeeper(peerID).heartbeat(peerAddr)
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
		tx.addrs = append(tx.addrs, addr.String())
		natAddrFound := func(t NATType) {
			if tx.peerID == "" {
				c.natType = t
				slog.Log(context.Background(), -1, "NATAddrFound", "addr", addr, "type", t)
				return
			}
			c.udpAddrSends <- &PeerUDPAddr{ID: tx.peerID, Addr: addr, Type: t}
		}
		if len(tx.addrs) == 1 {
			tx.timer = time.AfterFunc(3*time.Second, func() {
				c.stunSessionManager.Remove(string(txid[:]))
				if len(tx.addrs) > 1 {
					natAddrFound(Hard)
					return
				}
				natAddrFound(Unknown)
			})
			continue
		}
		if len(slices.Compact(tx.addrs)) == 1 {
			tx.timer.Stop()
			c.stunSessionManager.Remove(string(txid[:]))
			natAddrFound(Easy)
			continue
		}
		natAddrFound(Hard)
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
				v.healthcheck()
				if len(v.states) == 0 {
					v.close()
					delete(c.peersIndex, k)
				}
			}
			c.peersIndexMutex.Unlock()
		}
	}
}

func (c *UDPConn) RequestSTUN(peerID peer.ID, stunServers []string) {
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
		for _, state := range v.states {
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
func (c *UDPConn) findPeer(peerID peer.ID) (*peerkeeper, bool) {
	c.peersIndexMutex.RLock()
	defer c.peersIndexMutex.RUnlock()
	if peer, ok := c.peersIndex[peerID]; ok && peer.ready() {
		return peer, true
	}
	return nil, false
}

func (c *UDPConn) WriteToUDP(p []byte, peerID peer.ID) (int, error) {
	if peer, ok := c.findPeer(peerID); ok {
		if addr := peer.selectUDPAddr(); addr != nil {
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
		for _, state := range v.states {
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
		peersIndex:         make(map[peer.ID]*peerkeeper),
		stunSessionManager: stunSessionManager{sessions: make(map[string]*stunSession)},
	}

	if err := udpConn.RestartListener(); err != nil {
		return nil, err
	}

	go udpConn.runSTUNEventLoop()
	go udpConn.runPacketEventLoop()
	go udpConn.runPeersHealthcheckLoop()
	return &udpConn, nil
}

type PeerStore interface {
	Peers() []PeerState
}

type PeerState struct {
	PeerID         peer.ID
	Addr           *net.UDPAddr
	LastActiveTime time.Time
}

type stunSession struct {
	peerID peer.ID
	cTime  time.Time
	addrs  []string
	timer  *time.Timer
}

type stunSessionManager struct {
	sync.RWMutex
	sessions map[string]*stunSession
}

func (m *stunSessionManager) Get(txid string) (*stunSession, bool) {
	m.RLock()
	defer m.RUnlock()
	s, ok := m.sessions[txid]
	return s, ok
}

func (m *stunSessionManager) Set(txid string, peerID peer.ID) {
	m.Lock()
	defer m.Unlock()
	m.sessions[txid] = &stunSession{peerID: peerID, cTime: time.Now()}
}

func (m *stunSessionManager) Remove(txid string) {
	m.Lock()
	defer m.Unlock()
	delete(m.sessions, txid)
}

type peerkeeper struct {
	peerID     peer.ID
	states     map[string]*PeerState // key is udp addr
	createTime time.Time

	exitSig           chan struct{}
	ping              func(peerID peer.ID, addr *net.UDPAddr)
	keepaliveInterval time.Duration

	statesMutex sync.RWMutex
}

func (peer *peerkeeper) heartbeat(addr *net.UDPAddr) {
	if peer == nil || !peer.statesMutex.TryLock() {
		return
	}
	defer peer.statesMutex.Unlock()
	slog.Log(context.Background(), -5, "[UDP] Heartbeat", "peer", peer.peerID, "addr", addr)
	for _, state := range peer.states {
		if state.Addr.IP.Equal(addr.IP) && state.Addr.Port == addr.Port {
			state.LastActiveTime = time.Now()
			return
		}
	}
	slog.Info("[UDP] AddPeer", "peer", peer.peerID, "addr", addr)
	peer.states[addr.String()] = &PeerState{Addr: addr, LastActiveTime: time.Now(), PeerID: peer.peerID}
	peer.ping(peer.peerID, addr)
}

func (peer *peerkeeper) healthcheck() {
	if time.Since(peer.createTime) > 3*peer.keepaliveInterval {
		for addr, state := range peer.states {
			if time.Since(state.LastActiveTime) > 2*peer.keepaliveInterval+time.Second {
				slog.Info("[UDP] RemovePeer", "peer", peer.peerID, "addr", state.Addr)
				peer.statesMutex.Lock()
				delete(peer.states, addr)
				peer.statesMutex.Unlock()
			}
		}
	}
}

// ready when peer context has at least one active udp address
func (peer *peerkeeper) ready() bool {
	peer.statesMutex.RLock()
	defer peer.statesMutex.RUnlock()
	for _, state := range peer.states {
		if time.Since(state.LastActiveTime) <= peer.keepaliveInterval+2*time.Second {
			return true
		}
	}
	return false
}

func (peer *peerkeeper) selectUDPAddr() *net.UDPAddr {
	candidates := make([]PeerState, 0, len(peer.states))
	peer.statesMutex.RLock()
	for _, state := range peer.states {
		if time.Since(state.LastActiveTime) < peer.keepaliveInterval+2*time.Second {
			candidates = append(candidates, *state)
		}
	}
	peer.statesMutex.RUnlock()
	if len(candidates) == 0 {
		return nil
	}
	slices.SortFunc(candidates, func(c1, c2 PeerState) int {
		if c1.LastActiveTime.After(c2.LastActiveTime) {
			return -1
		}
		return 1
	})
	return candidates[0].Addr
}

func (peer *peerkeeper) run() {
	ticker := time.NewTicker(peer.keepaliveInterval)
	ping := func() {
		addrs := make([]*net.UDPAddr, 0, len(peer.states))
		peer.statesMutex.RLock()
		for _, v := range peer.states {
			addrs = append(addrs, v.Addr)
		}
		peer.statesMutex.RUnlock()
		for _, addr := range addrs {
			peer.ping(peer.peerID, addr)
		}
	}
	for {
		select {
		case <-peer.exitSig:
			ticker.Stop()
			slog.Debug("[UDP] KeepaliveExit", "peer", peer.peerID)
			return
		case <-ticker.C:
			ping()
		}
	}
}

func (peer *peerkeeper) close() error {
	close(peer.exitSig)
	return nil
}
