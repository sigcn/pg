package peermap

import (
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/rkonfj/peerguard/peer"
)

type Peermap struct {
	store  peer.SecretStore
	server *url.URL
}

func New(server *url.URL, store peer.SecretStore) (*Peermap, error) {
	if store == nil {
		return nil, errors.New("secret store is required")
	}
	if server == nil {
		return nil, errors.New("peermap server is required")
	}
	if !slices.Contains([]string{"https", "wss", "http", "ws"}, server.Scheme) {
		return nil, fmt.Errorf("invalid peermap server %s", server.String())
	}
	return &Peermap{
		store:  store,
		server: server,
	}, nil
}

func (s *Peermap) SecretStore() peer.SecretStore {
	return s.store
}

func (s *Peermap) String() string {
	return s.server.String()
}
