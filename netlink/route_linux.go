package netlink

import (
	"context"
	"log/slog"
	"net"
	"slices"

	"github.com/vishvananda/netlink"
)

// RouteSubscribe takes a chan down which notifications will be sent
// when routes are added or deleted. Rules are not currently supported
func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	rawChan := make(chan netlink.RouteUpdate)
	err := netlink.RouteSubscribe(rawChan, ctx.Done())
	if err != nil {
		return err
	}
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-rawChan:
				if e.Dst == nil || e.Gw == nil {
					continue
				}
				ru := RouteUpdate{
					Dst: e.Dst,
					Via: e.Gw,
				}
				if !slices.Contains([]uint16{24, 25}, e.Type) {
					slog.Debug("DropUnsupportRouteEvent")
					continue
				}
				if e.Type == 24 {
					ru.New = true
				}
				ch <- ru
			}
		}
	}()
	return nil
}

func AddRoute(_ string, to *net.IPNet, via net.IP) error {
	return netlink.RouteAdd(&netlink.Route{
		Dst: to,
		Gw:  via,
	})
}

func DelRoute(_ string, to *net.IPNet, via net.IP) error {
	return netlink.RouteDel(&netlink.Route{
		Dst: to,
		Gw:  via,
	})
}
