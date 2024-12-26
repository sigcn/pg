package rootless

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/sigcn/pg/vpn/nic/gvisor"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

func CreateGvisorStack() *stack.Stack {
	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
			icmp.NewProtocol4,
			icmp.NewProtocol6,
		},
	})

	tcpRecoveryOpt := tcpip.TCPRecovery(0)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpRecoveryOpt)
	return s
}

type ForwardEngine struct {
	GvisorCard *gvisor.GvisorCard
	Forwards   []*url.URL
}

func (g *ForwardEngine) Start(ctx context.Context, wg *sync.WaitGroup) (err error) {
	var forwardJobs []func()
	var listeners []net.Listener
	closeEngine := func() {
		for _, l := range listeners {
			l.Close()
		}
		g.GvisorCard.Close()
		g.GvisorCard.Stack.Close()
	}

	defer func() {
		if err != nil {
			closeEngine()
		}
	}()
	for _, forward := range g.Forwards {
		_, port, err := net.SplitHostPort(forward.Host)
		if err != nil {
			return fmt.Errorf("parse forward: %w", err)
		}
		portNum, _ := strconv.ParseInt(port, 10, 64)
		l, err := g.GvisorCard.Listen(ctx, forward.Scheme, uint16(portNum))
		if err != nil {
			return fmt.Errorf("gvisor listen: %w", err)
		}
		listeners = append(listeners, l)
		slog.Info("[gVisor] Forwarding", "pg_addr", l.Addr(), "to_addr", forward)
		forwardJobs = append(forwardJobs, func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				slog.Info("[gVisor] AcceptConn", "pg_addr", c.LocalAddr().String(), "from", c.RemoteAddr(), "forward_to", forward)
				c1, err := net.Dial(forward.Scheme, forward.Host)
				if err != nil {
					slog.Error("[gVisor] Dial backend", "backend", forward, "err", err)
					continue
				}
				go relay(c, c1)
			}
		})
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		closeEngine()
	}()
	for _, job := range forwardJobs {
		go job()
	}
	return nil
}

func relay(c1, c2 net.Conn) {
	defer c1.Close()
	defer c2.Close()
	go io.Copy(c1, c2)
	io.Copy(c2, c1)
}
