package link

import (
	"context"
	"net"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	cb, err := winipcfg.RegisterRouteChangeCallback(func(notificationType winipcfg.MibNotificationType, route *winipcfg.MibIPforwardRow2) {
		dst := route.DestinationPrefix.Prefix()
		ch <- RouteUpdate{
			Type: uint16(notificationType),
			Dst:  &net.IPNet{IP: net.IP(dst.Addr().AsSlice()), Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen())},
			Via:  net.IP(route.NextHop.Addr().AsSlice()),
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
