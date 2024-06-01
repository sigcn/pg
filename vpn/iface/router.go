package iface

import (
	"net"
)

type RoutingTable interface {
	GetPeer(ip string) (net.Addr, bool)
	AddPeer(ipv4, ipv6 string, peer net.Addr)
	AddRoute(cidr *net.IPNet, via net.IP)
}
