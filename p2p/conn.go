package p2p

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
	"tailscale.com/net/stun"
)

type PeerPacketConn struct {
	cfg                   Config
	networkID             peer.NetworkID
	udpConn               *net.UDPConn
	wsConn                *websocket.Conn
	closedSig             chan int
	peerKeepaliveInterval time.Duration

	peersMap  map[peer.PeerID]*PeerContext
	inbound   chan []byte
	peerEvent chan PeerEvent
	stunChan  chan []byte
	nonce     byte

	stunSession cmap.ConcurrentMap[string, STUNBindContext]

	peersMapMutex sync.RWMutex
	stunServers   []string
	localIPv6     []string
	localIPv4     []string
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
		err = io.ErrClosedPipe
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
	return len(p), c.writeToRelay(p, tgtPeer, 0)
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

func (c *PeerPacketConn) runWebSocketEventLoop() {
	for {
		mt, b, err := c.wsConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error(err.Error())
			}
			c.wsConn.Close()
			return
		}
		switch mt {
		case websocket.PingMessage:
			c.wsConn.WriteMessage(websocket.PongMessage, nil)
			continue
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ c.nonce
		}
		switch b[0] {
		case peer.CONTROL_RELAY:
			c.inbound <- b
		case peer.CONTROL_PRE_NAT_TRAVERSAL:
			go c.requestSTUN(b)
		case peer.CONTROL_NAT_TRAVERSAL:
			go c.traverseNAT(b)
		}
	}
}

func (c *PeerPacketConn) runPacketEventLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		n, peerAddr, err := c.udpConn.ReadFromUDP(buf)
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
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

