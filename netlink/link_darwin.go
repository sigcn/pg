package netlink

import (
	"errors"
	"net"
	"os/exec"
)

func SetupLink(ifName, cidr string) error {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	if ip.To4() == nil { // ipv6
		info.IPv6 = ip.String()
		if err := exec.Command("ifconfig", ifName, "inet6", "add", cidr).Run(); err != nil {
			return err
		}
	} else {
		info.IPv4 = ip.String()
		if err := exec.Command("ifconfig", ifName, "inet", cidr, ip.String(), "up").Run(); err != nil {
			return err
		}
	}
	return AddRoute(ifName, ipnet, nil)
}

func LinkByIndex(index int) (*Link, error) {
	return nil, errors.ErrUnsupported
}
