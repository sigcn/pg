package peer

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
