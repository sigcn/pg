package udp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/upnp"
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

var (
	ErrUDPConnNotReady = errors.New("udpConn not ready yet")
	ErrUDPConnInactive = errors.New("udpConn inactive")

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
	udpConnsMutex sync.RWMutex
	udpConns      []*net.UDPConn

	cfg           UDPConfig
	disco         *disco.Disco
	closedSig     chan int
	datagrams     chan *disco.Datagram
	natEvents     chan *disco.NATInfo
	udpAddrSends  chan *disco.PeerUDPAddr
	relayProtocol relayProtocol

	peersIndex      map[disco.PeerID]*peerkeeper
	peersIndexMutex sync.RWMutex

	stunResponseMapMutex sync.RWMutex
	stunResponseMap      map[string]chan stunResponse // key is stun txid
	natInfo              atomic.Pointer[disco.NATInfo]

	upnpDeleteMapping func()
}

func (c *UDPConn) Close() error {
	if c.upnpDeleteMapping != nil {
		c.upnpDeleteMapping()
	}
	c.udpConnsMutex.RLock()
	for _, conn := range c.udpConns {
		conn.Close()
	}
	c.udpConnsMutex.RUnlock()

	close(c.closedSig)
	close(c.natEvents)
	close(c.datagrams)
	close(c.udpAddrSends)
	return nil
}

// SetReadBuffer sets the size of the operating system's
// receive buffer associated with the connection.
func (c *UDPConn) SetReadBuffer(bytes int) error {
	c.udpConnsMutex.RLock()
	defer c.udpConnsMutex.RUnlock()
	for _, conn := range c.udpConns {
		conn.SetReadBuffer(bytes)
	}
	return nil
}

// SetWriteBuffer sets the size of the operating system's
// transmit buffer associated with the connection.
func (c *UDPConn) SetWriteBuffer(bytes int) error {
	c.udpConnsMutex.RLock()
	defer c.udpConnsMutex.RUnlock()
	for _, conn := range c.udpConns {
		conn.SetWriteBuffer(bytes)
	}
	return nil
}

