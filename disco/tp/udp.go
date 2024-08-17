package tp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/upnp"
	"golang.org/x/time/rate"
	"tailscale.com/net/stun"
)

var defaultDiscoConfig = DiscoConfig{
	PortScanOffset:            -1000,
	PortScanCount:             3000,
	PortScanDuration:          5 * time.Second,
	ChallengesRetry:           5,
	ChallengesInitialInterval: 200 * time.Millisecond,
	ChallengesBackoffRate:     1.65,
}

type DiscoConfig struct {
	PortScanOffset            int
	PortScanCount             int
	PortScanDuration          time.Duration
	ChallengesRetry           int
	ChallengesInitialInterval time.Duration
	ChallengesBackoffRate     float64
}

func SetModifyDiscoConfig(modify func(cfg *DiscoConfig)) {
	if modify != nil {
		modify(&defaultDiscoConfig)
	}
	defaultDiscoConfig.PortScanOffset = max(min(defaultDiscoConfig.PortScanOffset, 65535), -65535)
	defaultDiscoConfig.PortScanCount = min(max(32, defaultDiscoConfig.PortScanCount), 65535-1024)
	defaultDiscoConfig.PortScanDuration = max(time.Second, defaultDiscoConfig.PortScanDuration)
	defaultDiscoConfig.ChallengesRetry = max(1, defaultDiscoConfig.ChallengesRetry)
	defaultDiscoConfig.ChallengesInitialInterval = max(10*time.Millisecond, defaultDiscoConfig.ChallengesInitialInterval)
	defaultDiscoConfig.ChallengesBackoffRate = max(1, defaultDiscoConfig.ChallengesBackoffRate)
}

type NATInfo struct {
	Type  disco.NATType
	Addrs []*net.UDPAddr
}

var (
	ErrUDPConnNotReady = errors.New("udpConn not ready yet")

	_ PeerStore = (*UDPConn)(nil)
)

type UDPConfig struct {
	Port                  int
	DisableIPv4           bool
	DisableIPv6           bool
	ID                    disco.PeerID
	PeerKeepaliveInterval time.Duration
	DiscoMagic            func() []byte
}

type UDPConn struct {
	rawConn      atomic.Pointer[net.UDPConn]
	cfg          UDPConfig
	disco        *disco.Disco
	closedSig    chan int
	datagrams    chan *disco.Datagram
	udpAddrSends chan *disco.PeerUDPAddr

	peersIndex      map[disco.PeerID]*peerkeeper
	peersIndexMutex sync.RWMutex

	stunResponseMapMutex sync.RWMutex
	stunResponseMap      map[string]chan stunResponse // key is stun txid
	natInfo              atomic.Pointer[NATInfo]

	upnpDeleteMapping func()
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

func (c *UDPConn) Datagrams() <-chan *disco.Datagram {
	return c.datagrams
}

func (c *UDPConn) UDPAddrSends() <-chan *disco.PeerUDPAddr {
	return c.udpAddrSends
}

func (c *UDPConn) GenerateLocalAddrsSends(peerID disco.PeerID, stunServers []string) {
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
			c.udpAddrSends <- &disco.PeerUDPAddr{
				ID:   peerID,
				Addr: &net.UDPAddr{IP: externalIP, Port: mappedPort},
				Type: disco.UPnP,
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
		natType := disco.Internal

		if uaddr.IP.IsGlobalUnicast() && !uaddr.IP.IsPrivate() {
			if uaddr.IP.To4() != nil {
				natType = disco.IP4
			} else {
				natType = disco.IP6
			}
		}
		c.udpAddrSends <- &disco.PeerUDPAddr{
			ID:   peerID,
			Addr: uaddr,
			Type: natType,
		}
	}
	// WAN
	time.AfterFunc(time.Second, func() {
		if _, ok := c.findPeer(peerID); ok {
			return
		}
		natInfo := c.DetectNAT(stunServers)
		if natInfo.Addrs == nil {
			slog.Warn("No NAT addr found")
			return
		}
		if natInfo.Type == disco.Hard {
			for _, addr := range natInfo.Addrs {
				c.udpAddrSends <- &disco.PeerUDPAddr{
					ID:   peerID,
					Addr: addr,
					Type: natInfo.Type,
				}
			}
			return
		}
		c.udpAddrSends <- &disco.PeerUDPAddr{
			ID:   peerID,
			Addr: natInfo.Addrs[0],
			Type: natInfo.Type,
		}
	})
}

func (c *UDPConn) RoundTripSTUN(stunServer string) (*net.UDPAddr, error) {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return nil, errors.New("udp conn not ready yet")
	}
	txID := stun.NewTxID()
	ch := make(chan stunResponse)
	defer close(ch)
	c.stunResponseMapMutex.Lock()
	c.stunResponseMap[string(txID[:])] = ch
	c.stunResponseMapMutex.Unlock()
	uaddr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return nil, fmt.Errorf("resolve stun addr: %w", err)
	}
	_, err = udpConn.WriteToUDP(stun.Request(txID), uaddr)
	if err != nil {
		return nil, fmt.Errorf("write udp: %w", err)
	}

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	select {
	case r := <-ch:
		return r.addr, nil
	case <-timeout.C:
		return nil, os.ErrDeadlineExceeded
	}
}

