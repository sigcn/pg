package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rkonfj/peerguard/peer"
)

var (
	notifyContext    = make(map[string]chan peer.NetworkSecret)
	notifyContextMut sync.RWMutex
)

func NotifyToken(state string, secret peer.NetworkSecret) error {
	notifyContextMut.RLock()
	defer notifyContextMut.RUnlock()
	if ch, ok := notifyContext[state]; ok {
		ch <- secret
		return nil
	}
	return errors.New("state not found")
}

func HandleNotifyToken(w http.ResponseWriter, r *http.Request) {
	ch := make(chan peer.NetworkSecret)
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
	provider, ok := Provider(r.PathValue("provider"))
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "provider %s not found", r.PathValue("provider"))
		return
	}
	authURL := provider.oAuthConfig.AuthCodeURL(r.URL.Query().Get("state"))
	http.Redirect(w, r, authURL, http.StatusFound)
}
