//go:build linux

package link

import (
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/tun"
)

func SetupLink(device tun.Device, cidr string) error {
	ifName, err := device.Name()
	if err != nil {
		return err
	}

	link, err := netlink.LinkByName(ifName)
	if err != nil {
		return err
	}

	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return err
	}

	if err := netlink.AddrAdd(link, addr); err != nil {
		return err
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	return nil
}
