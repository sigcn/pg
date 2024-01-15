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

type Node struct {
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
}

func (n *Node) ListenPacket() net.PacketConn {
	slog.Info("Listen packet", "id", n.cfg.PeerID)
	return &PeerPacketConn{
		node:   n,
		peerID: n.cfg.PeerID,
	}
}

func (n *Node) Close() error {
	close(n.closedSig)
	return nil
}

func (n *Node) runWebSocketEventLoop() {
	for {
		mt, b, err := n.wsConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error(err.Error())
			}
			n.wsConn.Close()
			return
		}
		switch mt {
		case websocket.PingMessage:
			n.wsConn.WriteMessage(websocket.PongMessage, nil)
			continue
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ n.nonce
		}
		switch b[0] {
		case peer.CONTROL_RELAY:
			n.inbound <- b
		case peer.CONTROL_PRE_NAT_TRAVERSAL:
			go n.requestSTUN(b)
		case peer.CONTROL_NAT_TRAVERSAL:
			go n.traverseNAT(b)
		}
	}
}

func (n *Node) runPacketEventLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-n.closedSig:
			return
		default:
		}
		c, peerAddr, err := n.udpConn.ReadFromUDP(buf)
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				slog.Error("read from udp error", "err", err)
			}
			return
		}

		// ping
		if c > 4 && string(buf[:5]) == "_ping" && c <= 260 {
			peerID := string(buf[5:c])
			n.peerEvent <- PeerEvent{
				Op:     OP_PEER_CONFIRM,
				PeerID: peer.PeerID(peerID),
				Addr:   peerAddr,
			}
			continue
		}

		// stun
		if stun.Is(buf[:c]) {
			b := make([]byte, c)
			copy(b, buf[:c])
			n.stunChan <- b
			continue
		}

		// other
		peerID := n.findPeerID(peerAddr)
		b := make([]byte, 2+len(peerID)+c)
		b[1] = peerID.Len()
		copy(b[2:], peerID.Bytes())
		copy(b[2+len(peerID):], buf[:c])
		n.inbound <- b
	}
}

func (n *Node) runPeerEventLoop() {
	handlePeerEvent := func(e PeerEvent) {
		n.peersMapMutex.Lock()
		defer n.peersMapMutex.Unlock()
		switch e.Op {
		case OP_PEER_DISCO: // 收到 peer addr
			if peerCtx, ok := n.peersMap[e.PeerID]; ok {
				peerCtx.Addr = e.Addr
				break
			}
			n.peersMap[e.PeerID] = &PeerContext{Addr: e.Addr, CreateTime: time.Now()}
		case OP_PEER_CONFIRM: // 确认事务
			slog.Debug("[UDP] Heartbeat", "peer", e.PeerID, "addr", e.Addr)
			if peer, ok := n.peersMap[e.PeerID]; ok {
				updated := time.Since(peer.LastActiveTime) > 2*n.peerKeepaliveInterval
				if updated {
					slog.Info("[UDP] Add peer", "peer", e.PeerID, "addr", e.Addr)
				}
				peer.LastActiveTime = time.Now()
				peer.Addr = e.Addr
			}
		case OP_PEER_HEALTHCHECK:
			for k, v := range n.peersMap {
				if time.Since(v.CreateTime) > n.peerKeepaliveInterval &&
					time.Since(v.LastActiveTime) > n.peerKeepaliveInterval {
					slog.Info("[UDP] Remove peer", "peer", k, "addr", v.Addr)
					delete(n.peersMap, k)
				}
			}
		}
	}
	for {
		select {
		case <-n.closedSig:
			return
		case e := <-n.peerEvent:
			handlePeerEvent(e)
		}
	}
}

