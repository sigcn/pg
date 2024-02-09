//go:build !linux && !windows && !darwin

package link

import (
	"net"

	"golang.zx2c4.com/wireguard/tun"
)

func SetupLink(tun.Device, string) error {
	// noop
	return nil
}

func AddRoute(tun.Device, *net.IPNet, net.IP) error {
	// noop
	return nil
}

func DelRoute(tun.Device, *net.IPNet, net.IP) error {
	// noop
	return nil
}
