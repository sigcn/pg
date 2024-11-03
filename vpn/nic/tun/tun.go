package tun

import (
	"fmt"
	"sync"

	"github.com/sigcn/pg/netlink"
	"github.com/sigcn/pg/vpn/nic"
	"golang.zx2c4.com/wireguard/tun"
)

var (
	_ nic.NIC = (*TUNIC)(nil)
)

// TUNIC implements nic.NIC use os TUN device
type TUNIC struct {
	dev    tun.Device
	mtu    int
	ifName string

	readBufs  [][]byte
	readSizes []int
	readInit  sync.Once

	readTotal, read int
}

func Create(tunName string, cfg nic.Config) (*TUNIC, error) {
	device, err := tun.CreateTUN(tunName, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("create tun device (%s): %w", tunName, err)
	}
	deviceName, err := device.Name()
	if err != nil {
		return nil, fmt.Errorf("get tun device name: %w", err)
	}
	if cfg.IPv4 != "" {
		netlink.SetupLink(deviceName, cfg.IPv4)
	}
	if cfg.IPv6 != "" {
		netlink.SetupLink(deviceName, cfg.IPv6)
	}
	return &TUNIC{dev: device, ifName: tunName, mtu: cfg.MTU}, nil
}

// Read read ip packet from nic. no concurrency support
func (tun *TUNIC) Read() (*nic.Packet, error) {
	tun.readInit.Do(func() {
		tun.readBufs = make([][]byte, tun.dev.BatchSize())
		tun.readSizes = make([]int, tun.dev.BatchSize())
		for i := range tun.readBufs {
			tun.readBufs[i] = make([]byte, tun.mtu+nic.IPPacketOffset+40)
		}
	})
	if tun.read < tun.readTotal {
		pkt := nic.IPPacketPool.Get()
		tun.read++
		return pkt, pkt.Write(tun.readBufs[tun.read][nic.IPPacketOffset : tun.readSizes[tun.read]+nic.IPPacketOffset])
	}
	n, err := tun.dev.Read(tun.readBufs, tun.readSizes, nic.IPPacketOffset)
	if err != nil {
		return nil, err
	}
	tun.readTotal = n - 1
	tun.read = 0

	pkt := nic.IPPacketPool.Get()
	return pkt, pkt.Write(tun.readBufs[tun.read][nic.IPPacketOffset : tun.readSizes[tun.read]+nic.IPPacketOffset])
}

// Write write ip packet to nic
func (tun *TUNIC) Write(p *nic.Packet) error {
	_, err := tun.dev.Write([][]byte{p.Bytes(0)}, nic.IPPacketOffset)
	return err
}

func (tun *TUNIC) Close() error {
	return tun.dev.Close()
}
