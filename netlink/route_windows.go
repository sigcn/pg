package netlink

import (
	"context"
	"fmt"
	"net"
	"os/exec"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	cb, err := winipcfg.RegisterRouteChangeCallback(func(notificationType winipcfg.MibNotificationType, route *winipcfg.MibIPforwardRow2) {
		dst := route.DestinationPrefix.Prefix()
		ch <- RouteUpdate{
			New: notificationType == 1,
			Dst: &net.IPNet{IP: net.IP(dst.Addr().AsSlice()), Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen())},
			Via: net.IP(route.NextHop.Addr().AsSlice()),
		}
	})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		cb.Unregister()
		close(ch)
	}()
	return nil
}
func AddRoute(ifName string, to *net.IPNet, via net.IP) error {
	if via.To4() == nil { // ipv6
		return exec.Command("netsh", "interface", "ipv6", "add", "route", to.String(), ifName, via.String()).Run()
	}
	// ipv4
	addrMask := fmt.Sprintf("%d.%d.%d.%d", to.Mask[0], to.Mask[1], to.Mask[2], to.Mask[3])
	return exec.Command("route", "add", to.IP.String(), "mask", addrMask, via.String()).Run()
}

func DelRoute(ifName string, to *net.IPNet, via net.IP) error {
	if via.To4() == nil { // ipv6
		return exec.Command("netsh", "interface", "ipv6", "delete", "route", to.String(), ifName, via.String()).Run()
	}
	return exec.Command("route", "delete", to.IP.String()).Run()
}