func (c *UDPConn) NATEvents() <-chan *disco.NATInfo {
	return c.natEvents
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

		udpPort := c.cfg.Port
		for i := 0; i < 20; i++ {
			mappedPort, err := nat.AddPortMapping("udp", udpPort+i, udpPort, "peerguard", 24*3600)
			if err != nil {
				continue
			}
			c.upnpDeleteMapping = func() { nat.DeletePortMapping("udp", mappedPort, udpPort) }
			addr := &net.UDPAddr{IP: externalIP, Port: mappedPort}
			c.udpAddrSends <- &disco.PeerUDPAddr{
				ID:   peerID,
				Addr: addr,
				Type: disco.UPnP,
			}
			select {
			case c.natEvents <- &disco.NATInfo{Type: disco.UPnP, Addrs: []*net.UDPAddr{addr}}:
			default:
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
			select {
			case c.natEvents <- &disco.NATInfo{Type: natType, Addrs: []*net.UDPAddr{uaddr}}:
			default:
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
	udpConn, err := c.getMainUDPConn()
	if err != nil {
		return nil, err
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

func (c *UDPConn) DetectNAT(stunServers []string) (info disco.NATInfo) {
	defer func() {
		lastNATInfo := c.natInfo.Load()
		slog.Log(context.Background(), -1, "[NAT] DetectNAT", "type", info.Type)
		c.natInfo.Store(&info)
		select {
		case c.natEvents <- &info:
		default:
		}
		if info.Type == disco.Hard {
			if lastNATInfo == nil || lastNATInfo.Type != disco.Hard {
				if err := c.RestartListener(); err != nil {
					slog.Error("[UDP] RestartListener", "event", "to_hard", "err", err)
				}
			}
			return
		}
		if lastNATInfo != nil && lastNATInfo.Type == disco.Hard {
			if err := c.RestartListener(); err != nil {
				slog.Error("[UDP] RestartListener", "event", "to_easy", "err", err)
			}
		}
	}()
	var udpAddrs []*net.UDPAddr
	var mutex sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(stunServers))
	for _, server := range stunServers {
		go func() {
			defer wg.Done()
			udpAddr, err := c.RoundTripSTUN(server)
			if err != nil {
				slog.Log(context.Background(), -3, "[UDP] RoundTripSTUN", "server", server, "err", err)
				return
			}
			mutex.Lock()
			defer mutex.Unlock()
			udpAddrs = append(udpAddrs, udpAddr)
		}()
	}
	wg.Wait()

	if len(udpAddrs) <= 1 {
		return disco.NATInfo{Type: disco.Unknown, Addrs: udpAddrs}
	}
	lastAddr := udpAddrs[0].String()
	for _, addr := range udpAddrs {
		if lastAddr != addr.String() {
			return disco.NATInfo{Type: disco.Hard, Addrs: udpAddrs}
		}
	}
	return disco.NATInfo{Type: disco.Easy, Addrs: udpAddrs}
}

func (c *UDPConn) RunDiscoMessageSendLoop(udpAddr disco.PeerUDPAddr) {
	slog.Log(context.Background(), -2, "RecvPeerAddr", "peer", udpAddr.ID, "udp", udpAddr.Addr, "nat", udpAddr.Type.String())

	easyChallenges := func(udpConn *net.UDPConn, wg *sync.WaitGroup, packetCounter *int32) {
		defer wg.Done()
		atomic.AddInt32(packetCounter, 1)
		c.discoPing(udpConn, udpAddr.ID, udpAddr.Addr)
		randDelay, _ := rand.Int(rand.Reader, big.NewInt(50))

		interval := defaultDiscoConfig.ChallengesInitialInterval + time.Duration(randDelay.Int64()*int64(time.Millisecond))
		for i := 0; i < defaultDiscoConfig.ChallengesRetry; i++ {
			time.Sleep(interval)
			select {
			case <-c.closedSig:
				return
			default:
			}
			if c.findPeerID(udpAddr.Addr) != "" {
				return
			}
			c.discoPing(udpConn, udpAddr.ID, udpAddr.Addr)
			atomic.AddInt32(packetCounter, 1)
			interval = time.Duration(float64(interval) * defaultDiscoConfig.ChallengesBackoffRate)
		}
	}

	hardChallenges := func(udpConn *net.UDPConn, packetCounter *int32) {
		rl := rate.NewLimiter(rate.Limit(256), 256)
		for range 2000 {
			select {
			case <-c.closedSig:
				return
			default:
			}
			if ctx, ok := c.findPeer(udpAddr.ID); ok && ctx.ready() {
				slog.Info("[UDP] HardChallengesHit", "peer", udpAddr.ID)
				return
			}
			if err := rl.Wait(context.Background()); err != nil {
				slog.Error("[UDP] HardChallengesRateLimiter", "err", err)
				return
			}
			port, _ := rand.Int(rand.Reader, big.NewInt(65535-1024))
			udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), &net.UDPAddr{IP: udpAddr.Addr.IP, Port: int(port.Int64())})
			*packetCounter++
		}
	}

	if udpAddr.Type == disco.Hard {
		if info := c.natInfo.Load(); info != nil && info.Type == disco.Hard {
			return
		}
		udpConn, err := c.getMainUDPConn()
		if err != nil {
			slog.Error("[UDP] HardChallenges", "err", err)
			return
		}
		var packetCounter int32
		slog.Info("[UDP] HardChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr)
		hardChallenges(udpConn, &packetCounter)
		slog.Info("[UDP] HardChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr, "packet_count", packetCounter)
		return
	}

	slog.Log(context.Background(), -3, "[UDP] EasyChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr)
	var wg sync.WaitGroup
	c.udpConnsMutex.RLock()
	var packetCounter int32
	for _, conn := range c.udpConns {
		wg.Add(1)
		go easyChallenges(conn, &wg, &packetCounter)
		if udpAddr.Addr.IP.IsPrivate() {
			break
		}
	}
	c.udpConnsMutex.RUnlock()
	wg.Wait()
	slog.Log(context.Background(), -3, "[UDP] EasyChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr, "packet_count", packetCounter)

	if keeper, ok := c.findPeer(udpAddr.ID); (ok && keeper.ready()) || (udpAddr.Addr.IP.To4() == nil) || udpAddr.Addr.IP.IsPrivate() {
		return
	}

	// use main udpConn do port-scan
	udpConn, err := c.getMainUDPConn()
	if err != nil {
		return
	}
	packetCounter = 0
	slog.Log(context.Background(), -3, "[UDP] PortScan", "peer", udpAddr.ID, "addr", udpAddr.Addr)
	limit := defaultDiscoConfig.PortScanCount / max(1, int(defaultDiscoConfig.PortScanDuration.Seconds()))
	rl := rate.NewLimiter(rate.Limit(limit), limit)
	for port := udpAddr.Addr.Port + defaultDiscoConfig.PortScanOffset; port <= udpAddr.Addr.Port+defaultDiscoConfig.PortScanCount; port++ {
		select {
		case <-c.closedSig:
			return
		default:
		}
		p := port % 65536
		if p <= 1024 {
			continue
		}
		if keeper, ok := c.findPeer(udpAddr.ID); ok && keeper.ready() {
			slog.Info("[UDP] PortScanHit", "peer", udpAddr.ID, "port", p)
			return
		}
		if err := rl.Wait(context.Background()); err != nil {
			slog.Error("[UDP] PortScanRateLimiter", "err", err)
			return
		}
		udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), &net.UDPAddr{IP: udpAddr.Addr.IP, Port: p})
		packetCounter++
	}
	slog.Log(context.Background(), -3, "[UDP] PortScan", "peer", udpAddr.ID, "addr", udpAddr.Addr, "packet_count", packetCounter)
}

func (c *UDPConn) WriteTo(p []byte, peerID disco.PeerID) (int, error) {
	if peer, ok := c.findPeer(peerID); ok {
		return peer.writeUDP(p)
	}
	return 0, net.ErrClosed
}

func (c *UDPConn) RelayTo(relay disco.PeerID, p []byte, peerID disco.PeerID) (int, error) {
	return c.WriteTo(c.relayProtocol.toRelay(p, peerID), relay)
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
		_, err := c.WriteTo(b, peer)
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
	c.udpConnsMutex.Lock()
	defer c.udpConnsMutex.Unlock()

	// close the existing connection(s)
	for _, conn := range c.udpConns {
		conn.Close()
	}
	c.udpConns = c.udpConns[:0]

	// listen new connection(s)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: c.cfg.Port})
	if err != nil {
		return fmt.Errorf("listen udp error: %w", err)
	}
	go c.runPacketEventLoop(conn)
	c.udpConns = append(c.udpConns, conn)

	if info := c.natInfo.Load(); info != nil && info.Type == disco.Hard {
		for i := range 255 {
			conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: c.cfg.Port + 1 + i})
			if err != nil {
				slog.Warn("[UDP] Listen", "err", err)
				continue
			}
			go c.runPacketEventLoop(conn)
			c.udpConns = append(c.udpConns, conn)
		}
		slog.Info("[UDP] Listen 256 ports on hard side")
	}
	return nil
}

func (c *UDPConn) getMainUDPConn() (*net.UDPConn, error) {
	c.udpConnsMutex.RLock()
	defer c.udpConnsMutex.RUnlock()
	if c.udpConns == nil {
		return nil, ErrUDPConnNotReady
	}
	return c.udpConns[0], nil
}

func (c *UDPConn) discoPing(udpConn *net.UDPConn, peerID disco.PeerID, peerAddr *net.UDPAddr) {
	slog.Debug("[UDP] Ping", "peer", peerID, "addr", peerAddr)
	udpConn.WriteToUDP(c.disco.NewPing(c.cfg.ID), peerAddr)
}

func (c *UDPConn) localAddrs() []string {
	ips, err := disco.ListLocalIPs()
	if err != nil {
		slog.Error("[UDP] LocalAddrs", "err", err)
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
	slog.Log(context.Background(), -2, "[UDP] LocalAddrs", "addrs", detectIPs)
	return detectIPs
}

func (c *UDPConn) tryGetPeerkeeper(udpConn *net.UDPConn, peerID disco.PeerID) *peerkeeper {
	if !c.peersIndexMutex.TryRLock() {
		return nil
	}
	ctx, ok := c.peersIndex[peerID]
	c.peersIndexMutex.RUnlock()
	if ok {
		if ctx.udpConn.Load() != udpConn {
			ctx.udpConn.Store(udpConn)
		}
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
	pkeeper.udpConn.Store(udpConn)
	c.peersIndex[peerID] = &pkeeper
	go pkeeper.run()
	return &pkeeper
}

func (c *UDPConn) runPacketEventLoop(udpConn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		n, peerAddr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if strings.Contains(err.Error(), net.ErrClosed.Error()) {
				return
			}
			slog.Error("[UDP] ReadPacket", "err", err)
			time.Sleep(10 * time.Millisecond) // avoid busy wait
			continue
		}

		// ping
		if peerID := c.disco.ParsePing(buf[:n]); peerID.Len() > 0 {
			c.tryGetPeerkeeper(udpConn, peerID).heartbeat(peerAddr)
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
		c.tryGetPeerkeeper(udpConn, peerID).heartbeat(peerAddr)
		b := make([]byte, n)
		copy(b, buf[:n])
		slog.Log(context.Background(), -3, "[UDP] ReadFrom", "peer", peerID, "addr", peerAddr)
		if pkt, dst := c.relayProtocol.tryToDst(buf[:n], peerID); pkt != nil {
			c.WriteTo(pkt, dst)
			continue
		}
		if pkt, src := c.relayProtocol.tryRecv(buf[:n]); pkt != nil {
			c.datagrams <- &disco.Datagram{PeerID: src, Data: pkt}
			continue
		}
		defer func() { recover() }()
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
		select {
		case r <- resp:
		default:
		}
		slog.Log(context.Background(), -2, "[STUN] RecvResponse", "from", peerAddr, "pub_addr", addr)
		return
	}
	c.stunResponseMapMutex.RUnlock()
	slog.Log(context.Background(), -2, "[STUN] RecvResponse", "from", peerAddr)
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
		natEvents:       make(chan *disco.NATInfo),
		datagrams:       make(chan *disco.Datagram),
		udpAddrSends:    make(chan *disco.PeerUDPAddr, 10),
		peersIndex:      make(map[disco.PeerID]*peerkeeper),
		stunResponseMap: make(map[string]chan stunResponse),
	}

	if err := udpConn.RestartListener(); err != nil {
		return nil, err
	}

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
	udpConn    atomic.Pointer[net.UDPConn]
	peerID     disco.PeerID
	states     map[string]*PeerState // key is udp addr
	createTime time.Time

	exitSig           chan struct{}
	ping              func(udpConn *net.UDPConn, peerID disco.PeerID, addr *net.UDPAddr)
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
	peer.ping(peer.udpConn.Load(), peer.peerID, addr)
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

// selectPeerUDP select one of the multiple UDP addresses discovered by the peer
func (peer *peerkeeper) selectPeerUDP() *PeerState {
	candidates := make([]PeerState, 0, len(peer.states))
	peer.statesMutex.RLock()
	for _, state := range peer.states {
		if time.Since(state.LastActiveTime) < 2*(peer.keepaliveInterval+time.Second) {
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
	return &candidates[0]
}

func (peer *peerkeeper) writeUDP(p []byte) (int, error) {
	if peerState := peer.selectPeerUDP(); p != nil {
		slog.Log(context.Background(), -3, "[UDP] WriteTo", "peer", peer.peerID, "addr", peerState.Addr)
		if time.Since(peerState.LastActiveTime) > peer.keepaliveInterval+200*time.Millisecond {
			peer.udpConn.Load().WriteTo(p, peerState.Addr)
			return 0, ErrUDPConnInactive
		}
		return peer.udpConn.Load().WriteTo(p, peerState.Addr)
	}
	return 0, net.ErrClosed
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
			peer.ping(peer.udpConn.Load(), peer.peerID, addr)
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
