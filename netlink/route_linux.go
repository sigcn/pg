package netlink

import (
	"context"
	"log/slog"

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
				if e.Type == 24 {
					ru.Type = 1
				} else if e.Type == 25 {
					ru.Type = 2
				} else {
					slog.Debug("DropUnsupportRouteEvent")
					continue
				}
				ch <- ru
			}
		}
	}()
	return nil
}
