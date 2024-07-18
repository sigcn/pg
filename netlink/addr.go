package netlink

import "net"

type AddrUpdate struct {
	New       bool
	Addr      net.IPNet
	LinkIndex int
}
