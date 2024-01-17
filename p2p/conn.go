package p2p

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
	"tailscale.com/net/stun"
)

type PeerPacketConn struct {
	cfg                   Config
	networkID             peer.NetworkID
	udpConn               *net.UDPConn
	serverConn            *peermapServerConn
	closedSig             chan int
	peerKeepaliveInterval time.Duration

	peermapServers []string
	peersMap       map[peer.PeerID]*PeerContext
	inbound        chan []byte
	peerEvent      chan PeerEvent
	stunChan       chan []byte
	stunSession    cmap.ConcurrentMap[string, STUNBindContext]
	localAddrs     []string

	peersMapMutex sync.RWMutex
	closeWait     sync.WaitGroup
	stunServers   []string
}

// ReadFrom reads a packet from the connection,
// copying the payload into p. It returns the number of
// bytes copied into p and the return address that
// was on the packet.
// It returns the number of bytes read (0 <= n <= len(p))
// and any error encountered. Callers should always process
// the n > 0 bytes returned before considering the error err.
// ReadFrom can be made to time out and return an error after a
// fixed time limit; see SetDeadline and SetReadDeadline.
func (c *PeerPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-c.closedSig:
		err = ErrUseOfClosedConnection
		c.Close()
		return
	default:
	}
	b := <-c.inbound
	addr = peer.PeerID(b[2 : b[1]+2])
	n = copy(p, b[b[1]+2:])
	return
}

// WriteTo writes a packet with payload p to addr.
// WriteTo can be made to time out and return an Error after a
// fixed time limit; see SetDeadline and SetWriteDeadline.
// On packet-oriented connections, write timeouts are rare.
func (c *PeerPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if _, ok := addr.(peer.PeerID); !ok {
		return 0, errors.New("not a p2p address")
	}
	tgtPeer := addr.(peer.PeerID)
	if _, ok := c.findPeer(tgtPeer); ok {
		return c.writeToUDP(tgtPeer, p)
	}
	slog.Debug("[Relay] WriteTo", "addr", tgtPeer)
	return len(p), c.serverConn.writeToRelay(p, tgtPeer, 0)
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
func (c *PeerPacketConn) Close() error {
	close(c.closedSig)
	return nil
}

// LocalAddr returns the local network address, if known.
func (c *PeerPacketConn) LocalAddr() net.Addr {
	return c.cfg.PeerID
}

// SetDeadline sets the read and write deadlines associated
// with the connection. It is equivalent to calling both
// SetReadDeadline and SetWriteDeadline.
//
// A deadline is an absolute time after which I/O operations
// fail instead of blocking. The deadline applies to all future
// and pending I/O, not just the immediately following call to
// Read or Write. After a deadline has been exceeded, the
// connection can be refreshed by setting a deadline in the future.
//
// If the deadline is exceeded a call to Read or Write or to other
// I/O methods will return an error that wraps os.ErrDeadlineExceeded.
// This can be tested using errors.Is(err, os.ErrDeadlineExceeded).
// The error's Timeout method will return true, but note that there
// are other possible errors for which the Timeout method will
// return true even if the deadline has not been exceeded.
//
// An idle timeout can be implemented by repeatedly extending
// the deadline after successful ReadFrom or WriteTo calls.
//
// A zero value for t means I/O operations will not time out.
func (c *PeerPacketConn) SetDeadline(t time.Time) error {
	return nil
}

// SetReadDeadline sets the deadline for future ReadFrom calls
// and any currently-blocked ReadFrom call.
// A zero value for t means ReadFrom will not time out.
func (c *PeerPacketConn) SetReadDeadline(t time.Time) error {
	return nil
}

// SetWriteDeadline sets the deadline for future WriteTo calls
// and any currently-blocked WriteTo call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means WriteTo will not time out.
func (c *PeerPacketConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *PeerPacketConn) listenUDP() error {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: c.cfg.UDPPort})
	if err != nil {
		return fmt.Errorf("listen udp error: %w", err)
	}
	c.udpConn = udpConn
	ips, err := ListLocalIPs()
	if err != nil {
		return fmt.Errorf("list local ips error: %w", err)
	}
	for _, ip := range ips {
		addr := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", c.cfg.UDPPort))
		if ip.To4() != nil {
			if c.cfg.DisableIPv4 {
				continue
			}
		} else {
			if c.cfg.DisableIPv6 {
				continue
			}
		}
		c.localAddrs = append(c.localAddrs, addr)
	}
	go c.runPacketEventLoop()
	return nil
}

