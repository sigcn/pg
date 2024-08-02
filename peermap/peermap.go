package peermap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peermap/auth"
	"github.com/rkonfj/peerguard/peermap/exporter"
	exporterauth "github.com/rkonfj/peerguard/peermap/exporter/auth"
	"github.com/rkonfj/peerguard/peermap/oidc"
	"golang.org/x/time/rate"
)

var (
	ErrAddressAlreadyInuse  = peer.Error{Code: 4000, Msg: "the network address is already in use"}
	ErrNetworkSecretExpired = peer.Error{Code: 4030, Msg: "network secret is expired"}

	_ io.ReadWriter = (*Peer)(nil)
)

type peerStat struct {
	RelayRx  uint64
	StreamTx uint64
	StreamRx uint64
}
type Peer struct {
	conn      *websocket.Conn
	exitSig   chan struct{}
	closeOnce sync.Once
	peerMap   *PeerMap

	networkSecret  auth.JSONSecret
	networkContext *networkContext

	stat       peerStat
	metadata   url.Values
	activeTime atomic.Int64
	id         peer.ID
	nonce      byte
	wMut       sync.Mutex

	relayRatelimiter *rate.Limiter

	connRRL  *rate.Limiter
	connWRL  *rate.Limiter
	connData chan []byte
	connBuf  []byte
}

func (p *Peer) write(b []byte) error {
	for i, v := range b {
		b[i] = v ^ p.nonce
	}
	return p.writeWS(websocket.BinaryMessage, b)
}

func (p *Peer) writeWS(messageType int, b []byte) error {
	p.wMut.Lock()
	defer p.wMut.Unlock()
	return p.conn.WriteMessage(messageType, b)
}

func (p *Peer) Read(b []byte) (n int, err error) {
	defer func() {
		if p.connRRL != nil && n > 0 {
			p.connRRL.WaitN(context.Background(), n)
		}
		p.stat.StreamRx += uint64(n)
	}()
	if p.connBuf != nil {
		n = copy(b, p.connBuf)
		if n < len(p.connBuf) {
			p.connBuf = p.connBuf[n:]
		} else {
			p.connBuf = nil
		}
		return
	}

	wsb, ok := <-p.connData
	if !ok {
		return 0, io.EOF
	}
	n = copy(b, wsb)
	if n < len(wsb) {
		p.connBuf = wsb[n:]
	}
	return
}

func (p *Peer) Write(b []byte) (n int, err error) {
	if p.connWRL != nil && len(b) > 0 {
		p.connWRL.WaitN(context.Background(), len(b))
	}
	err = p.write(append(append([]byte(nil), peer.CONTROL_CONN.Byte()), b...))
	if err != nil {
		return
	}
	p.stat.StreamTx += uint64(len(b))
	return len(b), nil
}

func (p *Peer) Close() error {
	p.closeOnce.Do(func() {
		p.peerMap.removePeer(p.networkSecret.Network, p.id)
		_ = p.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
		p.conn.Close()
		close(p.exitSig)
		close(p.connData)
	})
	return nil
}

func (p *Peer) String() string {
	p.metadata.Set("rrx", fmt.Sprintf("%d", p.stat.RelayRx))
	p.metadata.Set("stx", fmt.Sprintf("%d", p.stat.StreamTx))
	p.metadata.Set("srx", fmt.Sprintf("%d", p.stat.StreamRx))
	return (&url.URL{
		Scheme:   "pg",
		Host:     string(p.id),
		RawQuery: p.metadata.Encode(),
	}).String()
}

func (p *Peer) Start() {
	go p.readMessageLoop()
	go p.keepalive()
	if p.metadata.Has("silenceMode") {
		return
	}

	if p.peerMap.cfg.PublicNetwork == p.networkSecret.Network {
		return
	}

	ctx, _ := p.peerMap.getNetwork(p.networkSecret.Network)
	ctx.peersMutex.RLock()
	defer ctx.peersMutex.RUnlock()
	for k, v := range ctx.peers {
		if k == string(p.id) {
			continue
		}

		if v.metadata.Has("silenceMode") {
			continue
		}
		p.leadDisco(v)
	}
}

