package oidc

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/langs"
)

var (
	notifyContext    = make(map[string]chan disco.NetworkSecret)
	notifyContextMut sync.RWMutex
)

func NotifyToken(state string, secret disco.NetworkSecret) error {
	notifyContextMut.RLock()
	defer notifyContextMut.RUnlock()
	if ch, ok := notifyContext[state]; ok {
		ch <- secret
		return nil
	}
	return errors.New("state not found")
}

func OIDCSecret(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		return
	}
	ch := make(chan disco.NetworkSecret)
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

func OIDCAuthURL(w http.ResponseWriter, r *http.Request) {
	provider, ok := Provider(r.PathValue("provider"))
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "provider %s not found", r.PathValue("provider"))
		return
	}
	authURL := provider.oAuthConfig.AuthCodeURL(r.URL.Query().Get("state"))
	http.Redirect(w, r, authURL, http.StatusFound)
}

func OIDCSelector(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<meta name="viewport" content="width=device-width, initial-scale=1.0">`)
	fmt.Fprintf(w, `<style>body{font-size: 18px;line-height: 26px;margin: 0;padding: 10px}</style>`)
	if len(providers) == 0 {
		fmt.Fprintf(w, `OIDC not configured yet`)
		return
	}
	fmt.Fprintf(w, `<b>Select an account to authenticate: </b><br />`)
	for provider := range providers {
		fmt.Fprintf(w, `<a href="//%s%s?state=%s">%s</a><br />`,
			cmp.Or(r.Header.Get("host"), r.Host), path.Join(r.URL.Path, provider), state, provider)
	}
}

func OIDCProviders(w http.ResponseWriter, r *http.Request) {
	langs.Data[[]string]{Data: slices.Sorted(maps.Keys(providers))}.MarshalTo(w)
}

type Grant func(email, state string) (disco.NetworkSecret, error)

type Authority struct {
	Grant Grant
}

func (a *Authority) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provider, ok := Provider(r.PathValue("provider"))
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
		w.Write([]byte("oidc: email is required"))
		return
	}
	secret, err := a.Grant(email, r.URL.Query().Get("state"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = NotifyToken(r.URL.Query().Get("state"), secret)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Write([]byte("ok"))
}
