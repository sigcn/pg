//go:build !windows

package tun

import (
	"os"

	"github.com/sigcn/pg/vpn/nic"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

func CreateFD(tunFD int, cfg nic.Config) (*TUNIC, error) {
	err := unix.SetNonblock(tunFD, true)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(tunFD), "/dev/net/tun")
	device, err := tun.CreateTUNFromFile(file, 0)
	if err != nil {
		return nil, err
	}
	return &TUNIC{dev: device, mtu: cfg.MTU}, nil
}
