package disco

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"time"
)

type Server struct {
	Secret SecretStore
	URL    string
}

func NewServer(serverURL string, store SecretStore) (*Server, error) {
	if store == nil {
		return nil, errors.New("secret store is required")
	}

	server, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"https", "wss", "http", "ws"}, server.Scheme) {
		return nil, fmt.Errorf("unsupport server protocol: %s", server.String())
	}
	return &Server{
		Secret: store,
		URL:    serverURL,
	}, nil
}

type SecretStore interface {
	NetworkSecret() (NetworkSecret, error)
	UpdateNetworkSecret(NetworkSecret) error
}

type NetworkSecret struct {
	Secret  string    `json:"secret"`
	Network string    `json:"network"`
	Expire  time.Time `json:"expire"`
}

func (s NetworkSecret) Expired() bool {
	return time.Until(s.Expire) <= 0
}

func (s *NetworkSecret) NetworkSecret() (NetworkSecret, error) {
	return *s, nil
}

func (s *NetworkSecret) UpdateNetworkSecret(secret NetworkSecret) error {
	s.Secret = secret.Secret
	s.Network = secret.Network
	s.Expire = secret.Expire
	return nil
}

type SecretFile struct {
	FilePath string
}

func (s *SecretFile) NetworkSecret() (NetworkSecret, error) {
	f, err := os.Open(s.FilePath)
	if err != nil {
		return NetworkSecret{}, fmt.Errorf("file secret store(%s) open failed: %s", s.FilePath, err)
	}
	defer f.Close()
	var secret NetworkSecret
	if err = json.NewDecoder(f).Decode(&secret); err != nil {
		return secret, fmt.Errorf("file secret store(%s) decode failed: %w", s.FilePath, err)
	}
	return secret, nil
}

func (s *SecretFile) UpdateNetworkSecret(secret NetworkSecret) error {
	f, err := os.Create(s.FilePath)
	if err != nil {
		return fmt.Errorf("update network secret failed: %w", err)
	}
	if err := json.NewEncoder(f).Encode(secret); err != nil {
		return fmt.Errorf("save network secret failed: %w", err)
	}
	return f.Close()
}
