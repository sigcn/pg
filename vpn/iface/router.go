package iface

import (
	"net"
)

type RoutingTable interface {
	GetPeer(ip string) (net.Addr, bool)
	AddPeer(peer net.Addr, ipv4, ipv6 string)
	AddRoute(dst *net.IPNet, via net.IP)
	DelRoute(dst *net.IPNet, via net.IP)
}
