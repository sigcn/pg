package disco

import (
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
	networkSecret  peer.NetworkSecret
	peerID         peer.PeerID
	peermapServers []string
	closedSig      chan int
	datagrams      chan *Datagram
	peers          chan *PeerFindEvent
	peersUDPAddrs  chan *PeerUDPAddrEvent
	nonce          byte
	writeMutex     sync.Mutex
}

func DialPeermapServer(secret peer.NetworkSecret, peerID peer.PeerID, peermapServers peer.PeermapCluster) (*WSConn, error) {
	conn, nonce, err := dialPeermapServer(secret, peerID, peermapServers)
	if err != nil {
		return nil, err
	}

	wsConn := WSConn{
		Conn:           conn,
		networkSecret:  secret,
		peerID:         peerID,
		peermapServers: peermapServers,
		closedSig:      make(chan int),
		datagrams:      make(chan *Datagram, 50),
		peers:          make(chan *PeerFindEvent, 20),
		peersUDPAddrs:  make(chan *PeerUDPAddrEvent, 20),
		nonce:          nonce,
	}
	go wsConn.runWebSocketEventLoop()
	return &wsConn, nil
}

func dialPeermapServer(secret peer.NetworkSecret, peerID peer.PeerID, peermapServers []string) (
	*websocket.Conn, byte, error) {
	for _, server := range peermapServers {
		handshake := http.Header{}
		handshake.Set("X-Network", string(secret))
		handshake.Set("X-PeerID", peerID.String())
		handshake.Set("X-Nonce", peer.NewNonce())

		peermap, err := url.Parse(server)
		if err != nil {
			continue
		}
		if peermap.Scheme == "http" {
			peermap.Scheme = "ws"
		} else if peermap.Scheme == "https" {
			peermap.Scheme = "wss"
		}
		conn, httpResp, err := websocket.DefaultDialer.Dial(peermap.String(), handshake)
		if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
			return nil, 0, fmt.Errorf("address: %s is already in used", peerID)
		}
		if httpResp != nil && httpResp.StatusCode == http.StatusForbidden {
			return nil, 0, fmt.Errorf("join network denied: %s", secret)
		}
		if err != nil {
			continue
		}
		slog.Info("PeermapConnected", "server", server)
		return conn, peer.MustParseNonce(httpResp.Header.Get("X-Nonce")), nil
	}
	return nil, 0, errors.New("no peermap server available")
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
				conn, nonce, err := dialPeermapServer(c.networkSecret, c.peerID, c.peermapServers)
				if err != nil {
					slog.Error("PeermapConnectFailed", "err", err)
					continue
				}
				c.Conn = conn
				c.nonce = nonce
				break
			}
			return
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
			json.Unmarshal(b[b[1]+2:], &event.STUNs)
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
		}
	}
}

func (c *WSConn) Close() error {
	close(c.closedSig)
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
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	if wsConn := c.Conn; wsConn != nil {
		return wsConn.WriteMessage(websocket.BinaryMessage, b)
	}
	return ErrUseOfClosedConnection
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
