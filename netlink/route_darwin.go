package netlink

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"

	"golang.org/x/net/route"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return fmt.Errorf("syscall socket: %w", err)
	}
	go func() {
		err := runReadLoop(fd, ch)
		if err != nil {
			slog.Error("SyscallReadExited", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		syscall.Close(fd)
		close(ch)
	}()
	return nil
}

func runReadLoop(fd int, ch chan<- RouteUpdate) error {
	buf := make([]byte, os.Getpagesize())
	for {
		_, err := syscall.Read(fd, buf)
		if err != nil {
			return fmt.Errorf("syscall read: %w", err)
		}
		msg := buf[:176]
		msg[0] = 176
		msgs, err := route.ParseRIB(route.RIBTypeRoute, msg)
		if err != nil {
			return fmt.Errorf("route parseRIB: %w", err)
		}
		for _, msg := range msgs {
			m, ok := msg.(*route.RouteMessage)
			if !ok {
				continue
			}
			addrs := m.Addrs
			if len(addrs) < 3 {
				continue
			}

			var dst, via, mask []byte

			if ip, ok := addrs[0].(*route.Inet4Addr); ok {
				dst = ip.IP[:]
				addr, ok := addrs[1].(*route.Inet4Addr)
				if !ok {
					continue
				}
				via = addr.IP[:]
				addr, ok = addrs[2].(*route.Inet4Addr)
				if !ok {
					continue
				}
				mask = addr.IP[:]
			} else if ip, ok := addrs[0].(*route.Inet6Addr); ok {
				dst = ip.IP[:]
				addr, ok := addrs[1].(*route.Inet6Addr)
				if !ok {
					continue
				}
				via = addr.IP[:]
				addr, ok = addrs[2].(*route.Inet6Addr)
				if !ok {
					continue
				}
				mask = addr.IP[:]
			} else {
				continue
			}
			ch <- RouteUpdate{
				Type: uint16(buf[3]),
				Dst: &net.IPNet{
					IP:   dst,
					Mask: mask[:],
				},
				Via: via,
			}
		}
	}
}
