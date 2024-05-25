package vpn

type InboundHandler interface {
	Name() string
	In([]byte) []byte
}

type OutboundHandler interface {
	Name() string
	Out([]byte) []byte
}
