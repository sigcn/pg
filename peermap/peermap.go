package peermap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peermap/auth"
	"github.com/rkonfj/peerguard/peermap/exporter"
	exporterauth "github.com/rkonfj/peerguard/peermap/exporter/auth"
	"github.com/rkonfj/peerguard/peermap/oidc"
	"golang.org/x/time/rate"
)

type Peer struct {
	exitSig        chan struct{}
	peerMap        *PeerMap
	secret         auth.JSONSecret
	networkContext *networkContext
	metadata       peer.Metadata
	conn           *websocket.Conn
	activeTime     time.Time
	networkID      string
	id             peer.ID
	nonce          byte
	wMut           sync.Mutex
}

func (p *Peer) write(b []byte) error {
	for i, v := range b {
		b[i] = v ^ p.nonce
	}
	return p.writeWS(websocket.BinaryMessage, b)
}

func (p *Peer) writeWS(messageType int, b []byte) error {
	if p.networkContext.ratelimiter != nil {
		p.networkContext.ratelimiter.WaitN(context.Background(), len(b))
	}
	p.wMut.Lock()
	defer p.wMut.Unlock()
	return p.conn.WriteMessage(messageType, b)
}

func (p *Peer) close() error {
	if ctx, ok := p.peerMap.networkMap.Get(string(p.networkID)); ok {
		slog.Debug("PeerRemoved", "network", p.networkID, "peer", p.id)
		ctx.Remove(string(p.id))
	}
	_ = p.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
	return p.conn.Close()
}

func (p *Peer) Close() error {
	close(p.exitSig)
	return p.close()
}

func (p *Peer) String() string {
	metadata := url.Values{}
	metadata.Add("alias1", p.metadata.Alias1)
	metadata.Add("alias2", p.metadata.Alias2)
	for k, v := range p.metadata.Extra {
		b, _ := json.Marshal(v)
		metadata.Add(k, string(b))
	}
	return (&url.URL{
		Scheme:   "pg",
		Host:     string(p.id),
		RawQuery: metadata.Encode(),
	}).String()
}

func (p *Peer) Start() {
	p.activeTime = time.Now()
	go p.readMessageLoop()
	go p.keepalive()
	if p.metadata.SilenceMode {
		return
	}

	ctx, _ := p.peerMap.networkMap.Get(string(p.networkID))
	for target := range ctx.IterBuffered() {
		if target.Key == string(p.id) {
			continue
		}

		if target.Val.metadata.SilenceMode {
			continue
		}
		p.leadDisco(target.Val)
	}
}

func (p *Peer) leadDisco(target *Peer) {
	myMeta := p.metadata.MustMarshalJSON()
	b := make([]byte, 2+len(p.id)+len(myMeta))
	b[0] = peer.CONTROL_NEW_PEER
	b[1] = p.id.Len()
	copy(b[2:], p.id.Bytes())
	copy(b[len(p.id)+2:], myMeta)
	target.write(b)

	peerMeta := target.metadata.MustMarshalJSON()
	b1 := make([]byte, 2+len(target.id)+len(peerMeta))
	b1[0] = peer.CONTROL_NEW_PEER
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
			if !websocket.IsCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
				websocket.CloseNormalClosure) {
				slog.Debug("ReadLoopExited", "details", err.Error())
			}
			p.Close()
			return
		}
		p.activeTime = time.Now()
		if p.networkContext.ratelimiter != nil {
			p.networkContext.ratelimiter.WaitN(context.Background(), len(b))
		}
		switch mt {
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ p.nonce
		}
		tgtPeerID := peer.ID(b[2 : b[1]+2])
		slog.Debug("PeerEvent", "op", b[0], "from", p.id, "to", tgtPeerID)
		tgtPeer, err := p.peerMap.FindPeer(p.networkID, tgtPeerID)
		if err != nil {
			slog.Debug("FindPeer failed", "detail", err)
			continue
		}
		switch b[0] {
		case peer.CONTROL_LEAD_DISCO:
			p.leadDisco(tgtPeer)
		default:
			data := b[b[1]+2:]
			bb := make([]byte, 2+len(p.id)+len(data))
			bb[0] = b[0]
			bb[1] = p.id.Len()
			copy(bb[2:p.id.Len()+2], p.id.Bytes())
			copy(bb[p.id.Len()+2:], data)
			_ = tgtPeer.write(bb)
		}
	}
}

