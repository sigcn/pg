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
	file := os.NewFile(uintptr(tunFD), "")
	device, err := tun.CreateTUNFromFile(file, vpn.cfg.MTU)
	if err != nil {
		return err
	}
	return vpn.run(ctx, device)
}

func (vpn *VPN) RunUnmonitoredTUNFromFD(ctx context.Context, tunFD int) error {
	device, _, err := tun.CreateUnmonitoredTUNFromFD(tunFD)
	if err != nil {
		return err
	}
	return vpn.run(ctx, device)
}