func (n *Node) runSTUNEventLoop() {
	for {
		select {
		case <-n.closedSig:
			return
		default:
		}
		stunResp := <-n.stunChan
		txid, saddr, err := stun.ParseResponse(stunResp)
		if err != nil {
			slog.Error("Skipped invalid stun response", "err", err.Error())
			continue
		}

		tx, ok := n.stunSession.Get(string(txid[:]))
		if !ok {
			slog.Error("Skipped unknown stun response", "txid", hex.EncodeToString(txid[:]))
			continue
		}

		if !saddr.IsValid() {
			slog.Error("Skipped invalid UDP addr", "addr", saddr)
			continue
		}
		for i := 0; i < 3; i++ {
			err := n.writeToRelay([]byte(saddr.String()), tx.PeerID, peer.CONTROL_NAT_TRAVERSAL)
			if err == nil {
				slog.Info("[UDP] Public address found", "addr", saddr.String())
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		n.stunSession.Remove(string(txid[:]))
	}
}

func (n *Node) runPeersHealthcheckLoop() {
	ticker := time.NewTicker(n.peerKeepaliveInterval/2 + time.Second)
	for {
		select {
		case <-n.closedSig:
			ticker.Stop()
			return
		case <-ticker.C:
			n.peerEvent <- PeerEvent{
				Op: OP_PEER_HEALTHCHECK,
			}
		}
	}
}

func (n *Node) findPeerID(udpAddr *net.UDPAddr) peer.PeerID {
	if udpAddr == nil {
		return ""
	}
	n.peersMapMutex.RLock()
	defer n.peersMapMutex.RUnlock()
	for k, v := range n.peersMap {
		if v.Addr == nil {
			continue
		}
		if time.Since(v.LastActiveTime) > 2*n.peerKeepaliveInterval {
			continue
		}
		if v.Addr.IP.Equal(udpAddr.IP) && v.Addr.Port == udpAddr.Port {
			return peer.PeerID(k)
		}
	}
	return ""
}

func (n *Node) findPeer(peerID peer.PeerID) (*PeerContext, bool) {
	n.peersMapMutex.RLock()
	defer n.peersMapMutex.RUnlock()
	if peer, ok := n.peersMap[peerID]; ok {
		return peer, time.Since(peer.LastActiveTime) <= 2*n.peerKeepaliveInterval
	}
	return nil, false
}

func (n *Node) writeToRelay(p []byte, peerID peer.PeerID, op byte) error {
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

func (n *Node) writeToUDP(peerID peer.PeerID, p []byte) (int, error) {

	if peer, ok := n.findPeer(peerID); ok && peer.Addr != nil {
		slog.Debug("[UDP] WriteTo", "peer", peerID, "addr", peer.Addr)
		return n.udpConn.WriteToUDP(p, peer.Addr)
	}
	return 0, io.ErrClosedPipe
}

func (n *Node) requestSTUN(b []byte) {
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

func (n *Node) traverseNAT(b []byte) {
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
		if peerDiscovered || i >= 32 {
			interval = n.peerKeepaliveInterval
		}
		n.pingPeer(targetPeerID)
		time.Sleep(interval)
	}
}

func (n *Node) pingPeer(peerID peer.PeerID) {
	n.peersMapMutex.RLock()
	defer n.peersMapMutex.RUnlock()
	if peer, ok := n.peersMap[peerID]; ok && peer.Addr != nil {
		slog.Debug("[UDP] Ping", "peer", peerID, "addr", peer.Addr)
		n.udpConn.WriteToUDP([]byte("_ping"+n.cfg.PeerID), peer.Addr)
		return
	}
	slog.Debug("[UDP] Ping peer not found", "peer", peerID)
}

type Config struct {
	UDPPort int
	PeerID  peer.PeerID
}

type Option func(cfg *Config) error

func ListenUDPPort(port int) Option {
	return func(cfg *Config) error {
		cfg.UDPPort = port
		return nil
	}
}

func ListenPeerID(peerID peer.PeerID) Option {
	return func(cfg *Config) error {
		if peerID.Len() > 0 {
			cfg.PeerID = peerID
		}
		return nil
	}
}

func New(networkID peer.NetworkID, servers []string, opts ...Option) (*Node, error) {
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
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.UDPPort})
	if err != nil {
		return nil, fmt.Errorf("listen udp error: %w", err)
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

	node := Node{
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
	}
	go node.runPacketEventLoop()
	go node.runPeerEventLoop()
	go node.runSTUNEventLoop()
	go node.runWebSocketEventLoop()
	go node.runPeersHealthcheckLoop()
	return &node, nil
}
