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

func AddrSubscribe(ctx context.Context, ch chan<- AddrUpdate) error {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return fmt.Errorf("syscall socket: %w", err)
	}
	go func() {
		err := runAddrMsgReadLoop(fd, ch)
		if err != nil {
			slog.Error("AddrSubscribe", "err", fmt.Errorf("msg read loop exited: %w", err))
		}
	}()
	go func() {
		<-ctx.Done()
		syscall.Close(fd)
		close(ch)
	}()
	return nil
}

func runAddrMsgReadLoop(fd int, ch chan<- AddrUpdate) error {
	buf := make([]byte, os.Getpagesize())
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			return fmt.Errorf("syscall read: %w", err)
		}
		buf[0] = 254
		msgs, err := route.ParseRIB(route.RIBTypeRoute, buf[:254])
		if err != nil {
			slog.Warn("RouteParseRIB", "err", err, "msglen", n, "msg", hex.EncodeToString(buf[:n]))
			continue
		}
		for _, msg := range msgs {
			m, ok := msg.(*route.InterfaceAddrMessage)
			if !ok {
				continue
			}
			if !slices.Contains([]int{syscall.RTM_NEWADDR, syscall.RTM_DELADDR}, m.Type) {
				continue
			}
			ipnet := net.IPNet{}
			for _, addr := range m.Addrs {
				var ip []byte
				switch v := addr.(type) {
				case *route.Inet4Addr:
					ip = v.IP[:]
				case *route.Inet6Addr:
					ip = v.IP[:]
				default:
					continue
				}
				if ipnet.Mask == nil {
					ipnet.Mask = ip
					continue
				}
				if ipnet.IP == nil {
					ipnet.IP = ip
					break
				}
			}
			ch <- AddrUpdate{
				New:       m.Type == syscall.RTM_NEWADDR,
				Addr:      ipnet,
				LinkIndex: m.Index,
			}
		}
	}
}
