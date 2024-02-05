package disco

import (
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"sync"
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
	PeerID     peer.PeerID
	States     map[string]*PeerState
	CreateTime time.Time

	exitSig           chan struct{}
	ping              func(addr *net.UDPAddr)
	keepaliveInterval time.Duration

	statesMutex sync.RWMutex
}

func (peer *PeerContext) AddAddr(addr *net.UDPAddr) {
	peer.statesMutex.Lock()
	defer peer.statesMutex.Unlock()
	if _, ok := peer.States[addr.String()]; ok {
		return
	}
	peer.States[addr.String()] = &PeerState{Addr: addr}
}

func (peer *PeerContext) Heartbeat(addr *net.UDPAddr) {
	peer.statesMutex.Lock()
	defer peer.statesMutex.Unlock()
	for _, state := range peer.States {
		if state.Addr.IP.Equal(addr.IP) && state.Addr.Port == addr.Port {
			updated := time.Since(state.LastActiveTime) > 2*peer.keepaliveInterval+2*time.Second
			if updated {
				slog.Info("[UDP] AddPeer", "peer", peer.PeerID, "addr", addr)
			}
			state.LastActiveTime = time.Now()
			return
		}
	}
	slog.Info("[UDP][0RTT] AddPeer", "peer", peer.PeerID, "addr", addr)
	peer.States[addr.String()] = &PeerState{Addr: addr, LastActiveTime: time.Now()}
}

func (peer *PeerContext) Healthcheck() {
	if time.Since(peer.CreateTime) > 3*peer.keepaliveInterval {
		for addr, state := range peer.States {
			if time.Since(state.LastActiveTime) > 2*peer.keepaliveInterval {
				if state.LastActiveTime.IsZero() {
					slog.Debug("[UDP] RemovePeer", "peer", peer.PeerID, "addr", state.Addr)
				} else {
					slog.Info("[UDP] RemovePeer", "peer", peer.PeerID, "addr", state.Addr)
				}
				peer.statesMutex.Lock()
				delete(peer.States, addr)
				peer.statesMutex.Unlock()
			}
		}
	}
}

func (peer *PeerContext) IPv4Ready() bool {
	peer.statesMutex.RLock()
	defer peer.statesMutex.RUnlock()
	for _, state := range peer.States {
		if state.Addr.IP.To4() != nil && time.Since(state.LastActiveTime) <= peer.keepaliveInterval+2*time.Second {
			return true
		}
	}
	return false
}

func (peer *PeerContext) Ready() bool {
	peer.statesMutex.RLock()
	defer peer.statesMutex.RUnlock()
	for _, state := range peer.States {
		if time.Since(state.LastActiveTime) <= peer.keepaliveInterval+2*time.Second {
			return true
		}
	}
	return false
}

func (peer *PeerContext) Select() *net.UDPAddr {
	peer.statesMutex.RLock()
	defer peer.statesMutex.RUnlock()
	addrs := make([]*net.UDPAddr, 0, len(peer.States))
	for _, state := range peer.States {
		if time.Since(state.LastActiveTime) <= peer.keepaliveInterval+2*time.Second {
			addrs = append(addrs, state.Addr)
		}
	}
	return addrs[rand.Intn(len(addrs))]
}

func (peer *PeerContext) Keepalive() {
	ticker := time.NewTicker(peer.keepaliveInterval)
	ping := func() {
		addrs := make([]*net.UDPAddr, 0, len(peer.States))
		peer.statesMutex.RLock()
		for _, v := range peer.States {
			if time.Since(v.LastActiveTime) > 2*peer.keepaliveInterval+2*time.Second {
				continue
			}
			addrs = append(addrs, v.Addr)
		}
		peer.statesMutex.RUnlock()
		for _, addr := range addrs {
			peer.ping(addr)
		}
	}
	ping()
	for {
		select {
		case <-peer.exitSig:
			ticker.Stop()
			slog.Debug("[UDP] Ping exit", "peer", peer.PeerID)
			return
		case <-ticker.C:
			ping()
		}
	}
}

func (peer *PeerContext) Close() error {
	close(peer.exitSig)
	return nil
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
