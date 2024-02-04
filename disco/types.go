package disco

import (
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"time"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/secure"
)

var (
	ErrUseOfClosedConnection error = errors.New("use of closed network connection")
)

const (
	OP_PEER_DISCO       = 1
	OP_PEER_CONFIRM     = 2
	OP_PEER_DELETE      = 3
	OP_PEER_HEALTHCHECK = 10
)

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
	return addrs[rand.Intn(len(addrs))]
}

type PeerState struct {
	Addr           *net.UDPAddr
	LastActiveTime time.Time
}

type PeerOP struct {
	Op     int
	Addr   *net.UDPAddr
	PeerID peer.PeerID
}

type STUNSession struct {
	PeerID peer.PeerID
	CTime  time.Time
}

type Datagram struct {
	PeerID peer.PeerID
	Data   []byte
}

func (d *Datagram) TryDecrypt(aesCBC *secure.AESCBC) []byte {
	b, err := aesCBC.Decrypt(d.Data, d.PeerID)
	if err != nil {
		slog.Debug("Datagram decrypt error", "err", err)
		return d.Data
	}
	return b
}

func (d *Datagram) TryEncrypt(aesCBC *secure.AESCBC) []byte {
	b, err := aesCBC.Encrypt(d.Data, d.PeerID)
	if err != nil {
		slog.Debug("Datagram encrypt error", "err", err)
		return d.Data
	}
	return b
}

type PeerFindEvent struct {
	PeerID   peer.PeerID
	Metadata peer.Metadata
}

type PeerUDPAddrEvent struct {
	PeerID peer.PeerID
	Addr   *net.UDPAddr
}
