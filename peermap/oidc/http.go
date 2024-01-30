package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/rkonfj/peerguard/peer"
)

var (
	notifyContext    = make(map[string]chan NetworkSecret)
	notifyContextMut sync.RWMutex
)

type NetworkSecret struct {
	Secret  peer.NetworkSecret `json:"secret"`
	Network string             `json:"network"`
	Expire  time.Time          `json:"expire"`
}

func NotifyToken(state string, secret NetworkSecret) error {
	notifyContextMut.RLock()
	defer notifyContextMut.RUnlock()
	if ch, ok := notifyContext[state]; ok {
		ch <- secret
		return nil
	}
	return errors.New("state not found")
}

func HandleNotifyToken(w http.ResponseWriter, r *http.Request) {
	ch := make(chan NetworkSecret)
	state := r.URL.Query().Get("state")
	notifyContextMut.Lock()
	notifyContext[state] = ch
	notifyContextMut.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer close(ch)
	defer func() {
		notifyContextMut.Lock()
		defer notifyContextMut.Unlock()
		delete(notifyContext, state)
	}()
	select {
	case <-ctx.Done():
		return
	case token := <-ch:
		json.NewEncoder(w).Encode(token)
	}
}

func RedirectAuthURL(w http.ResponseWriter, r *http.Request) {
	providerName := path.Base(r.URL.Path)
	provider, ok := Provider(providerName)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "provider %s not found", providerName)
		return
	}
	authURL := provider.oAuthConfig.AuthCodeURL(r.URL.Query().Get("state"))
	http.Redirect(w, r, authURL, http.StatusFound)
}
