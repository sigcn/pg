package udp

import (
	"context"
	"log/slog"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sigcn/pg/cache"
	"github.com/sigcn/pg/disco"
)

type peerkeeper struct {
	udpConn    atomic.Pointer[net.UDPConn]
	peerID     disco.PeerID
	states     map[string]*PeerState // key is udp addr
	createTime time.Time

	exitSig           chan struct{}
	ping              func(udpConn *net.UDPConn, peerID disco.PeerID, addr *net.UDPAddr)
	keepaliveInterval time.Duration

	statesMutex sync.RWMutex

	// caches
	cacheReady cache.CacheValue[bool]
}

func (peer *peerkeeper) heartbeat(addr *net.UDPAddr) {
	if peer == nil || !peer.statesMutex.TryLock() {
		return
	}
	defer peer.statesMutex.Unlock()
	slog.Log(context.Background(), -5, "[UDP] Heartbeat", "peer", peer.peerID, "addr", addr)
	for _, state := range peer.states {
		if state.Addr.IP.Equal(addr.IP) && state.Addr.Port == addr.Port {
			state.LastActiveTime = time.Now()
			return
		}
	}
	slog.Info("[UDP] AddPeer", "peer", peer.peerID, "addr", addr)
	peer.states[addr.String()] = &PeerState{Addr: addr, LastActiveTime: time.Now(), PeerID: peer.peerID}
	peer.ping(peer.udpConn.Load(), peer.peerID, addr)
}

func (peer *peerkeeper) healthcheck() {
	if time.Since(peer.createTime) > 3*peer.keepaliveInterval {
		for addr, state := range peer.states {
			if time.Since(state.LastActiveTime) > 2*peer.keepaliveInterval+time.Second {
				slog.Info("[UDP] RemovePeer", "peer", peer.peerID, "addr", state.Addr)
				peer.statesMutex.Lock()
				delete(peer.states, addr)
				peer.statesMutex.Unlock()
			}
		}
	}
}

// ready when has at least one active udp address
func (peer *peerkeeper) ready() bool {
	now := time.Now()
	doRealCheck := func() bool {
		peer.statesMutex.RLock()
		defer peer.statesMutex.RUnlock()
		for _, state := range peer.states {
			if now.Sub(state.LastActiveTime) <= peer.keepaliveInterval+2*time.Second {
				return true
			}
		}
		return false
	}

	// perform real-time checks within the first 20 seconds after creation,
	if now.Sub(peer.createTime) < 20*time.Second {
		return doRealCheck()
	}

	// then cache for one heartbeat interval thereafter
	return peer.cacheReady.LoadTTL(peer.keepaliveInterval, doRealCheck)
}

// selectPeerUDP select one of the multiple UDP addresses discovered by the peer
func (peer *peerkeeper) selectPeerUDP() *PeerState {
	candidates := make([]PeerState, 0, len(peer.states))
	peer.statesMutex.RLock()
	for _, state := range peer.states {
		if time.Since(state.LastActiveTime) < 2*(peer.keepaliveInterval+time.Second) {
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
	return &candidates[0]
}

func (peer *peerkeeper) writeUDP(p []byte) (int, error) {
	if peerState := peer.selectPeerUDP(); p != nil {
		slog.Log(context.Background(), -3, "[UDP] WriteTo", "peer", peer.peerID, "addr", peerState.Addr)
		if time.Since(peerState.LastActiveTime) > peer.keepaliveInterval+time.Second {
			peer.udpConn.Load().WriteTo(p, peerState.Addr)
			return 0, ErrUDPConnInactive
		}
		return peer.udpConn.Load().WriteTo(p, peerState.Addr)
	}
	return 0, net.ErrClosed
}

func (peer *peerkeeper) run() {
	ticker := time.NewTicker(peer.keepaliveInterval)
	ping := func() {
		addrs := make([]*net.UDPAddr, 0, len(peer.states))
		peer.statesMutex.RLock()
		for _, v := range peer.states {
			addrs = append(addrs, v.Addr)
		}
		peer.statesMutex.RUnlock()
		for _, addr := range addrs {
			peer.ping(peer.udpConn.Load(), peer.peerID, addr)
		}
	}
	for {
		select {
		case <-peer.exitSig:
			ticker.Stop()
			slog.Debug("[UDP] KeepaliveExit", "peer", peer.peerID)
			return
		case <-ticker.C:
			ping()
		}
	}
}

func (peer *peerkeeper) close() error {
	close(peer.exitSig)
	return nil
}
