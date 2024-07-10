package peer

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
)

type Peermap struct {
	store  SecretStore
	server *url.URL
}

func NewPeermap(server *url.URL, store SecretStore) (*Peermap, error) {
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

func NewPeermapURL(serverURL string, store SecretStore) (*Peermap, error) {
	sURL, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid peermap url: %w", err)
	}
	return NewPeermap(sURL, store)
}

func (s *Peermap) SecretStore() SecretStore {
	return s.store
}

func (s *Peermap) String() string {
	return s.server.String()
}
