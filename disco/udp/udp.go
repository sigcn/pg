package udp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sigcn/pg/cache"
	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/stun"
	"golang.org/x/time/rate"
)

var (
	ErrUDPConnNotReady = errors.New("udpConn not ready yet")
	ErrUDPConnInactive = errors.New("udpConn inactive")

	_ PeerStore = (*UDPConn)(nil)
)

type UDPConn struct {
	udpConnsMutex sync.RWMutex
	udpConns      []*net.UDPConn

	closedSig chan int
	closedWG  sync.WaitGroup

	cfg              UDPConfig
	disco            *disco.Disco
	datagrams        chan *disco.Datagram
	natEvents        chan *disco.NATInfo
	udpAddrSends     chan *disco.PeerUDPAddr
	relayProtocol    relayProtocol
	upnpPortMapping  upnpPortMapping
	stunRoundTripper stunRoundTripper

	peersIndex      map[disco.PeerID]*peerkeeper
	peersIndexMutex sync.RWMutex

	natInfo atomic.Pointer[disco.NATInfo]

	cachePeers cache.CacheValue[[]PeerState]
}

func (c *UDPConn) Close() error {
	c.upnpPortMapping.close()
	close(c.closedSig)
	c.udpConnsMutex.RLock()
	for _, conn := range c.udpConns {
		conn.Close()
	}
	c.udpConnsMutex.RUnlock()
	c.closedWG.Wait()
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
		addr, err := c.upnpPortMapping.mappingAddress(c.cfg.Port)
		if err != nil {
			slog.Debug("[UPnP] Disabled", "reason", err)
			return
		}
		c.closedWG.Add(1)
		defer c.closedWG.Done()
		c.udpAddrSends <- &disco.PeerUDPAddr{ID: peerID, Addr: addr, Type: disco.UPnP}
		select {
		case c.natEvents <- &disco.NATInfo{Type: disco.UPnP, Addrs: []*net.UDPAddr{addr}}:
		default:
		}
	}()

	// LAN
	c.lanAddrsGenerate(peerID)

	// WAN
	time.AfterFunc(time.Second, func() {
		if _, ok := c.findPeer(peerID); ok {
			return
		}
		natInfo := c.DetectNAT(context.TODO(), stunServers)
		if natInfo.Addrs == nil {
			slog.Warn("No NAT addr found")
			return
		}
		c.closedWG.Add(1)
		defer c.closedWG.Done()
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

func (c *UDPConn) DetectNAT(ctx context.Context, stunServers []string) (info disco.NATInfo) {
	defer func() {
		lastNATInfo := c.natInfo.Load()
		slog.Log(context.Background(), -1, "[NAT] DetectNAT", "type", info.Type)
		c.natInfo.Store(&info)
		la := c.localAddrs()
		if len(la.pubIP4()) > 0 && len(la.pubIP6()) > 0 {
			info.Type = disco.IP46
			info.MergeAddrs(la.pubIP4())
			info.MergeAddrs(la.pubIP6())
		} else if info.Type == disco.Easy && len(la.pubIP6()) > 0 {
			info.Type = disco.EasyIP6
			info.MergeAddrs(la.pubIP6())
		} else if len(la.pubIP4()) > 0 {
			info.Type = disco.IP4
			info.MergeAddrs(la.pubIP4())
		}
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
	udpConn, err := c.getMainUDPConn()
	if err != nil {
		return
	}
	var udpAddrs []*net.UDPAddr
	var mutex sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(stunServers))
	for _, server := range stunServers {
		go func() {
			defer wg.Done()
			udpAddr, err := c.stunRoundTripper.roundTrip(ctx, udpConn, server)
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
		slog.Log(context.Background(), -2, "[UDP] HardChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr)
		hardChallenges(udpConn, &packetCounter)
		slog.Log(context.Background(), -2, "[UDP] HardChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr, "packet_count", packetCounter)
		return
	}

	slog.Log(context.Background(), -2, "[UDP] EasyChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr)
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
	slog.Log(context.Background(), -2, "[UDP] EasyChallenges", "peer", udpAddr.ID, "addr", udpAddr.Addr, "packet_count", packetCounter)

	if keeper, ok := c.findPeer(udpAddr.ID); (ok && keeper.ready()) || (udpAddr.Addr.IP.To4() == nil) || udpAddr.Addr.IP.IsPrivate() {
		return
	}

	// use main udpConn do port-scan
	udpConn, err := c.getMainUDPConn()
	if err != nil {
		return
	}
	packetCounter = 0
	slog.Log(context.Background(), -2, "[UDP] PortScan", "peer", udpAddr.ID, "addr", udpAddr.Addr)
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
	slog.Log(context.Background(), -2, "[UDP] PortScan", "peer", udpAddr.ID, "addr", udpAddr.Addr, "packet_count", packetCounter)
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

// Peers load all peers (peers order is stable)
func (c *UDPConn) Peers() []PeerState {
	return c.cachePeers.LoadTTL(time.Millisecond, func() (peers []PeerState) {
		c.peersIndexMutex.RLock()
		defer c.peersIndexMutex.RUnlock()
		for _, v := range c.peersIndex {
			v.statesMutex.RLock()
			for _, state := range v.states {
				peers = append(peers, *state)
			}
			v.statesMutex.RUnlock()
		}
		sort.SliceStable(peers, func(i, j int) bool {
			return strings.Compare(
				fmt.Sprintf("%s%s", peers[i].PeerID, peers[i].Addr),
				fmt.Sprintf("%s%s", peers[j].PeerID, peers[j].Addr)) > 0
		})
		return
	})
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
	go c.udpRead(conn)
	c.udpConns = append(c.udpConns, conn)

	if info := c.natInfo.Load(); info != nil && info.Type == disco.Hard {
		for i := range 255 {
			conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: c.cfg.Port + 1 + i})
			if err != nil {
				slog.Warn("[UDP] Listen", "err", err)
				continue
			}
			go c.udpRead(conn)
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

func (c *UDPConn) udpRead(udpConn *net.UDPConn) {
	c.closedWG.Add(1)
	defer c.closedWG.Done()
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
			if disco.IPIgnored(peerAddr.IP) { // ignore packet from ip in the ignore list
				continue
			}
			c.tryGetPeerkeeper(udpConn, peerID).heartbeat(peerAddr)
			continue
		}

		// stun response
		if stun.Is(buf[:n]) {
			c.stunRoundTripper.recvResponse(buf[:n], peerAddr)
			continue
		}

		// datagram
		peerID := c.findPeerID(peerAddr)
		if peerID.Len() == 0 {
			slog.Warn("[UDP] Recv udp packet but peer not found", "peer_addr", peerAddr)
			continue
		}
		c.tryGetPeerkeeper(udpConn, peerID).heartbeat(peerAddr)
		slog.Log(context.Background(), -3, "[UDP] ReadFrom", "peer", peerID, "addr", peerAddr)
		if pkt, dst := c.relayProtocol.tryToDst(buf[:n], peerID); pkt != nil {
			c.WriteTo(pkt, dst) // relay to dest
			continue
		}
		if pkt, src := c.relayProtocol.tryRecv(buf[:n]); pkt != nil {
			c.datagrams <- &disco.Datagram{PeerID: src, Data: pkt} // recv from relay
			continue
		}
		b := append([]byte(nil), buf[:n]...)
		c.datagrams <- &disco.Datagram{PeerID: peerID, Data: b}
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

func (c *UDPConn) findPeerID(udpAddr *net.UDPAddr) disco.PeerID {
	if udpAddr == nil {
		return ""
	}
	doFind := func(_ string) disco.PeerID {
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
	return cache.LoadTTL(udpAddr.String(), time.Millisecond, doFind)
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
		cfg:          cfg,
		disco:        &disco.Disco{Magic: cfg.DiscoMagic},
		closedSig:    make(chan int),
		natEvents:    make(chan *disco.NATInfo, 3),
		datagrams:    make(chan *disco.Datagram),
		udpAddrSends: make(chan *disco.PeerUDPAddr, 10),
		peersIndex:   make(map[disco.PeerID]*peerkeeper),
	}

	if err := udpConn.RestartListener(); err != nil {
		return nil, err
	}

	go udpConn.runPeersHealthcheckLoop()
	return &udpConn, nil
}

type PeerStore interface {
	// Peers load all peers (peers order is stable)
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
