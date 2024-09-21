//go:build !windows

package iface

import (
	"net"
	"os"

	"github.com/rkonfj/peerguard/lru"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

func CreateFD(tunFD int, cfg Config) (*TunInterface, error) {
	err := unix.SetNonblock(tunFD, true)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(tunFD), "/dev/net/tun")
	device, err := tun.CreateTUNFromFile(file, 0)
	if err != nil {
		return nil, err
	}
	return &TunInterface{
		dev:     device,
		routing: lru.New[string, string](512),
		peers:   lru.New[string, net.Addr](1024),
	}, nil
}