func (c *UDPConn) DetectNAT(stunServers []string) NATInfo {
	var udpAddrs []*net.UDPAddr
	var mutex sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(stunServers))
	for _, server := range stunServers {
		go func() {
			defer wg.Done()
			udpAddr, err := c.RoundTripSTUN(server)
			if err != nil {
				slog.Log(context.Background(), -3, "RoundTripSTUN", "server", server, "err", err)
				return
			}
			mutex.Lock()
			defer mutex.Unlock()
			udpAddrs = append(udpAddrs, udpAddr)
		}()
	}
	wg.Wait()

	if len(udpAddrs) <= 1 {
		slog.Log(context.Background(), -1, "[NAT] TypeDetected", "type", disco.Unknown)
		c.natInfo.Store(&NATInfo{Type: disco.Unknown, Addrs: udpAddrs})
		return *c.natInfo.Load()
	}
	lastAddr := udpAddrs[0].String()
	for _, addr := range udpAddrs {
		if lastAddr != addr.String() {
			slog.Log(context.Background(), -1, "[NAT] TypeDetected", "type", disco.Hard)
			c.natInfo.Store(&NATInfo{Type: disco.Hard, Addrs: udpAddrs})
			return *c.natInfo.Load()
		}
	}
	slog.Log(context.Background(), -1, "[NAT] TypeDetected", "type", disco.Easy)
	c.natInfo.Store(&NATInfo{Type: disco.Easy, Addrs: udpAddrs})
	return *c.natInfo.Load()
}

func (c *UDPConn) RunDiscoMessageSendLoop(udpAddr disco.PeerUDPAddr) {
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

	if slices.Contains([]disco.NATType{disco.Easy, disco.IP4, disco.IP6, disco.UPnP}, udpAddr.Type) {
		return
	}

	slog.Info("[UDP] PortScanning", "peer", udpAddr.ID, "addr", udpAddr.Addr)
	scan := func(round int) bool {
		limit := defaultDiscoConfig.PortScanCount / max(1, int(defaultDiscoConfig.PortScanDuration.Seconds()))
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

func (c *UDPConn) WriteToUDP(p []byte, peerID disco.PeerID) (int, error) {
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
	return 0, net.ErrClosed
}

func (c *UDPConn) Broadcast(b []byte) (peerCount int, err error) {
	c.peersIndexMutex.RLock()
	peerCount = len(c.peersIndex)
	peers := make([]disco.PeerID, 0, peerCount)
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

func (c *UDPConn) discoPing(peerID disco.PeerID, peerAddr *net.UDPAddr) {
	udpConn := c.rawConn.Load()
	if udpConn == nil {
		return
	}
	slog.Debug("[UDP] DiscoPing", "peer", peerID, "addr", peerAddr)
	udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), peerAddr)
}

func (c *UDPConn) localAddrs() []string {
	ips, err := disco.ListLocalIPs()
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

func (c *UDPConn) tryGetPeerkeeper(peerID disco.PeerID) *peerkeeper {
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
			if !strings.Contains(err.Error(), net.ErrClosed.Error()) {
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
			c.recvSTUNResponse(buf[:n], peerAddr)
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
		c.datagrams <- &disco.Datagram{PeerID: peerID, Data: b}
	}
}

func (c *UDPConn) recvSTUNResponse(b []byte, peerAddr net.Addr) {
	txid, saddr, err := stun.ParseResponse(b)
	if err != nil {
		slog.Error("[STUN] ParseResponse", "stun", peerAddr, "err", fmt.Errorf("parse: %w", err))
		return
	}
	c.stunResponseMapMutex.RLock()
	if r, ok := c.stunResponseMap[string(txid[:])]; ok {
		c.stunResponseMapMutex.RUnlock()
		addr, err := net.ResolveUDPAddr("udp", saddr.String())
		if err != nil {
			slog.Error("[STUN] ParseResponse", "stun", peerAddr, "err", fmt.Errorf("resolve udp addr: %w", err))
			return
		}
		resp := stunResponse{txid: string(txid[:]), addr: addr}
		r <- resp
		slog.Log(context.Background(), -3, "[STUN] RecvResponse", "from", peerAddr, "pub_addr", addr)
		return
	}
	c.stunResponseMapMutex.RUnlock()
	slog.Log(context.Background(), -3, "[STUN] RecvResponse", "from", peerAddr)
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

func (c *UDPConn) findPeerID(udpAddr *net.UDPAddr) disco.PeerID {
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
func (c *UDPConn) findPeer(peerID disco.PeerID) (*peerkeeper, bool) {
	c.peersIndexMutex.RLock()
	defer c.peersIndexMutex.RUnlock()
	if peer, ok := c.peersIndex[peerID]; ok && peer.ready() {
		return peer, true
	}
	return nil, false
}

func ListenUDP(cfg UDPConfig) (*UDPConn, error) {
	if cfg.ID.Len() == 0 {
		return nil, errors.New("peer id is required")
	}
	if cfg.PeerKeepaliveInterval < time.Second {
		cfg.PeerKeepaliveInterval = 10 * time.Second
	}

	udpConn := UDPConn{
		cfg:             cfg,
		disco:           &disco.Disco{Magic: cfg.DiscoMagic},
		closedSig:       make(chan int),
		datagrams:       make(chan *disco.Datagram),
		udpAddrSends:    make(chan *disco.PeerUDPAddr, 10),
		peersIndex:      make(map[disco.PeerID]*peerkeeper),
		stunResponseMap: make(map[string]chan stunResponse),
	}

	if err := udpConn.RestartListener(); err != nil {
		return nil, err
	}

	go udpConn.runPacketEventLoop()
	go udpConn.runPeersHealthcheckLoop()
	return &udpConn, nil
}

type PeerStore interface {
	Peers() []PeerState
}

type PeerState struct {
	PeerID         disco.PeerID
	Addr           *net.UDPAddr
	LastActiveTime time.Time
}

type stunResponse struct {
	txid string
	addr *net.UDPAddr
}

type peerkeeper struct {
	peerID     disco.PeerID
	states     map[string]*PeerState // key is udp addr
	createTime time.Time

	exitSig           chan struct{}
	ping              func(peerID disco.PeerID, addr *net.UDPAddr)
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