func (p *Peer) leadDisco(target *Peer) {
	myMeta := []byte(p.metadata.Encode())
	b := make([]byte, 2+len(p.id)+len(myMeta))
	b[0] = peer.CONTROL_NEW_PEER.Byte()
	b[1] = p.id.Len()
	copy(b[2:], p.id.Bytes())
	copy(b[len(p.id)+2:], myMeta)
	target.write(b)

	peerMeta := []byte(target.metadata.Encode())
	b1 := make([]byte, 2+len(target.id)+len(peerMeta))
	b1[0] = peer.CONTROL_NEW_PEER.Byte()
	b1[1] = target.id.Len()
	copy(b1[2:], target.id.Bytes())
	copy(b1[len(target.id)+2:], peerMeta)
	p.write(b1)
}

func (p *Peer) readMessageLoop() {
	for {
		select {
		case <-p.exitSig:
			return
		default:
		}
		mt, b, err := p.conn.ReadMessage()
		if err != nil {
			slog.Debug("ReadLoopExited", "err", err.Error())
			p.Close()
			return
		}
		p.activeTime.Store(time.Now().Unix())
		switch mt {
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ p.nonce
		}
		if slices.Contains([]peer.ControlCode{peer.CONTROL_LEAD_DISCO, peer.CONTROL_NEW_PEER_UDP_ADDR}, peer.ControlCode(b[0])) {
			p.networkContext.disoRatelimiter.WaitN(context.Background(), len(b))
		} else if p.relayRatelimiter != nil {
			p.relayRatelimiter.WaitN(context.Background(), len(b))
		}
		if b[0] == peer.CONTROL_CONN.Byte() {
			p.connData <- b[1:]
			continue
		}
		tgtPeerID := peer.ID(b[2 : b[1]+2])
		slog.Debug("PeerEvent", "op", peer.ControlCode(b[0]), "from", p.id, "to", tgtPeerID)
		tgtPeer, err := p.peerMap.getPeer(p.networkSecret.Network, tgtPeerID)
		if err != nil {
			slog.Debug("FindPeer failed", "detail", err)
			continue
		}
		if peer.ControlCode(b[0]) == peer.CONTROL_LEAD_DISCO {
			p.leadDisco(tgtPeer)
			continue
		}
		if peer.ControlCode(b[0]) == peer.CONTROL_NEW_PEER_UDP_ADDR {
			p.updatePeerUDPAddr(b)
		}
		data := b[b[1]+2:]
		bb := make([]byte, 2+len(p.id)+len(data))
		bb[0] = b[0]
		bb[1] = p.id.Len()
		copy(bb[2:p.id.Len()+2], p.id.Bytes())
		copy(bb[p.id.Len()+2:], data)
		_ = tgtPeer.write(bb)
		p.stat.RelayRx += uint64(len(b))
	}
}

func (p *Peer) updatePeerUDPAddr(b []byte) {
	if b[b[1]+2] != 'a' {
		return
	}
	addrLen := b[b[1]+3]
	s := b[1] + 4
	addr, err := net.ResolveUDPAddr("udp", string(b[s:s+addrLen]))
	if err != nil {
		slog.Error("Resolve udp addr error", "err", err)
		return
	}
	natType := disco.NATType(b[s+addrLen:])
	slog.Debug("ExchangeUDPAddr", "nat", natType, "addr", addr.String())
	if slices.Contains([]disco.NATType{disco.Easy, disco.Hard, disco.IP6, disco.IP4}, natType) {
		if natType.AccurateThan(disco.NATType(p.metadata.Get("nat"))) {
			p.metadata.Set("nat", natType.String())
		}
		if !slices.Contains(p.metadata["addr"], addr.String()) {
			p.metadata.Add("addr", addr.String())
		}
	}
}

