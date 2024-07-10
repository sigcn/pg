package peer

const (
	CONTROL_RELAY                 = 0
	CONTROL_NEW_PEER              = 1
	CONTROL_NEW_PEER_UDP_ADDR     = 2
	CONTROL_LEAD_DISCO            = 3
	CONTROL_UPDATE_NETWORK_SECRET = 20
	CONTROL_CONN                  = 30
)

type ID string

func (id ID) String() string {
	return string(id)
}

func (id ID) Network() string {
	return "p2p"
}

func (id ID) Len() byte {
	return byte(len(id))
}

func (id ID) Bytes() []byte {
	return []byte(id)
}
