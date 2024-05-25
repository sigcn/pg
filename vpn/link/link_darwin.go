package link

import (
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

func AddRoute(ifName string, to *net.IPNet, _ net.IP) error {
	if to.IP.To4() == nil { // ipv6
		return exec.Command("route", "-qn", "add", "-inet6", to.String(), "-iface", ifName).Run()
	}
	return exec.Command("route", "-qn", "add", "-inet", to.String(), "-iface", ifName).Run()
}

func DelRoute(_ string, to *net.IPNet, _ net.IP) error {
	if to.IP.To4() == nil { // ipv6
		return exec.Command("route", "-qn", "delete", "-inet6", to.String()).Run()
	}
	return exec.Command("route", "-qn", "delete", "-inet", to.String()).Run()
}