func (p *Peer) keepalive() {
	p.activeTime.Store(time.Now().Unix())
	p.conn.SetPongHandler(func(appData string) error {
		p.activeTime.Store(time.Now().Unix())
		slog.Debug("Pong", "peer", p.id)
		return nil
	})
	ticker := time.NewTicker(12 * time.Second)
	for {
		select {
		case <-p.exitSig:
			ticker.Stop()
			return
		case <-ticker.C:
		}
		if time.Now().Unix()-p.activeTime.Load() > 25 {
			slog.Debug("Closing inactive connection", "peer", p.id)
			break
		}
		err := p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
		if err != nil {
			slog.Warn("Ping", "err", err)
		} else {
			slog.Debug("Ping", "peer", p.id)
		}
		if time.Until(time.Unix(p.networkSecret.Deadline, 0)) <
			p.peerMap.cfg.SecretValidityPeriod-p.peerMap.cfg.SecretRotationPeriod {
			p.updateSecret()
		}
	}
	p.Close()
}

func (p *Peer) updateSecret() error {
	secret, err := p.peerMap.generateSecret(auth.Net{
		ID:        p.networkSecret.Network,
		Alias:     p.networkContext.alias,
		Neighbors: p.networkContext.neighbors,
	})
	if err != nil {
		slog.Error("NetworkSecretRefresh", "err", err)
		return err
	}
	b, err := json.Marshal(secret)
	if err != nil {
		slog.Error("NetworkSecretRefresh", "err", err)
		return err
	}
	data := make([]byte, 1+len(b))
	data[0] = peer.CONTROL_UPDATE_NETWORK_SECRET.Byte()
	copy(data[1:], b)
	if err = p.write(data); err != nil {
		slog.Error("NetworkSecretRefresh", "err", err)
		return err
	}
	p.networkSecret, _ = p.peerMap.authenticator.ParseSecret(secret.Secret)
	return nil
}

func (p *Peer) checkAlive() bool {
	seconds := time.Now().Unix()
	for range 3 {
		slog.Debug("CheckAlive", "sec", seconds, "active", p.activeTime.Load(), "peer", p.id)
		if seconds-p.activeTime.Load() <= 2 {
			return true
		}
		p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
		time.Sleep(200 * time.Millisecond)
	}
	p.Close()
	return false
}

type networkContext struct {
	peersMutex      sync.RWMutex
	peers           map[string]*Peer
	disoRatelimiter *rate.Limiter
	createTime      time.Time
	updateTime      time.Time

	id        string
	metaMutex sync.Mutex
	alias     string
	neighbors []string
}

func (ctx *networkContext) removePeer(id peer.ID) {
	ctx.peersMutex.Lock()
	defer ctx.peersMutex.Unlock()
	delete(ctx.peers, string(id))
}

func (ctx *networkContext) getPeer(id peer.ID) (*Peer, bool) {
	ctx.peersMutex.RLock()
	defer ctx.peersMutex.RUnlock()
	p, ok := ctx.peers[id.String()]
	return p, ok
}

func (ctx *networkContext) peerCount() int {
	ctx.peersMutex.RLock()
	defer ctx.peersMutex.RUnlock()
	return len(ctx.peers)
}

func (ctx *networkContext) SetIfAbsent(peerID string, p *Peer) bool {
	ctx.peersMutex.Lock()
	if p1, ok := ctx.peers[peerID]; ok {
		ctx.peersMutex.Unlock()
		if p1.checkAlive() {
			return false
		}
		ctx.peersMutex.Lock()
	}
	ctx.peers[peerID] = p
	ctx.peersMutex.Unlock()
	return true
}

func (ctx *networkContext) initMeta(n auth.Net, updateTime time.Time) {
	ctx.metaMutex.Lock()
	defer ctx.metaMutex.Unlock()
	if ctx.updateTime.After(updateTime) {
		return
	}
	ctx.updateTime = updateTime
	ctx.alias = n.Alias
	ctx.neighbors = n.Neighbors
}

func (ctx *networkContext) updateMeta(n auth.Net) error {
	ctx.metaMutex.Lock()
	defer ctx.metaMutex.Unlock()
	if ctx.alias == n.Alias && slices.Equal(ctx.neighbors, n.Neighbors) {
		return nil
	}
	ctx.updateTime = time.Now()
	ctx.neighbors = n.Neighbors
	ctx.alias = n.Alias
	ctx.peersMutex.RLock()
	defer ctx.peersMutex.RUnlock()
	for _, v := range ctx.peers {
		v.updateSecret()
	}
	return nil
}

