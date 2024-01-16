package p2p

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/rkonfj/peerguard/peer"
)

type peermapServerConn struct {
	peermapServers []string
	networkID      peer.NetworkID
	peerID         peer.PeerID
	inbound        chan<- []byte
	preNT          chan []byte
	doNT           chan []byte
	closedSig      chan int

	wsConn *websocket.Conn
	nonce  byte
}

func (c *peermapServerConn) dialPeermapServer() error {
	for _, server := range c.peermapServers {
		handshake := http.Header{}
		handshake.Set("X-Network", string(c.networkID))
		handshake.Set("X-PeerID", string(c.peerID))
		handshake.Set("X-Nonce", peer.NewNonce())
		conn, httpResp, err := websocket.DefaultDialer.Dial(server, handshake)
		if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("address[%s] is already in used", c.peerID)
		}
		if err != nil {
			continue
		}
		c.wsConn = conn
		c.nonce = peer.MustParseNonce(httpResp.Header.Get("X-Nonce"))
		return nil
	}
	return errors.New("no peermap server available")
}

func (c *peermapServerConn) runWebSocketEventLoop() {
	for {
		select {
		case <-c.closedSig:
			return
		default:
		}
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
			c.preNT <- b
		case peer.CONTROL_NAT_TRAVERSAL:
			c.doNT <- b
		}
	}
}

func (c *peermapServerConn) writeToRelay(p []byte, peerID peer.PeerID, op byte) error {
	b := make([]byte, 0, 2+len(peerID)+len(p))
	b = append(b, op)                // relay
	b = append(b, peerID.Len())      // addr length
	b = append(b, peerID.Bytes()...) // addr
	b = append(b, p...)              // data
	for i, v := range b {
		b[i] = v ^ c.nonce
	}
	if wsConn := c.wsConn; wsConn != nil {
		return wsConn.WriteMessage(websocket.BinaryMessage, b)
	}
	return ErrUseOfClosedConnection
}

func (c *peermapServerConn) preNTChannel() <-chan []byte {
	return c.preNT
}

func (c *peermapServerConn) doNTChannel() <-chan []byte {
	return c.doNT
}
