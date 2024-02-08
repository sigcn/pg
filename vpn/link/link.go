//go:build !linux && !windows

package link

import (
	"net"

	"golang.zx2c4.com/wireguard/tun"
)

func SetupLink(device tun.Device, cidr string) error {
	// noop
	return nil
}

func AddRoute(device tun.Device, to *net.IPNet, via net.IP) error {
	// noop
	return nil
}

func DelRoute(device tun.Device, to *net.IPNet, via net.IP) error {
	// noop
	return nil
}
