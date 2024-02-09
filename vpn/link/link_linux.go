//go:build linux

package link

import (
	"net"

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

	info.IPv4 = addr.IP.String()
	if addr.IP.To4() == nil {
		info.IPv6 = addr.IP.String()
	}

	if err := netlink.AddrAdd(link, addr); err != nil {
		return err
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	return nil
}

func AddRoute(device tun.Device, to *net.IPNet, via net.IP) error {
	return netlink.RouteAdd(&netlink.Route{
		Dst: to,
		Gw:  via,
	})
}

func DelRoute(device tun.Device, to *net.IPNet, via net.IP) error {
	return netlink.RouteDel(&netlink.Route{
		Dst: to,
		Gw:  via,
	})
}
