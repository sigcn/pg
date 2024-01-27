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
	ctx := "ipv4"
	if ip.To4() == nil {
		ctx = "ipv6"
	}
	addrMask := fmt.Sprintf("%d.%d.%d.%d", ipnet.Mask[0], ipnet.Mask[1], ipnet.Mask[2], ipnet.Mask[3])
	return exec.Command("netsh", "interface", ctx, "set", "address", ifName, "static", ip.String(), addrMask).Run()
}
