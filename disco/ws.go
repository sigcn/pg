package disco

import (
	"encoding/base64"
	"encoding/json"
	"errors"
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
)

type WSConn struct {
	*websocket.Conn
	secretStore   peer.SecretStore
	dialPeermap   func() (*websocket.Conn, byte, []string, error)
	closedSig     chan int
	datagrams     chan *Datagram
	peers         chan *PeerFindEvent
	peersUDPAddrs chan *PeerUDPAddrEvent
	nonce         byte
	stuns         []string
	writeMutex    sync.Mutex
}

func DialPeermapServer(
	peermapServers peer.PeermapCluster,
	secretStore peer.SecretStore,
	peerID peer.PeerID, metadata peer.Metadata) (*WSConn, error) {
	dialPeermap := func() (*websocket.Conn, byte, []string, error) {
		networkSecret, err := secretStore.NetworkSecret()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("get network secret failed: %w", err)
		}
		for _, server := range peermapServers {
			handshake := http.Header{}
			handshake.Set("X-Network", networkSecret.Secret)
			handshake.Set("X-PeerID", peerID.String())
			handshake.Set("X-Nonce", peer.NewNonce())
			handshake.Set("X-Metadata", base64.StdEncoding.EncodeToString(metadata.MustMarshalJSON()))

			peermap, err := url.Parse(server)
			if err != nil {
				slog.Error("invalid server format", "server", server, "err", err)
				continue
			}
			if peermap.Scheme == "http" {
				peermap.Scheme = "ws"
			} else if peermap.Scheme == "https" {
				peermap.Scheme = "wss"
			}
			t1 := time.Now()
			conn, httpResp, err := websocket.DefaultDialer.Dial(peermap.String(), handshake)
			if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
				return nil, 0, nil, fmt.Errorf("address: %s is already in used", peerID)
			}
			if httpResp != nil && httpResp.StatusCode == http.StatusForbidden {
				return nil, 0, nil, fmt.Errorf("join network denied: %s", networkSecret.Network)
			}
			if err != nil {
				slog.Error("dial server error", "server", server, "err", err)
				continue
			}
			slog.Info("PeermapConnected", "server", server, "latency", time.Since(t1))
			xSTUNs, err := base64.StdEncoding.DecodeString(httpResp.Header.Get("X-STUNs"))
			if err != nil {
				return nil, 0, nil, fmt.Errorf("decode stun error: %w", err)
			}
			var stuns []string
			json.Unmarshal(xSTUNs, &stuns)
			return conn, peer.MustParseNonce(httpResp.Header.Get("X-Nonce")), stuns, nil
		}
		return nil, 0, nil, errors.New("no peermap server available")
	}
	conn, nonce, stuns, err := dialPeermap()
	if err != nil {
		return nil, err
	}

	wsConn := WSConn{
		Conn:          conn,
		secretStore:   secretStore,
		dialPeermap:   dialPeermap,
		closedSig:     make(chan int),
		datagrams:     make(chan *Datagram, 50),
		peers:         make(chan *PeerFindEvent, 20),
		peersUDPAddrs: make(chan *PeerUDPAddrEvent, 20),
		nonce:         nonce,
		stuns:         stuns,
	}
	go wsConn.runWebSocketEventLoop()
	go wsConn.keepalive()
	return &wsConn, nil
}

func (c *WSConn) runWebSocketEventLoop() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
		mt, b, err := c.Conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) &&
				!websocket.IsUnexpectedCloseError(err, websocket.CloseAbnormalClosure) &&
				!strings.Contains(err.Error(), ErrUseOfClosedConnection.Error()) {
				slog.Error("Read websocket message error", "err", err.Error())
			}
			c.Conn.Close()
			for {
				select {
				case <-c.closedSig:
					return
				default:
				}
				time.Sleep(5 * time.Second)
				conn, nonce, stuns, err := c.dialPeermap()
				if err != nil {
					slog.Error("PeermapConnectFailed", "err", err)
					continue
				}
				c.Conn = conn
				c.nonce = nonce
				c.stuns = stuns
				break
			}
			continue
		}
		switch mt {
		case websocket.PingMessage:
			c.Conn.WriteMessage(websocket.PongMessage, nil)
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
			c.datagrams <- &Datagram{
				PeerID: peer.PeerID(b[2 : b[1]+2]),
				Data:   b[b[1]+2:],
			}
		case peer.CONTROL_NEW_PEER:
			event := PeerFindEvent{
				PeerID: peer.PeerID(b[2 : b[1]+2]),
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
				PeerID: peer.PeerID(b[2 : b[1]+2]),
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
}

func (c *WSConn) write(messageType int, data []byte) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	if wsConn := c.Conn; wsConn != nil {
		return wsConn.WriteMessage(messageType, data)
	}
	return ErrUseOfClosedConnection
}

func (c *WSConn) keepalive() {
	for {
		time.Sleep(20 * time.Second)
		if err := c.write(websocket.PingMessage, nil); err != nil {
			break
		}
	}
	c.Close()
}

func (c *WSConn) Close() error {
	close(c.closedSig)
	_ = c.Conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
	return c.Conn.Close()
}

func (c *WSConn) WriteTo(p []byte, peerID peer.PeerID, op byte) error {
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

func (c *WSConn) LeadDisco(peerID peer.PeerID) error {
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

func (c *WSConn) updateNetworkSecret(secret peer.NetworkSecret) {
	for i := 0; i < 5; i++ {
		if err := c.secretStore.UpdateNetworkSecret(secret); err != nil {
			slog.Error("NetworkSecretUpdate", "err", err)
			time.Sleep(time.Second)
			continue
		}
		return
	}
	slog.Error("NetworkSecretUpdate give up", "secret", secret)
}
