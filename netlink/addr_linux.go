package netlink

import (
	"context"

	"github.com/vishvananda/netlink"
)

func AddrSubscribe(ctx context.Context, ch chan<- AddrUpdate) error {
	rawChan := make(chan netlink.AddrUpdate)
	err := netlink.AddrSubscribe(rawChan, ctx.Done())
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
				ch <- AddrUpdate{
					New:       e.NewAddr,
					Addr:      e.LinkAddress,
					LinkIndex: e.LinkIndex,
				}
			}
		}
	}()
	return nil
}
