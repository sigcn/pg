package gvisor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/sigcn/pg/vpn/nic"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

var (
	_ nic.NIC = (*GvisorCard)(nil)
)

type GvisorCard struct {
	Stack  *stack.Stack
	Config nic.Config

	initOnce  sync.Once
	closeOnce sync.Once

	ep    *channel.Endpoint
	nicID tcpip.NICID

	addr4 tcpip.Address
	addr6 tcpip.Address
}

func (g *GvisorCard) init() {
	g.initOnce.Do(func() {
		g.nicID = g.Stack.NextNICID()
		g.ep = channel.New(512, uint32(cmp.Or(g.Config.MTU, 1500)), "00:ab:00:00:00:00")
		g.Stack.CreateNIC(g.nicID, g.ep)

		if g.Config.IPv4 != "" {
			ip, ipnet, err := net.ParseCIDR(g.Config.IPv4)
			if err != nil {
				panic(fmt.Errorf("parse ipv4: %s: %w", g.Config.IPv4, err))
			}
			ones, _ := ipnet.Mask.Size()
			g.addr4 = tcpip.AddrFrom4([4]byte(ip.To4()))

			g.Stack.AddProtocolAddress(g.nicID, tcpip.ProtocolAddress{
				Protocol:          ipv4.ProtocolNumber,
				AddressWithPrefix: tcpip.AddressWithPrefix{Address: g.addr4, PrefixLen: ones},
			}, stack.AddressProperties{})

			subnet, err := tcpip.NewSubnet(tcpip.AddrFrom4([4]byte(ipnet.IP.To4())), tcpip.MaskFromBytes(ipnet.Mask))
			g.Stack.AddRoute(tcpip.Route{Destination: subnet, NIC: g.nicID})
		}
		if g.Config.IPv6 != "" {
			ip, ipnet, err := net.ParseCIDR(g.Config.IPv6)
			if err != nil {
				panic(fmt.Errorf("parse ipv6: %s: %w", g.Config.IPv6, err))
			}
			ones, _ := ipnet.Mask.Size()
			g.addr6 = tcpip.AddrFrom16([16]byte(ip))

			g.Stack.AddProtocolAddress(g.nicID, tcpip.ProtocolAddress{
				Protocol:          ipv6.ProtocolNumber,
				AddressWithPrefix: tcpip.AddressWithPrefix{Address: g.addr6, PrefixLen: ones},
			}, stack.AddressProperties{})

			subnet, err := tcpip.NewSubnet(tcpip.AddrFrom16([16]byte(ipnet.IP)), tcpip.MaskFromBytes(ipnet.Mask))
			g.Stack.AddRoute(tcpip.Route{Destination: subnet, NIC: g.nicID})
		}
	})
}

func (g *GvisorCard) Write(p *nic.Packet) error {
	g.init()
	var ipVer tcpip.NetworkProtocolNumber
	if p.Ver() == 4 {
		ipVer = ipv4.ProtocolNumber
	} else {
		ipVer = ipv6.ProtocolNumber
	}
	g.ep.InjectInbound(ipVer, stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p.AsBytes())}))
	return nil
}

func (g *GvisorCard) Read() (*nic.Packet, error) {
	g.init()
	buf := g.ep.ReadContext(context.TODO())
	if buf == nil {
		return nil, net.ErrClosed
	}
	defer buf.DecRef()
	pkt := nic.IPPacketPool.Get()
	pkt.Write(buf.ToView().AsSlice())
	return pkt, nil
}

func (g *GvisorCard) Close() error {
	g.closeOnce.Do(func() {
		if g.Stack != nil {
			if g.addr4.Len() > 0 {
				g.Stack.RemoveAddress(g.nicID, g.addr4)
			}
			if g.addr6.Len() > 0 {
				g.Stack.RemoveAddress(g.nicID, g.addr6)
			}
			g.Stack.RemoveRoutes(func(r tcpip.Route) bool {
				return r.NIC == g.nicID
			})
		}
		if g.ep != nil {
			g.ep.Close()
		}
	})
	return nil
}

func (g *GvisorCard) Listen(ctx context.Context, network string, port uint16) (l net.Listener, err error) {
	g.init()
	if !strings.HasPrefix(network, "tcp") && !strings.HasPrefix(network, "udp") {
		return nil, errors.New("only tcp/udp is supported")
	}

	if network == "tcp4" {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr4, Port: port}
		return gonet.ListenTCP(g.Stack, addr, ipv4.ProtocolNumber)
	}

	if network == "tcp6" {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr6, Port: port}
		return gonet.ListenTCP(g.Stack, addr, ipv6.ProtocolNumber)
	}

	if network == "udp4" {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr4, Port: port}
		return &udpListener{s: g.Stack, addr: addr}, nil
	}

	if network == "udp6" {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr6, Port: port}
		return &udpListener{s: g.Stack, addr: addr}, nil
	}

	var listeners []net.Listener
	defer func() {
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
		}
	}()

	if network == "udp" {
		if g.addr4.Len() > 0 {
			addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr4, Port: port}
			listeners = append(listeners, &udpListener{s: g.Stack, addr: addr})
		}
		if g.addr6.Len() > 0 {
			addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr6, Port: port}
			listeners = append(listeners, &udpListener{s: g.Stack, addr: addr})
		}
		return &combinedListeners{listeners: listeners}, nil
	}

	if g.addr4.Len() > 0 {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr4, Port: port}
		l, err := gonet.ListenTCP(g.Stack, addr, ipv4.ProtocolNumber)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
	}
	if g.addr6.Len() > 0 {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr6, Port: port}
		l, err := gonet.ListenTCP(g.Stack, addr, ipv6.ProtocolNumber)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
	}
	return &combinedListeners{listeners: listeners}, nil
}

var _ net.Listener = (*combinedListeners)(nil)

type combinedListeners struct {
	listeners []net.Listener

	closeChan chan struct{}
	connChan  chan net.Conn
	errChan   chan error
	initOnce  sync.Once
	closeOnce sync.Once
}

func (l *combinedListeners) init() {
	l.initOnce.Do(func() {
		l.closeChan = make(chan struct{})
		l.connChan = make(chan net.Conn)
		l.errChan = make(chan error)
		for _, listener := range l.listeners {
			go l.accept(listener)
		}
	})
}

func (l *combinedListeners) accept(listener net.Listener) {
	for {
		c, err := listener.Accept()
		if err != nil {
			select {
			case <-l.closeChan:
				return
			default:
			}
			l.errChan <- err
			return
		}
		l.connChan <- c
	}
}

func (l *combinedListeners) Accept() (net.Conn, error) {
	l.init()
	select {
	case <-l.closeChan:
		return nil, net.ErrClosed
	case err := <-l.errChan:
		return nil, err
	case c := <-l.connChan:
		return c, nil
	}
}

func (l *combinedListeners) Close() error {
	l.closeOnce.Do(func() {
		if l.closeChan != nil {
			close(l.closeChan)
		}
		if l.connChan != nil {
			close(l.connChan)
		}
		if l.errChan != nil {
			close(l.errChan)
		}
		for _, listener := range l.listeners {
			listener.Close()
		}
	})
	return nil
}

func (l *combinedListeners) Addr() net.Addr {
	if len(l.listeners) == 0 {
		return nil
	}
	return l.listeners[0].Addr()
}
