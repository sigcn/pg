package peer

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const (
	CONTROL_RELAY                 = 0
	CONTROL_NEW_PEER              = 1
	CONTROL_NEW_PEER_UDP_ADDR     = 2
	CONTROL_LEAD_DISCO            = 3
	CONTROL_UPDATE_NETWORK_SECRET = 20
)

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

type FileSecretStore struct {
	StoreFilePath string
}

func (s *FileSecretStore) NetworkSecret() (NetworkSecret, error) {
	f, err := os.Open(s.StoreFilePath)
	if err != nil {
		return NetworkSecret{}, fmt.Errorf("file secret store(%s) open failed: %s", s.StoreFilePath, err)
	}
	defer f.Close()
	var secret NetworkSecret
	if err = json.NewDecoder(f).Decode(&secret); err != nil {
		return secret, fmt.Errorf("file secret store(%s) decode failed: %w", s.StoreFilePath, err)
	}
	return secret, nil
}

func (s *FileSecretStore) UpdateNetworkSecret(secret NetworkSecret) error {
	f, err := os.Create(s.StoreFilePath)
	if err != nil {
		return fmt.Errorf("update network secret failed: %w", err)
	}
	if err := json.NewEncoder(f).Encode(secret); err != nil {
		return fmt.Errorf("save network secret failed: %w", err)
	}
	return f.Close()
}

type NetworkID string

func (id NetworkID) String() string {
	return string(id)
}

type PeerID string

func (id PeerID) String() string {
	return string(id)
}

func (id PeerID) Network() string {
	return "p2p"
}

func (id PeerID) Len() byte {
	return byte(len(id))
}

func (id PeerID) Bytes() []byte {
	return []byte(id)
}

type PeermapCluster []string

type Metadata struct {
	SilenceMode bool           `json:"silenceMode"`
	Alias1      string         `json:"alias1"`
	Alias2      string         `json:"alias2"`
	Extra       map[string]any `json:"extra"`
}

func (meta Metadata) MustMarshalJSON() []byte {
	b, _ := json.Marshal(meta)
	return b
}