func (c *PeerPacketConn) runPacketEventLoop() {
	c.closeWait.Add(1)
	defer c.closeWait.Done()
	buf := make([]byte, 65535)
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		n, peerAddr, err := c.udpConn.ReadFromUDP(buf)
		if err != nil {
			if !strings.Contains(err.Error(), ErrUseOfClosedConnection.Error()) {
				slog.Error("read from udp error", "err", err)
			}
			return
		}

		// ping
		if n > 4 && string(buf[:5]) == "_ping" && n <= 260 {
			peerID := string(buf[5:n])
			c.peerEvent <- PeerEvent{
				Op:     OP_PEER_CONFIRM,
				PeerID: peer.PeerID(peerID),
				Addr:   peerAddr,
			}
			continue
		}

		// stun
		if stun.Is(buf[:n]) {
			b := make([]byte, n)
			copy(b, buf[:n])
			c.stunChan <- b
			continue
		}

		// other
		peerID := c.findPeerID(peerAddr)
		b := make([]byte, 2+len(peerID)+n)
		b[1] = peerID.Len()
		copy(b[2:], peerID.Bytes())
		copy(b[2+len(peerID):], buf[:n])
		c.inbound <- b
	}
}

func (c *PeerPacketConn) runPeerFindEventLoop() {
	c.closeWait.Add(1)
	defer c.closeWait.Done()
	for {
		select {
		case <-c.closedSig:
			return
		case b := <-c.serverConn.preNTChannel():
			go c.traverseNAT1(b)
		case b := <-c.serverConn.doNTChannel():
			go c.traverseNAT2(b)
		}
	}
}

func (c *PeerPacketConn) runPeerEventLoop() {
	c.closeWait.Add(1)
	defer c.closeWait.Done()
	handlePeerEvent := func(e PeerEvent) {
		c.peersMapMutex.Lock()
		defer c.peersMapMutex.Unlock()
		switch e.Op {
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
		case e := <-c.peerEvent:
			handlePeerEvent(e)
		}
	}
}

func (c *PeerPacketConn) runSTUNEventLoop() {
	c.closeWait.Add(1)
	defer c.closeWait.Done()
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		stunResp := <-c.stunChan
		txid, saddr, err := stun.ParseResponse(stunResp)
		if err != nil {
			slog.Error("Skipped invalid stun response", "err", err.Error())
			continue
		}

		tx, ok := c.stunSession.Get(string(txid[:]))
		if !ok {
			slog.Error("Skipped unknown stun response", "txid", hex.EncodeToString(txid[:]))
			continue
		}

		if !saddr.IsValid() {
			slog.Error("Skipped invalid UDP addr", "addr", saddr)
			continue
		}
		c.dialPeerStartNT2(saddr.String(), tx.PeerID)
		c.stunSession.Remove(string(txid[:]))
	}
}

func (c *PeerPacketConn) runPeersHealthcheckLoop() {
	c.closeWait.Add(1)
	defer c.closeWait.Done()
	ticker := time.NewTicker(c.peerKeepaliveInterval/2 + time.Second)
	for {
		select {
		case <-c.closedSig:
			ticker.Stop()
			return
		case <-ticker.C:
			c.peerEvent <- PeerEvent{
				Op: OP_PEER_HEALTHCHECK,
			}
		}
	}
}

