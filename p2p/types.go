package p2p

import (
	"net"
	"time"

	"github.com/rkonfj/peerguard/peer"
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
	Addr           *net.UDPAddr
	LastActiveTime time.Time
	CreateTime     time.Time
}

type PeerEvent struct {
	Op     int
	Addr   *net.UDPAddr
	PeerID peer.PeerID
}
