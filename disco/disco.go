package disco

import (
	"errors"
	"log/slog"
	"net"
	"net/url"
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
	PortScanOffset:            -1000,
	PortScanCount:             3000,
	ChallengesRetry:           5,
	ChallengesInitialInterval: 200 * time.Millisecond,
	ChallengesBackoffRate:     1.65,
}

type DiscoConfig struct {
	PortScanOffset            int
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
	defaultDiscoConfig.ChallengesRetry = max(1, defaultDiscoConfig.ChallengesRetry)
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
	States     map[string]*PeerState // key is udp addr
	CreateTime time.Time

	exitSig           chan struct{}
	ping              func(peerID peer.ID, addr *net.UDPAddr)
	keepaliveInterval time.Duration

	statesMutex sync.RWMutex
}

func (peer *PeerContext) Heartbeat(addr *net.UDPAddr) {
	if peer == nil || !peer.statesMutex.TryLock() {
		return
	}
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
			if time.Since(state.LastActiveTime) > 2*peer.keepaliveInterval+time.Second {
				slog.Info("[UDP] RemovePeer", "peer", peer.PeerID, "addr", state.Addr)
				peer.statesMutex.Lock()
				delete(peer.States, addr)
				peer.statesMutex.Unlock()
			}
		}
	}
}

// Ready when peer context has at least one active udp address
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
	candidates := make([]PeerState, 0, len(peer.States))
	peer.statesMutex.RLock()
	for _, state := range peer.States {
		if time.Since(state.LastActiveTime) < peer.keepaliveInterval+2*time.Second {
			candidates = append(candidates, *state)
		}
	}
	peer.statesMutex.RUnlock()
	if len(candidates) == 0 {
		return nil
	}
	slices.SortFunc(candidates, func(c1, c2 PeerState) int {
		if c1.LastActiveTime.After(c2.LastActiveTime) {
			return -1
		}
		return 1
	})
	return candidates[0].Addr
}

func (peer *PeerContext) RunKeepaliveLoop() {
	ticker := time.NewTicker(peer.keepaliveInterval)
	ping := func() {
		addrs := make([]*net.UDPAddr, 0, len(peer.States))
		peer.statesMutex.RLock()
		for _, v := range peer.States {
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
			slog.Debug("[UDP] KeepaliveExit", "peer", peer.PeerID)
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

type stunSession struct {
	peerID peer.ID
	cTime  time.Time
}

type stunSessionManager struct {
	sync.RWMutex
	sessions map[string]stunSession
}

func (m *stunSessionManager) Get(txid string) (stunSession, bool) {
	m.RLock()
	defer m.RUnlock()
	s, ok := m.sessions[txid]
	return s, ok
}

func (m *stunSessionManager) Set(txid string, peerID peer.ID) {
	m.Lock()
	defer m.Unlock()
	m.sessions[txid] = stunSession{peerID: peerID, cTime: time.Now()}
}

func (m *stunSessionManager) Remove(txid string) {
	m.Lock()
	defer m.Unlock()
	delete(m.sessions, txid)
}

type Datagram struct {
	PeerID peer.ID
	Data   []byte
}

func (d *Datagram) TryDecrypt(symmAlgo secure.SymmAlgo) []byte {
	if symmAlgo == nil {
		return d.Data
	}
	b, err := symmAlgo.Decrypt(d.Data, d.PeerID.String())
	if err != nil {
		slog.Debug("Datagram decrypt error", "err", err)
		return d.Data
	}
	return b
}

func (d *Datagram) TryEncrypt(symmAlgo secure.SymmAlgo) []byte {
	if symmAlgo == nil {
		return d.Data
	}
	b, err := symmAlgo.Encrypt(d.Data, d.PeerID.String())
	if err != nil {
		slog.Debug("Datagram encrypt error", "err", err)
		return d.Data
	}
	return b
}

type PeerFindEvent struct {
	PeerID   peer.ID
	Metadata url.Values
}

type PeerUDPAddrEvent struct {
	PeerID peer.ID
	Addr   *net.UDPAddr
}
