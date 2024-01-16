package p2p

import (
	"errors"
	"net"
	"time"

	"github.com/rkonfj/peerguard/peer"
)

var (
	ErrUseOfClosedConnection error = errors.New("use of closed network connection")
)

const (
	OP_PEER_DISCO       = 1
	OP_PEER_CONFIRM     = 2
	OP_PEER_HEALTHCHECK = 10
)

type STUNBindContext struct {
	PeerID peer.PeerID
	CTime  time.Time
}

type PeerContext struct {
	States     map[string]*PeerState
	CreateTime time.Time
}

func (peer *PeerContext) IPv4Ready() bool {
	for _, state := range peer.States {
		if state.Addr.IP.To4() != nil && time.Since(state.LastActiveTime) <= 20*time.Second {
			return true
		}
	}
	return false
}

func (peer *PeerContext) Ready() bool {
	for _, state := range peer.States {
		if time.Since(state.LastActiveTime) <= 20*time.Second {
			return true
		}
	}
	return false
}

func (peer *PeerContext) Select() *net.UDPAddr {
	addrs := make([]*net.UDPAddr, 0, len(peer.States))
	for _, state := range peer.States {
		if time.Since(state.LastActiveTime) <= 20*time.Second {
			addrs = append(addrs, state.Addr)
		}
	}
	return addrs[0]
}

type PeerState struct {
	Addr           *net.UDPAddr
	LastActiveTime time.Time
}

type PeerEvent struct {
	Op     int
	Addr   *net.UDPAddr
	PeerID peer.PeerID
}
