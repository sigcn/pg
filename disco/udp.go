package disco

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/upnp"
	"tailscale.com/net/stun"
)

type UDPConn struct {
	*net.UDPConn
	closedSig             chan int
	peersOPs              chan *PeerOP
	datagrams             chan *Datagram
	stunResponse          chan []byte
	udpAddrSends          chan *PeerUDPAddrEvent
	peerKeepaliveInterval time.Duration
	id                    peer.PeerID
	peersMap              map[peer.PeerID]*PeerContext
	stunSessions          cmap.ConcurrentMap[string, STUNSession]

	localAddrs        []string
	upnpDeleteMapping func()

	peersMapMutex sync.RWMutex
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

func (c *UDPConn) GenerateLocalAddrsSends(peerID peer.PeerID, stunServers []string) {
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
		if ctx, ok := c.findPeer(peerID); !ok || !ctx.IPv4Ready() {
			c.requestSTUN(peerID, stunServers)
		}
	})
}

func (c *UDPConn) RunDiscoMessageSendLoop(peerID peer.PeerID, addr *net.UDPAddr) {
	c.peersOPs <- &PeerOP{
		Op:     OP_PEER_DISCO,
		PeerID: peerID,
		Addr:   addr,
	}
	defer slog.Debug("[UDP] Ping exit", "peer", peerID, "addr", addr)
	interval := 500 * time.Millisecond
	for i := 0; ; i++ {
		select {
		case <-c.closedSig:
			return
		default:
		}
		peerDiscovered := c.findPeerID(addr) != ""
		if interval == c.peerKeepaliveInterval && !peerDiscovered {
			break
		}
		if peerDiscovered || i >= 10 {
			interval = c.peerKeepaliveInterval
		}
		slog.Debug("[UDP] Ping", "peer", peerID, "addr", addr)
		c.UDPConn.WriteToUDP([]byte("_ping"+c.id), addr)
		time.Sleep(interval)
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
			return
		}

		// ping
		if n > 4 && string(buf[:5]) == "_ping" && n <= 260 {
			peerID := string(buf[5:n])
			c.peersOPs <- &PeerOP{
				Op:     OP_PEER_CONFIRM,
				PeerID: peer.PeerID(peerID),
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
		b := make([]byte, n)
		copy(b, buf[:n])
		c.datagrams <- &Datagram{
			PeerID: peerID,
			Data:   b,
		}
	}
}

func (c *UDPConn) runPeerOPLoop() {
	handlePeerEvent := func(e *PeerOP) {
		c.peersMapMutex.Lock()
		defer c.peersMapMutex.Unlock()
		switch e.Op {
		case OP_PEER_DELETE:
			delete(c.peersMap, e.PeerID)
		case OP_PEER_DISCO: // 收到 peer addr
			if _, ok := c.peersMap[e.PeerID]; !ok {
				peerCtx := PeerContext{
					States:     make(map[string]*PeerState),
					CreateTime: time.Now()}
				c.peersMap[e.PeerID] = &peerCtx
			}
			peerCtx := c.peersMap[e.PeerID]
			peerCtx.States[e.Addr.String()] = &PeerState{Addr: e.Addr}
		case OP_PEER_CONFIRM: // 确认事务
			slog.Debug("[UDP] Heartbeat", "peer", e.PeerID, "addr", e.Addr)
			if peer, ok := c.peersMap[e.PeerID]; ok {
				for _, state := range peer.States {
					if state.Addr.IP.Equal(e.Addr.IP) && state.Addr.Port == e.Addr.Port {
						updated := time.Since(state.LastActiveTime) > 2*c.peerKeepaliveInterval
						if updated {
							slog.Info("[UDP] AddPeer", "peer", e.PeerID, "addr", e.Addr)
						}
						state.LastActiveTime = time.Now()
					}
				}
			}
		case OP_PEER_HEALTHCHECK:
			for k, v := range c.peersMap {
				if time.Since(v.CreateTime) > 3*c.peerKeepaliveInterval {
					for addr, state := range v.States {
						if time.Since(state.LastActiveTime) > 2*c.peerKeepaliveInterval {
							if state.LastActiveTime.IsZero() {
								slog.Debug("[UDP] RemovePeer", "peer", k, "addr", state.Addr)
							} else {
								slog.Info("[UDP] RemovePeer", "peer", k, "addr", state.Addr)
							}
							delete(v.States, addr)
						}
					}
				}
				if len(v.States) == 0 {
					delete(c.peersMap, k)
				}
			}
		}
	}
	for {
		select {
		case <-c.closedSig:
			return
		case e := <-c.peersOPs:
			handlePeerEvent(e)
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
			slog.Error("Skipped unknown stun response", "txid", hex.EncodeToString(txid[:]))
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

func (c *UDPConn) requestSTUN(peerID peer.PeerID, stunServers []string) {
	txID := stun.NewTxID()
	c.stunSessions.Set(string(txID[:]), STUNSession{PeerID: peerID, CTime: time.Now()})
	for _, stunServer := range stunServers {
		uaddr, err := net.ResolveUDPAddr("udp", stunServer)
		if err != nil {
			slog.Error(err.Error())
			continue
		}
		_, err = c.UDPConn.WriteToUDP(stun.Request(txID), uaddr)
		if err != nil {
			slog.Error(err.Error())
			continue
		}
		time.Sleep(2 * time.Second)
		if _, ok := c.findPeer(peerID); ok {
			c.stunSessions.Remove(string(txID[:]))
			break
		}
	}
}

func (c *UDPConn) findPeerID(udpAddr *net.UDPAddr) peer.PeerID {
	if udpAddr == nil {
		return ""
	}
	c.peersMapMutex.RLock()
	defer c.peersMapMutex.RUnlock()
	for k, v := range c.peersMap {
		for _, state := range v.States {
			if !state.Addr.IP.Equal(udpAddr.IP) || state.Addr.Port != udpAddr.Port {
				continue
			}
			if time.Since(state.LastActiveTime) > 2*c.peerKeepaliveInterval {
				return ""
			}
			return peer.PeerID(k)
		}
	}
	return ""
}

func (c *UDPConn) findPeer(peerID peer.PeerID) (*PeerContext, bool) {
	c.peersMapMutex.RLock()
	defer c.peersMapMutex.RUnlock()
	if peer, ok := c.peersMap[peerID]; ok && peer.Ready() {
		return peer, true
	}
	return nil, false
}

func (n *UDPConn) WriteToUDP(p []byte, peerID peer.PeerID) (int, error) {
	if peer, ok := n.findPeer(peerID); ok {
		addr := peer.Select()
		slog.Debug("[UDP] WriteTo", "peer", peerID, "addr", addr)
		return n.UDPConn.WriteToUDP(p, addr)
	}
	return 0, ErrUseOfClosedConnection
}

func (c *UDPConn) Broadcast(b []byte) (peerCount int, err error) {
	c.peersMapMutex.RLock()
	peerCount = len(c.peersMap)
	peers := make([]peer.PeerID, 0, peerCount)
	for k := range c.peersMap {
		peers = append(peers, k)
	}
	c.peersMapMutex.RUnlock()

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

func ListenUDP(port int, disableIPv4, disableIPv6 bool, id peer.PeerID) (*UDPConn, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen udp error: %w", err)
	}
	ips, err := ListLocalIPs()
	if err != nil {
		return nil, fmt.Errorf("list local ips error: %w", err)
	}

	udpConn := UDPConn{
		UDPConn:               conn,
		closedSig:             make(chan int),
		peersOPs:              make(chan *PeerOP),
		datagrams:             make(chan *Datagram, 50),
		udpAddrSends:          make(chan *PeerUDPAddrEvent, 10),
		stunResponse:          make(chan []byte, 10),
		peerKeepaliveInterval: 10 * time.Second,
		id:                    id,
		peersMap:              make(map[peer.PeerID]*PeerContext),
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
