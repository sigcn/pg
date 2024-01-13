package peer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peernet"
)

type Peer interface {
	ListenPacket(peerID peernet.PeerID) (net.PacketConn, error)
}

type VolatileUDPAddr struct {
	*net.UDPAddr
	lastValidTime time.Time
}

type Node struct {
	networkID peernet.NetworkID
	servers   []string
	peersMap  cmap.ConcurrentMap[string, *VolatileUDPAddr]
	closedSig chan int
}

func (n *Node) ListenPacket(peerID peernet.PeerID) (net.PacketConn, error) {
	var conn *PeerPacketConn
	for _, server := range n.servers {
		handshake := http.Header{}
		handshake.Set("X-Network", string(n.networkID))
		handshake.Set("X-PeerID", string(peerID))
		handshake.Set("X-Nonce", peernet.NewNonce())
		wsConn, httpResp, err := websocket.DefaultDialer.Dial(server, handshake)
		if httpResp != nil && httpResp.StatusCode == http.StatusBadRequest {
			return nil, errors.New("address is already in used")
		}
		if err != nil {
			continue
		}
		conn = &PeerPacketConn{
			node:        n,
			wsConn:      wsConn,
			peerID:      peerID,
			nonce:       peernet.MustParseNonce(httpResp.Header.Get("X-Nonce")),
			inbound:     make(chan []byte, 10),
			stunChan:    make(chan []byte),
			stunTxIDMap: cmap.New[stunBindContext](),
		}
		conn.ctx, conn.ctxCancel = context.WithCancel(context.Background())
		go conn.runReadLoop()
		break
	}
	if conn == nil {
		return nil, errors.New("no server available")
	}
	return conn, nil
}

func (n *Node) PeerUDPAddr(peerID peernet.PeerID) *net.UDPAddr {
	if udpAddr, ok := n.peersMap.Get(string(peerID)); ok {
		return udpAddr.UDPAddr
	}
	return nil
}

func (n *Node) PeerID(udpAddr *net.UDPAddr) peernet.PeerID {
	if udpAddr == nil {
		return ""
	}
	for item := range n.peersMap.IterBuffered() {
		if item.Val.UDPAddr.IP.Equal(udpAddr.IP) && item.Val.UDPAddr.Port == udpAddr.Port {
			return peernet.PeerID(item.Key)
		}
	}
	return ""
}

func (n *Node) UpdatePeer(peerID peernet.PeerID, udpAddr *net.UDPAddr) (updated bool) {
	updated = true
	if peer, ok := n.peersMap.Get(string(peerID)); ok {
		updated = time.Since(peer.lastValidTime) > 15*time.Second
	}
	n.peersMap.Set(string(peerID),
		&VolatileUDPAddr{UDPAddr: udpAddr, lastValidTime: time.Now()})
	return
}

func (n *Node) Close() error {
	close(n.closedSig)
	return nil
}

func (n *Node) udpPeersHealthcheck() {
	for {
		select {
		case <-n.closedSig:
			return
		default:
		}
		for item := range n.peersMap.IterBuffered() {
			if time.Since(item.Val.lastValidTime) > 15*time.Second {
				n.peersMap.Remove(item.Key)
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func New(networkID peernet.NetworkID, servers []string) (*Node, error) {
	node := Node{
		networkID: networkID,
		servers:   servers,
		peersMap:  cmap.New[*VolatileUDPAddr](),
		closedSig: make(chan int),
	}
	go node.udpPeersHealthcheck()
	return &node, nil
}
