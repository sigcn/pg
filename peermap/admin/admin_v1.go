package admin

import (
	"errors"
	"net/http"
	"runtime/debug"
	"sync"

	"github.com/sigcn/pg/langs"
	"github.com/sigcn/pg/peermap/admin/types"
	"github.com/sigcn/pg/peermap/auth"
)

var (
	Version      string = "dev"
	ErrForbidden        = langs.Error{Code: 10000, Msg: "forbidden"}
)

type AdministratorV1 struct {
	Auth      *auth.Authenticator
	PeerStore types.PeerStore

	mux      http.ServeMux
	initOnce sync.Once
}

func (a *AdministratorV1) init() {
	a.initOnce.Do(func() {
		a.mux.HandleFunc("GET /pg/apis/v1/admin/peers", a.handleQueryPeers)
		a.mux.HandleFunc("GET /pg/apis/v1/admin/server_info", a.handleQueryServerInfo)
	})
}

func (a *AdministratorV1) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.init()
	a.mux.ServeHTTP(w, r)
}

func (a *AdministratorV1) handleQueryPeers(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Token")
	secret, err := a.Auth.ParseSecret(token)
	if err != nil {
		langs.Err(err).MarshalTo(w)
		return
	}
	if !secret.Admin {
		ErrForbidden.MarshalTo(w)
		return
	}
	peers, err := a.PeerStore.Peers(secret.Network)
	if err != nil {
		langs.Err(err).MarshalTo(w)
		return
	}
	langs.Data[any]{Data: peers}.MarshalTo(w)
}

func (a *AdministratorV1) handleQueryServerInfo(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Token")
	secret, err := a.Auth.ParseSecret(token)
	if err != nil {
		langs.Err(err).MarshalTo(w)
		return
	}
	if !secret.Admin {
		ErrForbidden.MarshalTo(w)
		return
	}

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