type NetState struct {
	ID         string    `json:"id"`
	Alias      string    `json:"alias"`
	Neighbors  []string  `json:"neighbors"`
	CreateTime time.Time `json:"createTime"`
	UpdateTime time.Time `json:"updateTime"`
}

type PeerMap struct {
	httpServer            *http.Server
	wsUpgrader            *websocket.Upgrader
	networkMapMutex       sync.RWMutex
	networkMap            map[string]*networkContext
	peerMapMutex          sync.RWMutex
	peerMap               map[string]*networkContext
	cfg                   Config
	authenticator         *auth.Authenticator
	exporterAuthenticator *exporterauth.Authenticator
}

func (pm *PeerMap) removePeer(network string, id peer.ID) {
	if ctx, ok := pm.getNetwork(network); ok {
		slog.Debug("PeerRemoved", "network", network, "peer", id)
		ctx.removePeer(id)
		pm.peerMapMutex.Lock()
		delete(pm.peerMap, id.String())
		pm.peerMapMutex.Unlock()
	}
}

func (pm *PeerMap) getNetwork(network string) (*networkContext, bool) {
	pm.networkMapMutex.RLock()
	defer pm.networkMapMutex.RUnlock()
	ctx, ok := pm.networkMap[network]
	return ctx, ok
}

func (pm *PeerMap) getPeer(network string, peerID peer.ID) (*Peer, error) {
	if ctx, ok := pm.getNetwork(network); ok {
		if peer, ok := ctx.getPeer(peerID); ok {
			return peer, nil
		}
		pm.peerMapMutex.RLock()
		neighNet, ok := pm.peerMap[peerID.String()]
		pm.peerMapMutex.RUnlock()
		if ok && slices.Contains(ctx.neighbors, neighNet.id) {
			if peer, ok := neighNet.getPeer(peerID); ok {
				return peer, nil
			}
		}
	}
	return nil, fmt.Errorf("peer(%s/%s) not found", network, peerID)
}

func (pm *PeerMap) FindPeer(network string, filter func(url.Values) bool) ([]*Peer, error) {
	if ctx, ok := pm.getNetwork(network); ok {
		var ret []*Peer
		ctx.peersMutex.RLock()
		defer ctx.peersMutex.RUnlock()
		for _, v := range ctx.peers {
			if filter(v.metadata) {
				ret = append(ret, v)
			}
		}
		return ret, nil
	}
	return nil, fmt.Errorf("peer(%s/metafilter) not found", network)
}

func (pm *PeerMap) Serve(ctx context.Context) error {
	slog.Debug("ApplyConfig", "cfg", pm.cfg)
	// watch sigterm for exit
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		slog.Info("Graceful shutdown")
		pm.httpServer.Shutdown(context.Background())
		if err := pm.Save(); err != nil {
			slog.Error("Save networks", "err", err)
		}
	}()
	// load networks
	if err := pm.Load(); err != nil {
		slog.Error("Load networks", "err", err)
	}
	// watch sighup for save networks
	go pm.watchSaveCycle(ctx)
	// serving http
	slog.Info("Serving for http now", "listen", pm.cfg.Listen)
	err := pm.httpServer.ListenAndServe()
	wg.Wait()
	return err
}

// Load networks state
func (pm *PeerMap) Load() error {
	f, err := os.Open(pm.cfg.StateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load: open state file: %w", err)
	}
	defer f.Close()
	var nets []NetState
	if err := json.NewDecoder(f).Decode(&nets); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("load: decode state: %w", err)
	}
	pm.networkMapMutex.Lock()
	defer pm.networkMapMutex.Unlock()
	for _, n := range nets {
		pm.networkMap[n.ID] = pm.newNetworkContext(n)
	}
	slog.Info("Load networks", "count", len(nets))
	return nil
}

