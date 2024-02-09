//go:build windows

package link

import (
	"fmt"
	"net"
	"os/exec"

	"golang.zx2c4.com/wireguard/tun"
)

func SetupLink(device tun.Device, cidr string) error {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	ifName, err := device.Name()
	if err != nil {
		return err
	}
	if ip.To4() == nil { // ipv6
		info.IPv6 = ip.String()
		return exec.Command("netsh", "interface", "ipv6", "add", "address", ifName, cidr).Run()
	}
	// ipv4
	info.IPv4 = ip.String()
	addrMask := fmt.Sprintf("%d.%d.%d.%d", ipnet.Mask[0], ipnet.Mask[1], ipnet.Mask[2], ipnet.Mask[3])
	return exec.Command("netsh", "interface", "ipv4", "set", "address", ifName, "static", ip.String(), addrMask).Run()
}

func AddRoute(device tun.Device, to *net.IPNet, via net.IP) error {
	ifName, err := device.Name()
	if err != nil {
		return err
	}
	if via.To4() == nil { // ipv6
		return exec.Command("netsh", "interface", "ipv6", "add", "route", to.String(), ifName, via.String()).Run()
	}
	// ipv4
	addrMask := fmt.Sprintf("%d.%d.%d.%d", to.Mask[0], to.Mask[1], to.Mask[2], to.Mask[3])
	return exec.Command("route", "add", to.IP.String(), "mask", addrMask, via.String()).Run()
}

func DelRoute(device tun.Device, to *net.IPNet, via net.IP) error {
	ifName, err := device.Name()
	if err != nil {
		return err
	}
	if via.To4() == nil { // ipv6
		return exec.Command("netsh", "interface", "ipv6", "delete", "route", to.String(), ifName, via.String()).Run()
	}
	return exec.Command("route", "delete", to.IP.String()).Run()
}
