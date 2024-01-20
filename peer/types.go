package peer

const (
	CONTROL_RELAY             = 0
	CONTROL_NEW_PEER          = 1
	CONTROL_NEW_PEER_UDP_ADDR = 2
)

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