func (c *PeerPacketConn) findPeerID(udpAddr *net.UDPAddr) peer.PeerID {
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

func (c *PeerPacketConn) findPeer(peerID peer.PeerID) (*PeerContext, bool) {
	c.peersMapMutex.RLock()
	defer c.peersMapMutex.RUnlock()
	if peer, ok := c.peersMap[peerID]; ok && peer.Ready() {
		return peer, true
	}
	return nil, false
}

func (n *PeerPacketConn) writeToUDP(peerID peer.PeerID, p []byte) (int, error) {
	if peer, ok := n.findPeer(peerID); ok {
		addr := peer.Select()
		slog.Debug("[UDP] WriteTo", "peer", peerID, "addr", addr)
		return n.udpConn.WriteToUDP(p, addr)
	}
	return 0, ErrUseOfClosedConnection
}

func (n *PeerPacketConn) requestSTUN(peerID peer.PeerID) {
	txID := stun.NewTxID()
	n.stunSession.Set(string(txID[:]), STUNBindContext{PeerID: peerID, CTime: time.Now()})
	for _, stunServer := range n.stunServers {
		uaddr, err := net.ResolveUDPAddr("udp", stunServer)
		if err != nil {
			slog.Error(err.Error())
			continue
		}
		_, err = n.udpConn.WriteToUDP(stun.Request(txID), uaddr)
		if err != nil {
			slog.Error(err.Error())
			continue
		}
		time.Sleep(3 * time.Second)
		if _, ok := n.findPeer(peerID); ok {
			break
		}
	}
}

func (c *PeerPacketConn) dialPeerStartNT2(udpAddr string, targetPeerID peer.PeerID) {
	for i := 0; i < 3; i++ {
		err := c.serverConn.writeToRelay([]byte(udpAddr), targetPeerID, peer.CONTROL_NAT_TRAVERSAL)
		if err == nil {
			slog.Info("ListenUDP", "addr", udpAddr)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (c *PeerPacketConn) traverseNAT1(b []byte) {
	peerID := peer.PeerID(b[2 : b[1]+2])
	json.Unmarshal(b[b[1]+2:], &c.stunServers)
	for _, addr := range c.localAddrs {
		c.dialPeerStartNT2(addr, peerID)
	}
	time.AfterFunc(2*time.Second, func() {
		if ctx, ok := c.findPeer(peerID); !ok || !ctx.IPv4Ready() {
			c.requestSTUN(peerID)
		}
	})
}

func (c *PeerPacketConn) traverseNAT2(b []byte) {
	targetPeerID := peer.PeerID(b[2 : b[1]+2])
	targetUDPAddr := b[b[1]+2:]
	udpAddr, err := net.ResolveUDPAddr("udp", string(targetUDPAddr))
	if err != nil {
		slog.Error("Resolve udp addr error", "err", err)
		return
	}
	c.peerEvent <- PeerEvent{
		Op:     OP_PEER_DISCO,
		PeerID: targetPeerID,
		Addr:   udpAddr,
	}
	defer slog.Debug("[UDP] Ping exit", "peer", targetPeerID, "addr", udpAddr)
	interval := 500 * time.Millisecond
	for i := 0; ; i++ {
		select {
		case <-c.closedSig:
			return
		default:
		}
		peerDiscovered := c.findPeerID(udpAddr) != ""
		if interval == c.peerKeepaliveInterval && !peerDiscovered {
			break
		}
		if peerDiscovered || i >= 32 {
			interval = c.peerKeepaliveInterval
		}
		slog.Debug("[UDP] Ping", "peer", targetPeerID, "addr", udpAddr)
		c.udpConn.WriteToUDP([]byte("_ping"+c.cfg.PeerID), udpAddr)
		time.Sleep(interval)
	}
}

func ListenPacket(networkID peer.NetworkID, peermapServers []string, opts ...Option) (*PeerPacketConn, error) {
	id := make([]byte, 32)
	rand.Read(id)
	cfg := Config{
		UDPPort: 29877,
		PeerID:  peer.PeerID(base64.StdEncoding.EncodeToString(id)),
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("config error: %w", err)
		}
	}

	node := PeerPacketConn{
		cfg:                   cfg,
		networkID:             networkID,
		closedSig:             make(chan int),
		peerKeepaliveInterval: 10 * time.Second,

		peermapServers: peermapServers,
		peersMap:       make(map[peer.PeerID]*PeerContext),
		inbound:        make(chan []byte, 100),
		peerEvent:      make(chan PeerEvent),
		stunChan:       make(chan []byte),
		stunSession:    cmap.New[STUNBindContext](),
	}

	node.serverConn = &peermapServerConn{
		peermapServers: peermapServers,
		networkID:      networkID,
		peerID:         cfg.PeerID,
		inbound:        node.inbound,
		closedSig:      node.closedSig,
		preNT:          make(chan []byte, 10),
		doNT:           make(chan []byte, 10),
	}

	if err := node.serverConn.dialPeermapServer(); err != nil {
		return nil, err
	}
	if err := node.listenUDP(); err != nil {
		return nil, fmt.Errorf("listen udp error: %w", err)
	}

	go node.runPeerEventLoop()
	go node.runSTUNEventLoop()
	go node.runPeerFindEventLoop()
	go node.runPeersHealthcheckLoop()
	go node.serverConn.runWebSocketEventLoop()
	slog.Info("ListenPeer", "addr", node.LocalAddr())
	return &node, nil
}

func ListLocalIPs() ([]net.IP, error) {
	var ips []net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addresses {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			ips = append(ips, ipnet.IP)
		}
	}
	return ips, nil
}