func (c *PeerPacketConn) runPeerEventLoop() {
	handlePeerEvent := func(e PeerEvent) {
		c.peersMapMutex.Lock()
		defer c.peersMapMutex.Unlock()
		switch e.Op {
		case OP_PEER_DISCO: // 收到 peer addr
			if peerCtx, ok := c.peersMap[e.PeerID]; ok {
				peerCtx.Addr = e.Addr
				break
			}
			c.peersMap[e.PeerID] = &PeerContext{Addr: e.Addr, CreateTime: time.Now()}
		case OP_PEER_CONFIRM: // 确认事务
			slog.Debug("[UDP] Heartbeat", "peer", e.PeerID, "addr", e.Addr)
			if peer, ok := c.peersMap[e.PeerID]; ok {
				updated := time.Since(peer.LastActiveTime) > 2*c.peerKeepaliveInterval
				if updated {
					slog.Info("[UDP] Add peer", "peer", e.PeerID, "addr", e.Addr)
				}
				peer.LastActiveTime = time.Now()
				peer.Addr = e.Addr
			}
		case OP_PEER_HEALTHCHECK:
			for k, v := range c.peersMap {
				if time.Since(v.CreateTime) > 3*c.peerKeepaliveInterval &&
					time.Since(v.LastActiveTime) > 2*c.peerKeepaliveInterval {
					slog.Info("[UDP] Remove peer", "peer", k, "addr", v.Addr)
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
		for i := 0; i < 3; i++ {
			err := c.writeToRelay([]byte(saddr.String()), tx.PeerID, peer.CONTROL_NAT_TRAVERSAL)
			if err == nil {
				slog.Info("ListenUDP", "addr", saddr.String())
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		c.stunSession.Remove(string(txid[:]))
	}
}

func (c *PeerPacketConn) runPeersHealthcheckLoop() {
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
		if v.Addr == nil {
			continue
		}
		if time.Since(v.LastActiveTime) > 2*c.peerKeepaliveInterval {
			continue
		}
		if v.Addr.IP.Equal(udpAddr.IP) && v.Addr.Port == udpAddr.Port {
			return peer.PeerID(k)
		}
	}
	return ""
}

func (c *PeerPacketConn) findPeer(peerID peer.PeerID) (*PeerContext, bool) {
	c.peersMapMutex.RLock()
	defer c.peersMapMutex.RUnlock()
	if peer, ok := c.peersMap[peerID]; ok {
		return peer, time.Since(peer.LastActiveTime) <= 2*c.peerKeepaliveInterval
	}
	return nil, false
}

func (n *PeerPacketConn) writeToRelay(p []byte, peerID peer.PeerID, op byte) error {
	b := make([]byte, 0, 2+len(peerID)+len(p))
	b = append(b, op)                // relay
	b = append(b, peerID.Len())      // addr length
	b = append(b, peerID.Bytes()...) // addr
	b = append(b, p...)              // data
	for i, v := range b {
		b[i] = v ^ n.nonce
	}
	return n.wsConn.WriteMessage(websocket.BinaryMessage, b)
}

func (n *PeerPacketConn) writeToUDP(peerID peer.PeerID, p []byte) (int, error) {

	if peer, ok := n.findPeer(peerID); ok && peer.Addr != nil {
		slog.Debug("[UDP] WriteTo", "peer", peerID, "addr", peer.Addr)
		return n.udpConn.WriteToUDP(p, peer.Addr)
	}
	return 0, io.ErrClosedPipe
}

func (n *PeerPacketConn) requestSTUN(b []byte) {
	peerID := peer.PeerID(b[2 : b[1]+2])
	json.Unmarshal(b[b[1]+2:], &n.stunServers)
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

func (n *PeerPacketConn) traverseNAT(b []byte) {
	targetPeerID := peer.PeerID(b[2 : b[1]+2])
	targetUDPAddr := b[b[1]+2:]
	udpAddr, err := net.ResolveUDPAddr("udp", string(targetUDPAddr))
	if err != nil {
		slog.Error("Resolve udp addr error", "err", err)
		return
	}
	n.peerEvent <- PeerEvent{
		Op:     OP_PEER_DISCO,
		PeerID: targetPeerID,
		Addr:   udpAddr,
	}
	interval := 300 * time.Millisecond
	for i := 0; ; i++ {
		select {
		case <-n.closedSig:
			slog.Info("[UDP] Ping exit", "peer", targetPeerID)
			return
		default:
		}
		peerDiscovered := n.findPeerID(udpAddr) != ""
		if interval == n.peerKeepaliveInterval && !peerDiscovered {
			break
		}
		if peerDiscovered || i >= 50 {
			interval = n.peerKeepaliveInterval
		}
		n.pingPeer(targetPeerID)
		time.Sleep(interval)
	}
}

func (c *PeerPacketConn) pingPeer(peerID peer.PeerID) {
	c.peersMapMutex.RLock()
	defer c.peersMapMutex.RUnlock()
	if peer, ok := c.peersMap[peerID]; ok && peer.Addr != nil {
		slog.Debug("[UDP] Ping", "peer", peerID, "addr", peer.Addr)
		c.udpConn.WriteToUDP([]byte("_ping"+c.cfg.PeerID), peer.Addr)
		return
	}
	slog.Debug("[UDP] Ping peer not found", "peer", peerID)
}

func ListenPacket(networkID peer.NetworkID, servers []string, opts ...Option) (*PeerPacketConn, error) {
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

	var wsConn *websocket.Conn
	var nonce byte
	for _, server := range servers {
		handshake := http.Header{}
		handshake.Set("X-Network", string(networkID))
		handshake.Set("X-PeerID", string(cfg.PeerID))
		handshake.Set("X-Nonce", peer.NewNonce())
		conn, httpResp, err := websocket.DefaultDialer.Dial(server, handshake)
		if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
			return nil, fmt.Errorf("address[%s] is already in used", cfg.PeerID)
		}
		if err != nil {
			continue
		}
		wsConn = conn
		nonce = peer.MustParseNonce(httpResp.Header.Get("X-Nonce"))
		break
	}
	if wsConn == nil {
		return nil, errors.New("no peermap server available")
	}

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.UDPPort})
	if err != nil {
		return nil, fmt.Errorf("listen udp error: %w", err)
	}
	ips, err := ListLocalIPs()
	if err != nil {
		return nil, fmt.Errorf("list local ips error: %w", err)
	}
	var ipv6 []string
	var ipv4 []string

	for _, ip := range ips {
		if ip.To4() != nil {
			if cfg.DisableIPv4 {
				continue
			}
			ipv4 = append(ipv4, ip.String())
		} else {
			if cfg.DisableIPv6 {
				continue
			}
			ipv6 = append(ipv6, ip.String())
		}
		slog.Info("ListenUDP", "addr", net.JoinHostPort(ip.String(), fmt.Sprintf("%d", cfg.UDPPort)))
	}

	node := PeerPacketConn{
		cfg:                   cfg,
		networkID:             networkID,
		udpConn:               udpConn,
		wsConn:                wsConn,
		closedSig:             make(chan int),
		peerKeepaliveInterval: 5 * time.Second,
		peersMap:              make(map[peer.PeerID]*PeerContext),
		inbound:               make(chan []byte, 100),
		peerEvent:             make(chan PeerEvent),
		stunChan:              make(chan []byte),
		stunSession:           cmap.New[STUNBindContext](),
		nonce:                 nonce,
		localIPv6:             ipv6,
		localIPv4:             ipv4,
	}
	go node.runPacketEventLoop()
	go node.runPeerEventLoop()
	go node.runSTUNEventLoop()
	go node.runWebSocketEventLoop()
	go node.runPeersHealthcheckLoop()
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
