package disco

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rkonfj/peerguard/peer"
	"golang.org/x/time/rate"
)

var (
	_ io.ReadWriter     = (*WSConn)(nil)
	_ ControllerManager = (*WSConn)(nil)
)

type WSConn struct {
	rawConn           atomic.Pointer[websocket.Conn]
	server            *peer.Peermap
	connectedServer   string
	peerID            peer.ID
	metadata          url.Values
	closedSig         chan int
	datagrams         chan *Datagram
	peers             chan *Peer
	peersUDPAddrs     chan *PeerUDPAddr
	nonce             byte
	stuns             []string
	activeTime        atomic.Int64
	writeMutex        sync.Mutex
	rateLimiter       *rate.Limiter
	streamRateLimiter *rate.Limiter
	controllersMutex  sync.RWMutex
	controllers       map[uint8][]Controller

	connData chan []byte
	connBuf  []byte
}

func (c *WSConn) Read(p []byte) (n int, err error) {
	if c.connBuf != nil {
		n = copy(p, c.connBuf)
		if n < len(c.connBuf) {
			c.connBuf = c.connBuf[n:]
		} else {
			c.connBuf = nil
		}
		return
	}

	wsb, ok := <-c.connData
	if !ok {
		return 0, io.EOF
	}
	n = copy(p, wsb)
	if n < len(wsb) {
		c.connBuf = wsb[n:]
	}
	return
}

func (c *WSConn) Write(p []byte) (n int, err error) {
	if c.streamRateLimiter != nil {
		c.streamRateLimiter.WaitN(context.Background(), len(p))
	}
	err = c.write(append(append([]byte(nil), CONTROL_CONN.Byte()), p...))
	if err != nil {
		return
	}
	return len(p), nil
}

func (c *WSConn) Close() error {
	close(c.closedSig)
	close(c.datagrams)
	close(c.peers)
	close(c.peersUDPAddrs)
	close(c.connData)
	if conn := c.rawConn.Load(); conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = conn.Close()
	}
	return nil
}

func (c *WSConn) RestartListener() error {
	if conn := c.rawConn.Load(); conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNoStatusReceived, ""), time.Now().Add(time.Second))
		_ = conn.Close()
	}
	return nil
}

func (c *WSConn) WriteTo(p []byte, peerID peer.ID, op ControlCode) error {
	if op == CONTROL_RELAY && c.rateLimiter != nil {
		c.rateLimiter.WaitN(context.Background(), len(p))
	}
	b := make([]byte, 0, 2+len(peerID)+len(p))
	b = append(b, op.Byte())         // relay
	b = append(b, peerID.Len())      // addr length
	b = append(b, peerID.Bytes()...) // addr
	b = append(b, p...)              // data
	return c.write(b)
}

func (c *WSConn) LeadDisco(peerID peer.ID) error {
	slog.Log(context.Background(), -3, "LeadDisco", "peer", peerID)
	return c.WriteTo(nil, peerID, CONTROL_LEAD_DISCO)
}

func (c *WSConn) Datagrams() <-chan *Datagram {
	return c.datagrams
}

func (c *WSConn) Peers() <-chan *Peer {
	return c.peers
}

func (c *WSConn) PeersUDPAddrs() <-chan *PeerUDPAddr {
	return c.peersUDPAddrs
}

func (c *WSConn) STUNs() []string {
	return c.stuns
}

func (c *WSConn) ServerURL() string {
	return c.connectedServer
}

func (c *WSConn) Register(ctr Controller) {
	c.controllersMutex.Lock()
	defer c.controllersMutex.Unlock()
	c.controllers[ctr.Type()] = append(c.controllers[ctr.Type()], ctr)
}

func (c *WSConn) Unregister(ctr Controller) {
	c.controllersMutex.Lock()
	defer c.controllersMutex.Unlock()
	var filterd []Controller
	for _, ct := range c.controllers[ctr.Type()] {
		if ct.Name() != ctr.Name() {
			filterd = append(filterd, ct)
		}
	}
	c.controllers[ctr.Type()] = filterd
}

