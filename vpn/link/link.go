//go:build !linux && !windows

package link

import (
	"golang.zx2c4.com/wireguard/tun"
)

func SetupLink(device tun.Device, cidr string) error {
	// noop
	return nil
}
