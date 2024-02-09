package link

import (
	"net"
	"os/exec"

	"golang.zx2c4.com/wireguard/tun"
)

func SetupLink(device tun.Device, cidr string) error {
	ifName, err := device.Name()
	if err != nil {
		return err
	}
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	if err = AddRoute(device, ipnet, nil); err != nil {
		return err
	}
	if ip.To4() == nil { // ipv6
		info.IPv6 = ip.String()
		return exec.Command("ifconfig", ifName, "inet6", "add", cidr).Run()
	}
	info.IPv4 = ip.String()
	return exec.Command("ifconfig", ifName, "inet", cidr, ip.String(), "up").Run()
}

func AddRoute(device tun.Device, to *net.IPNet, _ net.IP) error {
	ifName, err := device.Name()
	if err != nil {
		return err
	}
	if to.IP.To4() == nil { // ipv6
		return exec.Command("route", "-qn", "add", "-inet6", to.String(), "-iface", ifName).Run()
	}
	return exec.Command("route", "-qn", "add", "-inet", to.String(), "-iface", ifName).Run()
}

func DelRoute(_ tun.Device, to *net.IPNet, _ net.IP) error {
	if to.IP.To4() == nil { // ipv6
		return exec.Command("route", "-qn", "delete", "-inet6", to.String()).Run()
	}
	return exec.Command("route", "-qn", "delete", "-inet", to.String()).Run()
}
