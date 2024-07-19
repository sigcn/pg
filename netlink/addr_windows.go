package netlink

import (
	"context"
	"net"
	"slices"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func AddrSubscribe(ctx context.Context, ch chan<- AddrUpdate) error {
	cb, err := winipcfg.RegisterUnicastAddressChangeCallback(func(notificationType winipcfg.MibNotificationType, addr *winipcfg.MibUnicastIPAddressRow) {
		if !slices.Contains([]winipcfg.MibNotificationType{winipcfg.MibAddInstance, winipcfg.MibDeleteInstance}, notificationType) {
			return
		}
		ch <- AddrUpdate{
			New:       notificationType == winipcfg.MibAddInstance,
			Addr:      net.IPNet{IP: net.ParseIP(addr.Address.Addr().String())},
			LinkIndex: int(addr.InterfaceIndex),
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
