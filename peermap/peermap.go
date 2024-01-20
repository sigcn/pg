package peermap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peermap/auth"
)

type Peer struct {
	peerMap   *PeerMap
	conn      *websocket.Conn
	networkID peer.NetworkID
	id        peer.PeerID
	nonce     byte
	wMut      sync.Mutex
}

func (p *Peer) Read() ([]byte, error) {
	mt, b, err := p.conn.ReadMessage()
	if err != nil {
		if websocket.IsCloseError(err,
			websocket.CloseGoingAway, websocket.CloseNormalClosure) {
			return nil, io.EOF
		}
		return nil, err
	}
	switch mt {
	case websocket.PingMessage:
		p.conn.WriteMessage(websocket.PongMessage, nil)
		return nil, nil
	case websocket.BinaryMessage:
	default:
		return nil, nil
	}
	for i, v := range b {
		b[i] = v ^ p.nonce
	}
	return b, nil
}

func (p *Peer) write(b []byte) error {
	p.wMut.Lock()
	defer p.wMut.Unlock()
	return p.conn.WriteMessage(websocket.BinaryMessage, b)
}

func (p *Peer) close() error {
	if peers, ok := p.peerMap.networkMap.Get(string(p.networkID)); ok {
		peers.Remove(string(p.id))
	}
	_ = p.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
	return p.conn.Close()
}

func (p *Peer) String() string {
	return string(p.id)
}

func (p *Peer) Start() {
	go p.readMessageLoope()
	go p.keepalive()

	stuns, _ := json.Marshal(p.peerMap.opts.STUNs)

	peers, _ := p.peerMap.networkMap.Get(string(p.networkID))
	for peer := range peers.IterBuffered() {
		if peer.Key == string(p.id) {
			continue
		}
		b := make([]byte, 2+len(p.id)+len(stuns))
		b[0] = 1
		b[1] = p.id.Len()
		copy(b[2:], p.id.Bytes())
		copy(b[len(p.id)+2:], stuns)
		for i, v := range b {
			b[i] = v ^ peer.Val.nonce
		}
		peer.Val.write(b)

		b1 := make([]byte, 2+len(peer.Val.id)+len(stuns))
		b1[0] = 1
		b1[1] = peer.Val.id.Len()
		copy(b1[2:], peer.Val.id.Bytes())
		copy(b1[len(peer.Val.id)+2:], stuns)
		for i, v := range b1 {
			b1[i] = v ^ p.nonce
		}
		p.write(b1)
	}
}

func (p *Peer) readMessageLoope() {
	for {
		mt, b, err := p.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error(err.Error())
			}
			p.close()
			return
		}
		switch mt {
		case websocket.PingMessage:
			p.conn.WriteMessage(websocket.PongMessage, nil)
			continue
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ p.nonce
		}
		tgtPeerID := peer.PeerID(b[2 : b[1]+2])
		if tgtPeer, err := p.peerMap.FindPeer(p.networkID, tgtPeerID); err == nil {
			data := b[b[1]+2:]
			bb := make([]byte, 2+len(p.id)+len(data))
			bb[0] = b[0]
			bb[1] = p.id.Len()
			copy(bb[2:p.id.Len()+2], p.id.Bytes())
			copy(bb[p.id.Len()+2:], data)
			for i, v := range bb {
				bb[i] = v ^ tgtPeer.nonce
			}
			_ = tgtPeer.write(bb)
		}
	}
}

func (p *Peer) keepalive() {
	for {
		time.Sleep(10 * time.Second)
		if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			break
		}
	}
}

type PeerMap struct {
	httpServer    *http.Server
	wsUpgrader    *websocket.Upgrader
	networkMap    cmap.ConcurrentMap[string, cmap.ConcurrentMap[string, *Peer]]
	opts          Options
	authenticator auth.Authenticator
}

func (pm *PeerMap) FindPeer(networkID peer.NetworkID, peerID peer.PeerID) (*Peer, error) {
	if peers, ok := pm.networkMap.Get(string(networkID)); ok {
		if peer, ok := peers.Get(string(peerID)); ok {
			return peer, nil
		}
	}
	return nil, errors.New("peer not found")
}

func (pm *PeerMap) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		fmt.Println("Graceful shutdown")
		pm.close()
	}()
	slog.Info("Serving for http now", "listen", pm.opts.Listen)
	return pm.httpServer.ListenAndServe()
}

func (pm *PeerMap) close() {
	pm.httpServer.Shutdown(context.Background())
}

type Options struct {
	Listen       string
	AdvertiseURL string
	ClusterKey   string
	STUNs        []string
}

func (opts *Options) applyDefaults() error {
	if opts.Listen == "" {
		opts.Listen = ":9987"
	}
	if opts.ClusterKey == "" {
		return errors.New("cluster key is required")
	}
	return nil
}

func New(opts Options) (*PeerMap, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	pm := PeerMap{
		httpServer:    &http.Server{Handler: mux, Addr: opts.Listen},
		wsUpgrader:    &websocket.Upgrader{},
		networkMap:    cmap.New[cmap.ConcurrentMap[string, *Peer]](),
		authenticator: auth.NewAuthenticator(opts.ClusterKey),
		opts:          opts,
	}
	mux.HandleFunc("/", pm.handleWebsocket)
	mux.HandleFunc("/peermap", pm.handleQueryPeermap)
	return &pm, nil
}

func (pm *PeerMap) handleQueryPeermap(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(pm.networkMap)
}

func (pm *PeerMap) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	networkID := r.Header.Get("X-Network")
	clusterKey := r.Header.Get("X-ClusterKey")

	if len(networkID) > 0 {
		pm.handlePeerPacketConnect(w, r)
		return
	}

	if len(clusterKey) > 0 {
		pm.handleClusterConnect(w, r)
		return
	}
	w.WriteHeader(http.StatusForbidden)
}

func (pm *PeerMap) handlePeerPacketConnect(w http.ResponseWriter, r *http.Request) {
	networkID, err := pm.authenticator.VerifyToken(r.Header.Get("X-Network"))
	if err != nil {
		slog.Debug("authenticate failed", "err", err, "network", r.Header.Get("X-Network"))
		w.WriteHeader(http.StatusForbidden)
		return
	}

	peerID := r.Header.Get("X-PeerID")

	nonce := peer.MustParseNonce(r.Header.Get("X-Nonce"))
	pm.networkMap.SetIfAbsent(networkID, cmap.New[*Peer]())
	peersMap, _ := pm.networkMap.Get(networkID)
	peer := Peer{
		peerMap:   pm,
		networkID: peer.NetworkID(networkID),
		id:        peer.PeerID(peerID),
		nonce:     nonce,
	}
	if ok := peersMap.SetIfAbsent(peerID, &peer); !ok {
		slog.Debug("address is already in used", "addr", peerID)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	upgradeHeader := http.Header{}
	upgradeHeader.Set("X-Nonce", r.Header.Get("X-Nonce"))
	wsConn, err := pm.wsUpgrader.Upgrade(w, r, upgradeHeader)
	if err != nil {
		slog.Error(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	peer.conn = wsConn
	peer.Start()
}

func (pm *PeerMap) handleClusterConnect(w http.ResponseWriter, r *http.Request) {
	clusterKey := r.Header.Get("X-ClusterKey")
	// TODO
	fmt.Println(clusterKey)
}
