package netlink

import "net"

type RouteUpdate struct {
	New bool
	Dst *net.IPNet
	Via net.IP
}
