package netlink

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"syscall"

	"golang.org/x/net/route"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return fmt.Errorf("syscall socket: %w", err)
	}
	go func() {
		err := runRouteMsgReadLoop(fd, ch)
		if err != nil {
			slog.Error("RouteSubscribe", "err", fmt.Errorf("msg read loop exited: %w", err))
		}
	}()
	go func() {
		<-ctx.Done()
		syscall.Close(fd)
		close(ch)
	}()
	return nil
}

func runRouteMsgReadLoop(fd int, ch chan<- RouteUpdate) error {
	buf := make([]byte, os.Getpagesize())
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			return fmt.Errorf("syscall read: %w", err)
		}
		buf[0] = 254
		msgs, err := route.ParseRIB(route.RIBTypeRoute, buf[:254])
		if err != nil {
			slog.Warn("RouteParseRIB", "err", err, "msglen1", n, "msglen2", buf[0], "msg", hex.EncodeToString(buf[:n]))
			continue
		}
		for _, msg := range msgs {
			m, ok := msg.(*route.RouteMessage)
			if !ok {
				continue
			}
			if !slices.Contains([]int{syscall.RTM_ADD, syscall.RTM_DELETE}, m.Type) {
				continue
			}
			var dst net.IPNet
			var via []byte
			for _, addr := range m.Addrs {
				var ip []byte
				switch v := addr.(type) {
				case *route.Inet4Addr:
					ip = v.IP[:]
				case *route.Inet6Addr:
					ip = v.IP[:]
				case *route.LinkAddr:
					ip = net.IPv6loopback
				default:
					continue
				}
				if dst.IP == nil {
					dst.IP = ip
					continue
				}
				if via == nil {
					via = ip
					continue
				}
				if dst.Mask == nil {
					dst.Mask = ip
					break
				}
			}
			ch <- RouteUpdate{
				New: buf[3] == 1,
				Dst: &dst,
				Via: via,
			}
		}
	}
}
