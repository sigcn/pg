package netlink

import "net"

type RouteUpdate struct {
	Type uint16 // 1 add 2 del
	Dst  *net.IPNet
	Via  net.IP
}
