package peer

import "encoding/json"

const (
	CONTROL_RELAY             = 0
	CONTROL_NEW_PEER          = 1
	CONTROL_NEW_PEER_UDP_ADDR = 2
)

type NetworkSecret string

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