// Save networks state
func (pm *PeerMap) Save() error {
	var nets []NetState
	pm.networkMapMutex.RLock()
	for _, v := range pm.networkMap {
		nets = append(nets, NetState{
			ID:         v.id,
			Alias:      v.alias,
			Neighbors:  v.neighbors,
			CreateTime: v.createTime,
			UpdateTime: v.updateTime})
	}
	pm.networkMapMutex.RUnlock()
	if nets == nil {
		return nil
	}
	f, err := os.Create(pm.cfg.StateFile)
	if err != nil {
		return fmt.Errorf("save: open state file: %w", err)
	}
	if err := json.NewEncoder(f).Encode(nets); err != nil {
		return fmt.Errorf("save: encode state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save: close state file: %w", err)
	}
	slog.Info("Save networks", "count", len(nets))
	return nil
}

func (pm *PeerMap) HandleQueryNetworks(w http.ResponseWriter, r *http.Request) {
	if err := pm.checkAdminToken(w, r); err != nil {
		return
	}
	var networks []exporter.NetworkHead
	pm.networkMapMutex.RLock()
	for k, v := range pm.networkMap {
		networks = append(networks, exporter.NetworkHead{
			ID:         k,
			Alias:      v.alias,
			PeersCount: v.peerCount(),
			CreateTime: fmt.Sprintf("%d", v.createTime.UnixNano()),
		})
	}
	pm.networkMapMutex.RUnlock()
	json.NewEncoder(w).Encode(networks)
}

func (pm *PeerMap) HandleQueryNetworkPeers(w http.ResponseWriter, r *http.Request) {
	if err := pm.checkAdminToken(w, r); err != nil {
		return
	}
	var networks []exporter.Network
	pm.networkMapMutex.RLock()
	for k, v := range pm.networkMap {
		var peers []string
		v.peersMutex.RLock()
		for _, peer := range v.peers {
			peers = append(peers, peer.String())
		}
		v.peersMutex.RUnlock()
		networks = append(networks, exporter.Network{ID: k, Alias: v.alias, Peers: peers})
	}
	pm.networkMapMutex.RUnlock()
	json.NewEncoder(w).Encode(networks)
}

func (pm *PeerMap) HandleGetNetworkMeta(w http.ResponseWriter, r *http.Request) {
	if err := pm.checkAdminToken(w, r); err != nil {
		return
	}
	ctx, ok := pm.getNetwork(r.PathValue("network"))
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(exporter.NetworkMeta{Alias: ctx.alias, Neighbors: ctx.neighbors})
}

func (pm *PeerMap) HandlePutNetworkMeta(w http.ResponseWriter, r *http.Request) {
	if err := pm.checkAdminToken(w, r); err != nil {
		return
	}
	network := r.PathValue("network")
	var request exporter.NetworkMeta
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, ok := pm.getNetwork(network)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err := ctx.updateMeta(auth.Net{
		Alias:     request.Alias,
		Neighbors: request.Neighbors,
	}); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (pm *PeerMap) HandleOIDCAuthorize(w http.ResponseWriter, r *http.Request) {
	provider, ok := oidc.Provider(r.PathValue("provider"))
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	email, _, err := provider.UserInfo(r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("OIDC get userInfo error", "err", err)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(fmt.Sprintf("oidc: %s", err)))
		return
	}
	if email == "" {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("odic: email is required"))
		return
	}
	n := auth.Net{ID: email}
	if ctx, ok := pm.getNetwork(email); ok {
		n.Alias = ctx.alias
		n.Neighbors = ctx.neighbors
	}
	secret, err := pm.generateSecret(n)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = oidc.NotifyToken(r.URL.Query().Get("state"), secret)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Write([]byte("ok"))
}