func (c *WSConn) dial(ctx context.Context, server string) error {
	networkSecret, err := c.server.SecretStore().NetworkSecret()
	if err != nil {
		return fmt.Errorf("get network secret failed: %w", err)
	}
	handshake := http.Header{}
	handshake.Set("X-Network", networkSecret.Secret)
	handshake.Set("X-PeerID", c.peerID.String())
	handshake.Set("X-Nonce", peer.NewNonce())
	handshake.Set("X-Metadata", c.metadata.Encode())
	if server == "" {
		server = c.server.String()
	}
	peermap, err := url.Parse(server)
	if err != nil {
		return fmt.Errorf("invalid server(%s) format: %w", server, err)
	}
	if peermap.Scheme == "http" {
		peermap.Scheme = "ws"
	} else if peermap.Scheme == "https" {
		peermap.Scheme = "wss"
	}
	t1 := time.Now()
	conn, httpResp, err := websocket.DefaultDialer.DialContext(ctx, peermap.String(), handshake)
	if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("address: %s is already in used", c.peerID)
	}
	if httpResp != nil && httpResp.StatusCode == http.StatusForbidden {
		var err Error
		json.NewDecoder(httpResp.Body).Decode(&err)
		defer httpResp.Body.Close()
		return err
	}
	if httpResp != nil && httpResp.StatusCode == http.StatusTemporaryRedirect {
		slog.Info("RedirectPeermap", "location", httpResp.Header.Get("Location"))
		return c.dial(ctx, httpResp.Header.Get("Location"))
	}
	if err != nil {
		return fmt.Errorf("dial server %s: %w", server, err)
	}
	slog.Info("PeermapConnected", "server", server, "latency", time.Since(t1))

	if err := c.configureSTUNs(httpResp.Header); err != nil {
		return err
	}

	if err := c.configureRatelimiter(httpResp.Header); err != nil {
		return err
	}

	c.rawConn.Store(conn)
	c.nonce = peer.MustParseNonce(httpResp.Header.Get("X-Nonce"))
	c.connectedServer = server
	c.activeTime.Store(time.Now().Unix())
	conn.SetPingHandler(func(appData string) error {
		slog.Debug("WebsocketRecvPing")
		c.activeTime.Store(time.Now().Unix())
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		if err == websocket.ErrCloseSent {
			return nil
		} else if _, ok := err.(net.Error); ok {
			return nil
		}
		return err
	})
	return nil
}

func (c *WSConn) configureSTUNs(respHeader http.Header) error {
	stunsArg := respHeader.Get("X-STUNs")
	if stunsArg == "" {
		slog.Warn("NAT traversal is disabled")
		return nil
	}
	xSTUNs, err := base64.StdEncoding.DecodeString(stunsArg)
	if err != nil {
		return fmt.Errorf("invalid pgmap server: decode stuns: %w", err)
	}
	err = json.Unmarshal(xSTUNs, &c.stuns)
	if err != nil {
		return fmt.Errorf("invalid pgmap server: unmarshal json: %w", err)
	}
	return nil
}

func (c *WSConn) configureRatelimiter(respHeader http.Header) error {
	limitArg := respHeader.Get("X-Limiter-Limit")
	if limitArg == "" {
		return nil
	}
	limit, err := strconv.ParseInt(limitArg, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid pgmap server: parse ratelimiter limit: %w", err)
	}
	burstArg := respHeader.Get("X-Limiter-Burst")
	if burstArg == "" {
		burstArg = limitArg
	}
	burst, err := strconv.ParseInt(burstArg, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid pgmap server: parse ratelimiter burst: %w", err)
	}
	slog.Log(context.Background(), -2, "RealyRatelimiter", "limit", limit, "burst", burst)
	if limit > 0 {
		c.rateLimiter = rate.NewLimiter(rate.Limit(limit), int(burst))
	}
	streamLimitArg := respHeader.Get("X-Limiter-Stream-Limit")
	if limitArg == "" {
		return nil
	}
	streamLimit, err := strconv.ParseInt(streamLimitArg, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid pgmap server: parse stream.ratelimiter limit: %w", err)
	}
	streamBurstArg := respHeader.Get("X-Limiter-Stream-Burst")
	if streamBurstArg == "" {
		streamBurstArg = streamLimitArg
	}
	streamBurst, err := strconv.ParseInt(streamBurstArg, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid pgmap server: parse stream.ratelimiter burst: %w", err)
	}
	slog.Log(context.Background(), -2, "StreamRatelimiter", "limit", streamLimit, "burst", streamBurst)
	if limit > 0 {
		c.streamRateLimiter = rate.NewLimiter(rate.Limit(streamLimit), int(streamBurst))
	}
	return nil
}

func (c *WSConn) runConnAliveDetector() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		time.Sleep(time.Second)
		sec := time.Now().Unix()
		slog.Log(context.Background(), -6, "CheckAlive", "sec", sec, "active", c.activeTime.Load())
		if sec-c.activeTime.Load() > 25 {
			c.RestartListener()
		}
	}
}

