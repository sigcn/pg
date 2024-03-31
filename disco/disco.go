package disco

import (
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/secure"
)

var (
	ErrUseOfClosedConnection error = errors.New("use of closed network connection")
)

var defaultDiscoConfig = DiscoConfig{
	PortScanCount:             2000,
	ChallengesRetry:           3,
	ChallengesInitialInterval: 200 * time.Millisecond,
	ChallengesBackoffRate:     1.75,
}

type DiscoConfig struct {
	PortScanCount             int
	ChallengesRetry           int
	ChallengesInitialInterval time.Duration
	ChallengesBackoffRate     float64
}

func SetModifyDiscoConfig(modify func(cfg *DiscoConfig)) {
	if modify != nil {
		modify(&defaultDiscoConfig)
	}
	defaultDiscoConfig.PortScanCount = min(max(32, defaultDiscoConfig.PortScanCount), 65535-1024)
	defaultDiscoConfig.ChallengesRetry = max(2, defaultDiscoConfig.ChallengesRetry)
	defaultDiscoConfig.ChallengesInitialInterval = max(10*time.Millisecond, defaultDiscoConfig.ChallengesInitialInterval)
	defaultDiscoConfig.ChallengesBackoffRate = max(1, defaultDiscoConfig.ChallengesBackoffRate)
}

type Disco struct {
	Magic func() []byte
}

func (d *Disco) NewPing(peerID peer.ID) []byte {
	return slices.Concat(d.magic(), peerID.Bytes())
}

func (d *Disco) ParsePing(b []byte) peer.ID {
	magic := d.magic()
	if len(b) <= len(magic) || len(b) > 255+len(magic) {
		return ""
	}
	if slices.Equal(magic, b[:len(magic)]) {
		return peer.ID(b[len(magic):])
	}
	return ""
}

func (d *Disco) magic() []byte {
	var magic []byte
	if d.Magic != nil {
		magic = d.Magic()
	}
	if len(magic) == 0 {
		return []byte("_ping")
	}
	return magic
}

type PeerStore interface {
	FindPeer(peer.ID) (*PeerContext, bool)
}

type PeerContext struct {
	PeerID     peer.ID
	States     map[string]*PeerState
	CreateTime time.Time

	exitSig           chan struct{}
	ping              func(peerID peer.ID, addr *net.UDPAddr)
	keepaliveInterval time.Duration

	statesMutex sync.RWMutex
}

func (peer *PeerContext) Heartbeat(addr *net.UDPAddr) {
	peer.statesMutex.Lock()
	defer peer.statesMutex.Unlock()
	slog.Debug("[UDP] Heartbeat", "peer", peer.PeerID, "addr", addr)
	for _, state := range peer.States {
		if state.Addr.IP.Equal(addr.IP) && state.Addr.Port == addr.Port {
			state.LastActiveTime = time.Now()
			return
		}
	}
	slog.Info("[UDP] AddPeer", "peer", peer.PeerID, "addr", addr)
	peer.States[addr.String()] = &PeerState{Addr: addr, LastActiveTime: time.Now(), PeerID: peer.PeerID}
	peer.ping(peer.PeerID, addr)
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
		if time.Since(state.LastActiveTime) < peer.keepaliveInterval+2*time.Second {
			addrs = append(addrs, state.Addr)
		}
	}
	if len(addrs) == 0 {
		return nil
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
			peer.ping(peer.PeerID, addr)
		}
	}
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
	PeerID         peer.ID
	Addr           *net.UDPAddr
	LastActiveTime time.Time
}

type STUNSession struct {
	PeerID peer.ID
	CTime  time.Time
}

type Datagram struct {
	PeerID peer.ID
	Data   []byte
}

func (d *Datagram) TryDecrypt(symmAlgo secure.SymmAlgo) []byte {
	b, err := symmAlgo.Decrypt(d.Data, d.PeerID.String())
	if err != nil {
		slog.Debug("Datagram decrypt error", "err", err)
		return d.Data
	}
	return b
}

func (d *Datagram) TryEncrypt(symmAlgo secure.SymmAlgo) []byte {
	b, err := symmAlgo.Encrypt(d.Data, d.PeerID.String())
	if err != nil {
		slog.Debug("Datagram encrypt error", "err", err)
		return d.Data
	}
	return b
}

type PeerFindEvent struct {
	PeerID   peer.ID
	Metadata peer.Metadata
}

type PeerUDPAddrEvent struct {
	PeerID peer.ID
	Addr   *net.UDPAddr
}