func (p *Peer) keepalive() {
	ticker := time.NewTicker(12 * time.Second)
	for {
		select {
		case <-p.exitSig:
			ticker.Stop()
			return
		case <-ticker.C:
		}
		if err := p.writeWS(websocket.TextMessage, nil); err != nil {
			break
		}
		if time.Since(p.activeTime) > 25*time.Second {
			slog.Debug("Closing inactive connection", "peer", p.id)
			break
		}

		if time.Until(time.Unix(p.secret.Deadline, 0)) < 1*time.Hour {
			secret, err := p.peerMap.generateSecret(p.secret.Network)
			if err != nil {
				slog.Error("NetworkSecretRefresh", "err", err)
				continue
			}
			b, err := json.Marshal(secret)
			if err != nil {
				slog.Error("NetworkSecretRefresh", "err", err)
				continue
			}
			data := make([]byte, 1+len(b))
			data[0] = peer.CONTROL_UPDATE_NETWORK_SECRET
			copy(data[1:], b)
			if err = p.write(data); err != nil {
				slog.Error("NetworkSecretRefresh", "err", err)
				continue
			}
			p.secret, _ = p.peerMap.authenticator.ParseSecret(secret.Secret)
		}
	}
	p.close()
}

type networkContext struct {
	cmap.ConcurrentMap[string, *Peer]
	ratelimiter *rate.Limiter
	createTime  time.Time
}

type PeerMap struct {
	httpServer            *http.Server
	wsUpgrader            *websocket.Upgrader
	networkMap            cmap.ConcurrentMap[string, *networkContext]
	cfg                   Config
	authenticator         *auth.Authenticator
	exporterAuthenticator *exporterauth.Authenticator
}

func (pm *PeerMap) FindPeer(networkID string, peerID peer.ID) (*Peer, error) {
	if ctx, ok := pm.networkMap.Get(string(networkID)); ok {
		if peer, ok := ctx.Get(string(peerID)); ok {
			return peer, nil
		}
	}
	return nil, fmt.Errorf("peer(%s/%s) not found", networkID, peerID)
}

func (pm *PeerMap) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		fmt.Println("Graceful shutdown")
		pm.close()
	}()
	slog.Info("Serving for http now", "listen", pm.cfg.Listen)
	return pm.httpServer.ListenAndServe()
}

func (pm *PeerMap) close() {
	pm.httpServer.Shutdown(context.Background())
}

func New(cfg Config) (*PeerMap, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	pm := PeerMap{
		httpServer:            &http.Server{Handler: mux, Addr: cfg.Listen},
		wsUpgrader:            &websocket.Upgrader{},
		networkMap:            cmap.New[*networkContext](),
		authenticator:         auth.NewAuthenticator(cfg.SecretKey),
		exporterAuthenticator: exporterauth.New(cfg.SecretKey),
		cfg:                   cfg,
	}
	mux.HandleFunc("/", pm.HandlePeerPacketConnect)
	mux.HandleFunc("/networks", pm.HandleQueryNetworks)
	mux.HandleFunc("/peers", pm.HandleQueryNetworkPeers)

	mux.HandleFunc("/network/token", oidc.HandleNotifyToken)
	mux.HandleFunc("/oidc/", oidc.RedirectAuthURL)
	mux.HandleFunc("/oidc/authorize/", pm.HandleOIDCAuthorize)
	return &pm, nil
}

