package p2p

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
)

type Node struct {
	networkID             peer.NetworkID
	servers               []string
	closedSig             chan int
	peerKeepaliveInterval time.Duration
}

func (n *Node) ListenPacket(peerID peer.PeerID) (net.PacketConn, error) {
	var conn *PeerPacketConn
	for _, server := range n.servers {
		handshake := http.Header{}
		handshake.Set("X-Network", string(n.networkID))
		handshake.Set("X-PeerID", string(peerID))
		handshake.Set("X-Nonce", peer.NewNonce())
		wsConn, httpResp, err := websocket.DefaultDialer.Dial(server, handshake)
		if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
			return nil, errors.New("address is already in used")
		}
		if err != nil {
			continue
		}
		conn = &PeerPacketConn{
			peersMap:    make(map[peer.PeerID]*PeerContext),
			peerEvent:   make(chan PeerEvent),
			inbound:     make(chan []byte, 10),
			stunChan:    make(chan []byte),
			wsConn:      wsConn,
			node:        n,
			peerID:      peerID,
			nonce:       peer.MustParseNonce(httpResp.Header.Get("X-Nonce")),
			stunTxIDMap: cmap.New[STUNBindContext](),
		}
		conn.ctx, conn.ctxCancel = context.WithCancel(context.Background())
		go conn.keepState()
		break
	}
	if conn == nil {
		return nil, errors.New("no peermap server available")
	}
	return conn, nil
}

func (n *Node) Close() error {
	close(n.closedSig)
	return nil
}

func New(networkID peer.NetworkID, servers []string) (*Node, error) {
	node := Node{
		networkID:             networkID,
		servers:               servers,
		closedSig:             make(chan int),
		peerKeepaliveInterval: 16 * time.Second,
	}
	return &node, nil
}