func (c *WSConn) runEventsReadLoop() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		conn := c.rawConn.Load()
		if conn == nil {
			continue
		}
		mt, b, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure) &&
				!websocket.IsUnexpectedCloseError(err,
					websocket.CloseGoingAway,
					websocket.CloseAbnormalClosure) &&
				!strings.Contains(err.Error(), ErrUseOfClosedConnection.Error()) {
				slog.Error("ReadLoopExited", "details", err.Error())
			}
			conn.Close()
			for {
				select {
				case <-c.closedSig:
					return
				default:
				}
				time.Sleep(2 * time.Second)
				if err := c.dial(context.Background(), ""); err != nil {
					slog.Error("PeermapConnectFailed", "err", err)
					time.Sleep(time.Second)
					continue
				}
				break
			}
			continue
		}
		c.activeTime.Store(time.Now().Unix())
		switch mt {
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ c.nonce
		}
		c.handleEvents(b)
	}
}

func (c *WSConn) handleEvents(b []byte) {
	switch ControlCode(b[0]) {
	case CONTROL_RELAY:
		c.datagrams <- &Datagram{PeerID: peer.ID(b[2 : b[1]+2]), Data: b[b[1]+2:]}
	case CONTROL_NEW_PEER:
		meta, _ := url.ParseQuery(string(b[b[1]+2:]))
		event := Peer{ID: peer.ID(b[2 : b[1]+2]), Metadata: meta}
		c.peers <- &event
	case CONTROL_NEW_PEER_UDP_ADDR:
		if b[b[1]+2] != 'a' { // old version without nat type
			slog.Error("IncompatiblePeerVersionFound(v0.7 is required)", "peer", peer.ID(b[2:b[1]+2]))
			addr, err := net.ResolveUDPAddr("udp", string(b[b[1]+2:]))
			if err != nil {
				slog.Error("Resolve udp addr error", "err", err)
				break
			}
			c.peersUDPAddrs <- &PeerUDPAddr{ID: peer.ID(b[2 : b[1]+2]), Addr: addr}
			return
		}
		addrLen := b[b[1]+3]
		s := b[1] + 4
		addr, err := net.ResolveUDPAddr("udp", string(b[s:s+addrLen]))
		if err != nil {
			slog.Error("Resolve udp addr error", "err", err)
			break
		}
		c.peersUDPAddrs <- &PeerUDPAddr{ID: peer.ID(b[2 : b[1]+2]), Addr: addr, Type: NATType(b[s+addrLen:])}
	case CONTROL_UPDATE_NETWORK_SECRET:
		var secret peer.NetworkSecret
		if err := json.Unmarshal(b[1:], &secret); err != nil {
			slog.Error("NetworkSecretUpdate", "err", err)
			break
		}
		go c.updateNetworkSecret(secret)
	case CONTROL_CONN:
		c.connData <- b[1:]
	default:
		c.controllersMutex.RLock()
		ctrs := c.controllers[b[0]]
		c.controllersMutex.RUnlock()
		for _, ctr := range ctrs {
			ctr.Handle(b)
		}
	}
}

func (c *WSConn) write(b []byte) error {
	for i, v := range b {
		b[i] = v ^ c.nonce
	}
	return c.writeWS(websocket.BinaryMessage, b)
}

func (c *WSConn) writeWS(messageType int, data []byte) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	if wsConn := c.rawConn.Load(); wsConn != nil {
		return wsConn.WriteMessage(messageType, data)
	}
	return ErrUseOfClosedConnection
}

func (c *WSConn) updateNetworkSecret(secret peer.NetworkSecret) {
	for i := 0; i < 5; i++ {
		if err := c.server.SecretStore().UpdateNetworkSecret(secret); err != nil {
			slog.Error("NetworkSecretUpdate", "err", err)
			time.Sleep(time.Second)
			continue
		}
		return
	}
	slog.Error("NetworkSecretUpdate give up", "secret", secret)
}

func DialPeermap(ctx context.Context, server *peer.Peermap, peerID peer.ID, metadata url.Values) (*WSConn, error) {
	wsConn := &WSConn{
		server:        server,
		peerID:        peerID,
		metadata:      metadata,
		closedSig:     make(chan int),
		datagrams:     make(chan *Datagram, 50),
		peers:         make(chan *Peer, 20),
		peersUDPAddrs: make(chan *PeerUDPAddr, 20),
		connData:      make(chan []byte, 128),
		controllers:   make(map[uint8][]Controller),
	}
	if err := wsConn.dial(ctx, ""); err != nil {
		return nil, err
	}
	go wsConn.runEventsReadLoop()
	go wsConn.runConnAliveDetector()
	return wsConn, nil
}