func (pm *PeerMap) HandleQueryNetworks(w http.ResponseWriter, r *http.Request) {
	exporterToken := r.Header.Get("X-Token")
	_, err := pm.exporterAuthenticator.CheckToken(exporterToken)
	if err != nil {
		slog.Debug("ExporterAuthFailed", "details", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	items := pm.networkMap.Items()
	networks := make([]exporter.NetworkHead, 0, len(items))
	for k, v := range items {
		networks = append(networks, exporter.NetworkHead{
			ID:         k,
			PeersCount: v.Count(),
			CreateTime: fmt.Sprintf("%d", v.createTime.UnixNano()),
		})
	}
	json.NewEncoder(w).Encode(networks)
}

func (pm *PeerMap) HandleQueryNetworkPeers(w http.ResponseWriter, r *http.Request) {
	exporterToken := r.Header.Get("X-Token")
	_, err := pm.exporterAuthenticator.CheckToken(exporterToken)
	if err != nil {
		slog.Debug("ExporterAuthFailed", "details", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var networks []exporter.Network
	for item := range pm.networkMap.IterBuffered() {
		var peers []string
		for _, peer := range item.Val.Items() {
			peers = append(peers, peer.String())
		}
		networks = append(networks, exporter.Network{
			ID:    item.Key,
			Peers: peers,
		})
	}
	json.NewEncoder(w).Encode(networks)
}

func (pm *PeerMap) HandleOIDCAuthorize(w http.ResponseWriter, r *http.Request) {
	providerName := path.Base(r.URL.Path)
	provider, ok := oidc.Provider(providerName)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	email, _, err := provider.UserInfo(r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("OIDC get userInfo error", "err", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	secret, err := pm.generateSecret(email)
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

func (pm *PeerMap) generateSecret(network string) (peer.NetworkSecret, error) {
	secret, err := auth.NewAuthenticator(pm.cfg.SecretKey).GenerateSecret(network, 5*time.Hour)
	if err != nil {
		return peer.NetworkSecret{}, err
	}
	return peer.NetworkSecret{
		Network: network,
		Secret:  secret,
		Expire:  time.Now().Add(5*time.Hour - 10*time.Second),
	}, nil
}

func (pm *PeerMap) HandlePeerPacketConnect(w http.ResponseWriter, r *http.Request) {
	jsonSecret, err := pm.authenticator.ParseSecret(r.Header.Get("X-Network"))
	if err != nil {
		slog.Debug("Authenticate failed", "err", err, "network", jsonSecret.Network, "secret", r.Header.Get("X-Network"))
		w.WriteHeader(http.StatusForbidden)
		return
	}

	peerID := r.Header.Get("X-PeerID")

	nonce := peer.MustParseNonce(r.Header.Get("X-Nonce"))

	if !pm.networkMap.Has(jsonSecret.Network) {
		var rateLimiter *rate.Limiter
		if pm.cfg.RateLimiter != nil {
			if pm.cfg.RateLimiter.Limit > 0 {
				rateLimiter = rate.NewLimiter(rate.Limit(pm.cfg.RateLimiter.Limit), pm.cfg.RateLimiter.Burst)
			}
		}
		pm.networkMap.SetIfAbsent(jsonSecret.Network, &networkContext{
			ConcurrentMap: cmap.New[*Peer](),
			ratelimiter:   rateLimiter,
			createTime:    time.Now(),
		})
	}

	networkCtx, _ := pm.networkMap.Get(jsonSecret.Network)
	peer := Peer{
		exitSig:        make(chan struct{}),
		peerMap:        pm,
		secret:         jsonSecret,
		networkContext: networkCtx,
		networkID:      jsonSecret.Network,
		id:             peer.ID(peerID),
		nonce:          nonce,
		metadata:       peer.Metadata{},
	}

	metadata := r.Header.Get("X-Metadata")
	if len(metadata) > 0 {
		b, err := base64.StdEncoding.DecodeString(metadata)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.Unmarshal(b, &peer.metadata)
	}

	if ok := networkCtx.SetIfAbsent(peerID, &peer); !ok {
		slog.Debug("Address is already in used", "addr", peerID)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	upgradeHeader := http.Header{}
	upgradeHeader.Set("X-Nonce", r.Header.Get("X-Nonce"))
	stuns, _ := json.Marshal(pm.cfg.STUNs)
	upgradeHeader.Set("X-STUNs", base64.StdEncoding.EncodeToString(stuns))
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