func (pm *PeerMap) HandlePeerPacketConnect(w http.ResponseWriter, r *http.Request) {
	networkSecrest := r.Header.Get("X-Network")
	jsonSecret := auth.JSONSecret{
		Network:  networkSecrest,
		Deadline: math.MaxInt64,
	}
	if len(pm.cfg.PublicNetwork) == 0 || pm.cfg.PublicNetwork != networkSecrest {
		secret, err := pm.authenticator.ParseSecret(networkSecrest)
		if err != nil {
			slog.Debug("Authenticate failed", "err", err, "network", jsonSecret.Network, "secret", r.Header.Get("X-Network"))
			w.WriteHeader(http.StatusForbidden)
			ErrNetworkSecretExpired.MarshalTo(w)
			return
		}
		jsonSecret = secret
	}

	peerID := r.Header.Get("X-PeerID")
	nonce := peer.MustParseNonce(r.Header.Get("X-Nonce"))

	pm.networkMapMutex.RLock()
	networkCtx, ok := pm.networkMap[jsonSecret.Network]
	pm.networkMapMutex.RUnlock()
	if !ok {
		pm.networkMapMutex.Lock()
		networkCtx, ok = pm.networkMap[jsonSecret.Network]
		if !ok {
			networkCtx = pm.newNetworkContext(NetState{
				ID:         jsonSecret.Network,
				CreateTime: time.Now(),
			})
			pm.networkMap[jsonSecret.Network] = networkCtx
		}
		pm.networkMapMutex.Unlock()
	}

	networkCtx.initMeta(
		auth.Net{Alias: jsonSecret.Alias, Neighbors: jsonSecret.Neighbors},
		time.Unix(jsonSecret.Deadline, 0).Add(-pm.cfg.SecretValidityPeriod))

	var rateLimiter, srLimiter, swLimiter *rate.Limiter
	if pm.cfg.RateLimiter != nil && pm.cfg.RateLimiter.Relay.Limit > 0 {
		rateLimiter = rate.NewLimiter(rate.Limit(pm.cfg.RateLimiter.Relay.Limit), pm.cfg.RateLimiter.Relay.Burst)
	}
	if pm.cfg.RateLimiter != nil && pm.cfg.RateLimiter.StreamR.Limit > 0 {
		srLimiter = rate.NewLimiter(rate.Limit(pm.cfg.RateLimiter.StreamR.Limit), pm.cfg.RateLimiter.StreamR.Burst)
	}
	if pm.cfg.RateLimiter != nil && pm.cfg.RateLimiter.StreamW.Limit > 0 {
		srLimiter = rate.NewLimiter(rate.Limit(pm.cfg.RateLimiter.StreamW.Limit), pm.cfg.RateLimiter.StreamW.Burst)
	}
	peer := Peer{
		exitSig:          make(chan struct{}),
		peerMap:          pm,
		networkSecret:    jsonSecret,
		networkContext:   networkCtx,
		id:               peer.ID(peerID),
		nonce:            nonce,
		relayRatelimiter: rateLimiter,
		connRRL:          srLimiter,
		connWRL:          swLimiter,
		connData:         make(chan []byte, 128),
	}

	metadata := r.Header.Get("X-Metadata")
	if len(metadata) > 0 {
		_, err := base64.StdEncoding.DecodeString(metadata)
		if err == nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		meta, err := url.ParseQuery(metadata)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		peer.metadata = meta
	}

	if ok := networkCtx.SetIfAbsent(peerID, &peer); !ok {
		slog.Debug("Address is already in used", "addr", peerID)
		w.WriteHeader(http.StatusForbidden)
		ErrAddressAlreadyInuse.MarshalTo(w)
		return
	}
	pm.peerMapMutex.Lock()
	pm.peerMap[peerID] = networkCtx
	pm.peerMapMutex.Unlock()
	upgradeHeader := http.Header{}
	upgradeHeader.Set("X-Nonce", r.Header.Get("X-Nonce"))
	stuns, _ := json.Marshal(pm.cfg.STUNs)
	upgradeHeader.Set("X-STUNs", base64.StdEncoding.EncodeToString(stuns))
	if pm.cfg.RateLimiter != nil {
		if pm.cfg.RateLimiter.Relay.Limit > 0 {
			upgradeHeader.Set("X-Limiter-Burst", fmt.Sprintf("%d", pm.cfg.RateLimiter.Relay.Burst))
			upgradeHeader.Set("X-Limiter-Limit", fmt.Sprintf("%d", pm.cfg.RateLimiter.Relay.Limit))
		}
		if pm.cfg.RateLimiter.StreamR.Limit > 0 {
			upgradeHeader.Set("X-Limiter-Stream-Burst", fmt.Sprintf("%d", pm.cfg.RateLimiter.StreamR.Burst))
			upgradeHeader.Set("X-Limiter-Stream-Limit", fmt.Sprintf("%d", pm.cfg.RateLimiter.StreamR.Limit))
		}
	}
	wsConn, err := pm.wsUpgrader.Upgrade(w, r, upgradeHeader)
	if err != nil {
		slog.Error(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	peer.conn = wsConn
	peer.Start()
	slog.Debug("PeerConnected", "network", jsonSecret.Network, "peer", peerID)
}

func (pm *PeerMap) watchSaveCycle(ctx context.Context) {
	for {
		sig := make(chan os.Signal, 2)
		signal.Notify(sig, syscall.SIGHUP)
		select {
		case <-ctx.Done():
			close(sig)
			return
		case <-sig:
			close(sig)
			if err := pm.Save(); err != nil {
				slog.Error("Save networks", "err", err)
			}
		}
	}
}

func (pm *PeerMap) newNetworkContext(state NetState) *networkContext {
	return &networkContext{
		id:              state.ID,
		peers:           make(map[string]*Peer),
		disoRatelimiter: rate.NewLimiter(rate.Limit(10*1024), 128*1024),
		createTime:      state.CreateTime,
		updateTime:      state.UpdateTime,
		alias:           state.Alias,
		neighbors:       state.Neighbors,
	}
}

func (pm *PeerMap) generateSecret(n auth.Net) (peer.NetworkSecret, error) {
	secret, err := auth.NewAuthenticator(pm.cfg.SecretKey).GenerateSecret(n, pm.cfg.SecretValidityPeriod)
	if err != nil {
		return peer.NetworkSecret{}, err
	}
	return peer.NetworkSecret{
		Network: n.ID,
		Secret:  secret,
		Expire:  time.Now().Add(pm.cfg.SecretValidityPeriod - 10*time.Second),
	}, nil
}

func (pm *PeerMap) checkAdminToken(w http.ResponseWriter, r *http.Request) error {
	exporterToken := r.Header.Get("X-Token")
	_, err := pm.exporterAuthenticator.CheckToken(exporterToken)
	if err != nil {
		err = fmt.Errorf("exporter auth: %w", err)
		slog.Debug("ExporterAuthFailed", "err", err)
		w.WriteHeader(http.StatusUnauthorized)
		return err
	}
	return nil
}

func New(cfg Config) (*PeerMap, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}

	pm := PeerMap{
		wsUpgrader:            &websocket.Upgrader{},
		networkMap:            make(map[string]*networkContext),
		peerMap:               make(map[string]*networkContext),
		authenticator:         auth.NewAuthenticator(cfg.SecretKey),
		exporterAuthenticator: exporterauth.New(cfg.SecretKey),
		cfg:                   cfg,
	}

	mux := http.NewServeMux()
	pm.httpServer = &http.Server{Handler: mux, Addr: cfg.Listen}
	mux.HandleFunc("GET /pg", pm.HandlePeerPacketConnect)
	mux.HandleFunc("GET /pg/networks", pm.HandleQueryNetworks)
	mux.HandleFunc("GET /pg/peers", pm.HandleQueryNetworkPeers)
	mux.HandleFunc("GET /pg/networks/{network}/meta", pm.HandleGetNetworkMeta)
	mux.HandleFunc("PUT /pg/networks/{network}/meta", pm.HandlePutNetworkMeta)

	mux.HandleFunc("GET /oidc", oidc.OIDCSelector)
	mux.HandleFunc("GET /oidc/secret", oidc.OIDCSecret)
	mux.HandleFunc("GET /oidc/{provider}", oidc.OIDCAuthURL)
	mux.HandleFunc("GET /oidc/authorize/{provider}", pm.HandleOIDCAuthorize)
	return &pm, nil
}
