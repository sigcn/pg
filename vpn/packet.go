package vpn

import "github.com/sigcn/pg/vpn/nic"

type InboundHandler interface {
	Name() string
	In(*nic.Packet) *nic.Packet
}

type OutboundHandler interface {
	Name() string
	Out(*nic.Packet) *nic.Packet
}
