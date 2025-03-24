package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sigcn/pg/langs"
	"github.com/sigcn/pg/peermap/api/types"
	"github.com/sigcn/pg/peermap/auth"
	"github.com/sigcn/pg/peermap/config"
	"github.com/sigcn/pg/peermap/oidc"
)

var (
	Version      string = "dev"
	ErrForbidden        = langs.Error{Code: 10000, Msg: "forbidden"}
)

type contextKey string

type ApiV1 struct {
	Config    config.Config
	Auth      *auth.Authenticator
	PeerStore types.PeerStore
	Grant     oidc.Grant

	mux      http.ServeMux
	initOnce sync.Once
}

func (a *ApiV1) init() {
	a.initOnce.Do(func() {
		a.mux.HandleFunc("GET /api/v1/r8/stuns", a.handleQuerySTUNs)
		a.mux.HandleFunc("GET /api/v1/r5/peers", a.handleQueryPeers)
		a.mux.HandleFunc("GET /api/v1/r5/psns.json", a.handleDownloadSecret)
		a.mux.HandleFunc("GET /api/v1/r5/server_info", a.handleQueryServerInfo)
	})
}

func (a *ApiV1) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.init()
	token := r.Header.Get("X-Token")
	secret, err := a.Auth.ParseSecret(token)
	if err != nil {
		langs.Err(err).MarshalTo(w)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/r5/") && !secret.Admin {
		ErrForbidden.MarshalTo(w)
		return
	}
	if time.Until(time.Unix(secret.Deadline, 0)) <
		a.Config.SecretValidityPeriod-a.Config.SecretRotationPeriod {
		if newSecret, err := a.Grant(secret.Network, "PG_ADM"); err == nil {
			b, _ := json.Marshal(newSecret)
			w.Header().Add("X-Set-Token", string(b))
		}
	}
	a.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey("secret"), secret)))
}

func (a *ApiV1) handleQuerySTUNs(w http.ResponseWriter, r *http.Request) {
	var ret []string
	for _, stun := range a.Config.STUNs {
		if strings.Contains(stun, "://") {
			ret = append(ret, stun)
		} else {
			ret = append(ret, fmt.Sprintf("udp://%s", stun))
		}
	}
	langs.Data[[]string]{Data: ret}.MarshalTo(w)
}

func (a *ApiV1) handleQueryPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := a.PeerStore.Peers(r.Context().Value(contextKey("secret")).(auth.JSONSecret).Network)
	if err != nil {
		langs.Err(err).MarshalTo(w)
		return
	}
	langs.Data[any]{Data: peers}.MarshalTo(w)
}

func (a *ApiV1) handleDownloadSecret(w http.ResponseWriter, r *http.Request) {
	secret := r.Context().Value(contextKey("secret")).(auth.JSONSecret)
	secretJSON, err := a.Grant(secret.Network, "")
	if err != nil {
		langs.Err(err).MarshalTo(w)
		return
	}
	fileName := fmt.Sprintf("%s_psns.json", secret.Network)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Transfer-Encoding", "binary")
	json.NewEncoder(w).Encode(secretJSON)
	slog.Info("Generate a secret", "network", secret.Network)
}

func (a *ApiV1) handleQueryServerInfo(w http.ResponseWriter, r *http.Request) {
	info, err := readBuildInfo()
	if err != nil {
		langs.Err(err).MarshalTo(w)
	}
	langs.Data[any]{Data: serverInfo{Version: Version, buildInfo: info}}.MarshalTo(w)
}

type buildInfo struct {
	GoVersion   string `json:"go_version"`
	VCSRevision string `json:"vcs_revision"`
	VCSTime     string `json:"vcs_time"`
}

func readBuildInfo() (buildInfo buildInfo, err error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		err = errors.ErrUnsupported
		return
	}
	buildInfo.GoVersion = info.GoVersion
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			buildInfo.VCSRevision = s.Value
			continue
		}
		if s.Key == "vcs.time" {
			buildInfo.VCSTime = s.Value
		}
	}
	return
}

type serverInfo struct {
	Version string `json:"version"`
	buildInfo
}
