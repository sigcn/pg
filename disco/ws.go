package disco

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peer/peermap"
)

type WSConn struct {
	*websocket.Conn
	peermap       *peermap.Peermap
	peerID        peer.ID
	metadata      peer.Metadata
	closedSig     chan int
	datagrams     chan *Datagram
	peers         chan *PeerFindEvent
	peersUDPAddrs chan *PeerUDPAddrEvent
	nonce         byte
	stuns         []string
	activeTime    time.Time
	writeMutex    sync.Mutex
}

func DialPeermap(peermap *peermap.Peermap, peerID peer.ID, metadata peer.Metadata) (*WSConn, error) {
	wsConn := &WSConn{
		peermap:       peermap,
		peerID:        peerID,
		metadata:      metadata,
		closedSig:     make(chan int),
		datagrams:     make(chan *Datagram, 50),
		peers:         make(chan *PeerFindEvent, 20),
		peersUDPAddrs: make(chan *PeerUDPAddrEvent, 20),
	}
	if err := wsConn.dial(""); err != nil {
		return nil, err
	}
	wsConn.activeTime = time.Now()
	go wsConn.runKeepaliveLoop()
	go wsConn.runEventsReadLoop()
	return wsConn, nil
}

func (c *WSConn) Close() error {
	close(c.closedSig)
	if conn := c.Conn; conn != nil {
		_ = c.Conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = c.Conn.Close()
	}
	return nil
}

func (c *WSConn) CloseConn() error {
	if conn := c.Conn; conn != nil {
		_ = c.Conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNoStatusReceived, ""), time.Now().Add(time.Second))
		_ = c.Conn.Close()
	}
	return nil
}

func (c *WSConn) WriteTo(p []byte, peerID peer.ID, op byte) error {
	b := make([]byte, 0, 2+len(peerID)+len(p))
	b = append(b, op)                // relay
	b = append(b, peerID.Len())      // addr length
	b = append(b, peerID.Bytes()...) // addr
	b = append(b, p...)              // data
	for i, v := range b {
		b[i] = v ^ c.nonce
	}
	return c.write(websocket.BinaryMessage, b)
}

func (c *WSConn) LeadDisco(peerID peer.ID) error {
	slog.Log(context.Background(), -3, "LeadDisco", "peer", peerID)
	return c.WriteTo(nil, peerID, peer.CONTROL_LEAD_DISCO)
}

func (c *WSConn) Datagrams() <-chan *Datagram {
	return c.datagrams
}

func (c *WSConn) Peers() <-chan *PeerFindEvent {
	return c.peers
}

func (c *WSConn) PeersUDPAddrs() <-chan *PeerUDPAddrEvent {
	return c.peersUDPAddrs
}

func (c *WSConn) STUNs() []string {
	return c.stuns
}

func (c *WSConn) dial(server string) error {
	networkSecret, err := c.peermap.SecretStore().NetworkSecret()
	if err != nil {
		return fmt.Errorf("get network secret failed: %w", err)
	}
	handshake := http.Header{}
	handshake.Set("X-Network", networkSecret.Secret)
	handshake.Set("X-PeerID", c.peerID.String())
	handshake.Set("X-Nonce", peer.NewNonce())
	handshake.Set("X-Metadata", base64.StdEncoding.EncodeToString(c.metadata.MustMarshalJSON()))
	if server == "" {
		server = c.peermap.String()
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
	conn, httpResp, err := websocket.DefaultDialer.Dial(peermap.String(), handshake)
	if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("address: %s is already in used", c.peerID)
	}
	if httpResp != nil && httpResp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("join network denied: %s", networkSecret.Network)
	}
	if httpResp != nil && httpResp.StatusCode == http.StatusTemporaryRedirect {
		slog.Info("RedirectPeermap", "location", httpResp.Header.Get("Location"))
		return c.dial(httpResp.Header.Get("Location"))
	}
	if err != nil {
		return fmt.Errorf("dial server %s: %w", server, err)
	}
	slog.Info("PeermapConnected", "server", server, "latency", time.Since(t1))
	xSTUNs, err := base64.StdEncoding.DecodeString(httpResp.Header.Get("X-STUNs"))
	if err != nil {
		return fmt.Errorf("decode stun error: %w", err)
	}
	var stuns []string
	err = json.Unmarshal(xSTUNs, &stuns)
	if err != nil {
		return err
	}
	c.Conn = conn
	c.stuns = stuns
	c.nonce = peer.MustParseNonce(httpResp.Header.Get("X-Nonce"))
	return nil
}

func (c *WSConn) runKeepaliveLoop() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		time.Sleep(12 * time.Second)
		if time.Since(c.activeTime) > 25*time.Second {
			c.CloseConn()
			continue
		}
		c.write(websocket.TextMessage, nil)
	}
}

func (c *WSConn) runEventsReadLoop() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		conn := c.Conn
		if conn == nil {
			continue
		}
		mt, b, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) &&
				!websocket.IsUnexpectedCloseError(err, websocket.CloseAbnormalClosure) &&
				!strings.Contains(err.Error(), ErrUseOfClosedConnection.Error()) {
				slog.Error("Read websocket message error", "err", err.Error())
			}
			conn.Close()
			for {
				select {
				case <-c.closedSig:
					return
				default:
				}
				if err := c.dial(""); err != nil {
					slog.Error("PeermapConnectFailed", "err", err)
					time.Sleep(5 * time.Second)
					continue
				}
				break
			}
			continue
		}
		c.activeTime = time.Now()
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
	switch b[0] {
	case peer.CONTROL_RELAY:
		c.datagrams <- &Datagram{
			PeerID: peer.ID(b[2 : b[1]+2]),
			Data:   b[b[1]+2:],
		}
	case peer.CONTROL_NEW_PEER:
		event := PeerFindEvent{
			PeerID: peer.ID(b[2 : b[1]+2]),
		}
		json.Unmarshal(b[b[1]+2:], &event.Metadata)
		c.peers <- &event
	case peer.CONTROL_NEW_PEER_UDP_ADDR:
		addr, err := net.ResolveUDPAddr("udp", string(b[b[1]+2:]))
		if err != nil {
			slog.Error("Resolve udp addr error", "err", err)
			break
		}
		c.peersUDPAddrs <- &PeerUDPAddrEvent{
			PeerID: peer.ID(b[2 : b[1]+2]),
			Addr:   addr,
		}
	case peer.CONTROL_UPDATE_NETWORK_SECRET:
		var secret peer.NetworkSecret
		if err := json.Unmarshal(b[1:], &secret); err != nil {
			slog.Error("NetworkSecretUpdate", "err", err)
			break
		}
		go c.updateNetworkSecret(secret)
	}
}

func (c *WSConn) write(messageType int, data []byte) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	if wsConn := c.Conn; wsConn != nil {
		return wsConn.WriteMessage(messageType, data)
	}
	return ErrUseOfClosedConnection
}

func (c *WSConn) updateNetworkSecret(secret peer.NetworkSecret) {
	for i := 0; i < 5; i++ {
		if err := c.peermap.SecretStore().UpdateNetworkSecret(secret); err != nil {
			slog.Error("NetworkSecretUpdate", "err", err)
			time.Sleep(time.Second)
			continue
		}
		return
	}
	slog.Error("NetworkSecretUpdate give up", "secret", secret)
}
