//go:build !windows

package vpn

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

func (vpn *VPN) RunTunFD(ctx context.Context, tunFD int) error {
	err := unix.SetNonblock(tunFD, true)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(tunFD), "/dev/net/tun")
	device, err := tun.CreateTUNFromFile(file, 0)
	if err != nil {
		return err
	}
	return vpn.run(ctx, device)
}
