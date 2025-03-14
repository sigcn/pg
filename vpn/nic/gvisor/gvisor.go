package gvisor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	N "github.com/sigcn/pg/net"
	"github.com/sigcn/pg/vpn/nic"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
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

			subnet, _ := tcpip.NewSubnet(tcpip.AddrFrom4([4]byte(ipnet.IP.To4())), tcpip.MaskFromBytes(ipnet.Mask))
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

			subnet, _ := tcpip.NewSubnet(tcpip.AddrFrom16([16]byte(ipnet.IP)), tcpip.MaskFromBytes(ipnet.Mask))
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
	return nic.GetPacket(buf.ToView().AsSlice()), nil
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

func (g *GvisorCard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	g.init()
	if !strings.HasPrefix(network, "tcp") && !strings.HasPrefix(network, "udp") {
		return nil, errors.New("only tcp/udp is supported")
	}

	if strings.HasPrefix(network, "tcp") {
		tcpAddr, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return nil, err
		}
		var add tcpip.Address
		var protocol tcpip.NetworkProtocolNumber
		if tcpAddr.IP.To4() != nil {
			add = tcpip.AddrFrom4(tcpAddr.AddrPort().Addr().As4())
			protocol = ipv4.ProtocolNumber
		} else {
			add = tcpip.AddrFrom16(tcpAddr.AddrPort().Addr().As16())
			protocol = ipv6.ProtocolNumber
		}
		addr := tcpip.FullAddress{
			NIC:  g.nicID,
			Addr: add,
			Port: tcpAddr.AddrPort().Port()}
		return gonet.DialContextTCP(ctx, g.Stack, addr, protocol)
	}

	if strings.HasPrefix(network, "udp") {
		udpAddr, err := net.ResolveUDPAddr(network, address)
		if err != nil {
			return nil, err
		}
		var add tcpip.Address
		var protocol tcpip.NetworkProtocolNumber
		if udpAddr.IP.To4() != nil {
			add = tcpip.AddrFrom4(udpAddr.AddrPort().Addr().As4())
			protocol = ipv4.ProtocolNumber
		} else {
			add = tcpip.AddrFrom16(udpAddr.AddrPort().Addr().As16())
			protocol = ipv6.ProtocolNumber
		}
		addr := &tcpip.FullAddress{
			NIC:  g.nicID,
			Addr: add,
			Port: udpAddr.AddrPort().Port()}
		return gonet.DialUDP(g.Stack, nil, addr, protocol)
	}
	return nil, nil
}

func (g *GvisorCard) listenUDP(addr tcpip.FullAddress) (net.PacketConn, error) {
	var wq waiter.Queue
	var ep tcpip.Endpoint
	var err tcpip.Error
	if net.IP(addr.Addr.AsSlice()).To4() != nil {
		ep, err = g.Stack.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	} else {
		ep, err = g.Stack.NewEndpoint(udp.ProtocolNumber, ipv6.ProtocolNumber, &wq)
	}
	if err != nil {
		return nil, errors.New(err.String())
	}
	err = ep.Bind(addr)
	if err != nil {
		return nil, errors.New(err.String())
	}
	return gonet.NewUDPConn(&wq, ep), nil
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
		pc, err := g.listenUDP(addr)
		if err != nil {
			return nil, err
		}
		return &N.UDPListener{PacketConn: pc}, nil
	}

	if network == "udp6" {
		addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr6, Port: port}
		pc, err := g.listenUDP(addr)
		if err != nil {
			return nil, err
		}
		return &N.UDPListener{PacketConn: pc}, nil
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
			pc, err := g.listenUDP(addr)
			if err != nil {
				return nil, err
			}
			listeners = append(listeners, &N.UDPListener{PacketConn: pc})
		}
		if g.addr6.Len() > 0 {
			addr := tcpip.FullAddress{NIC: g.nicID, Addr: g.addr6, Port: port}
			pc, err := g.listenUDP(addr)
			if err != nil {
				return nil, err
			}
			listeners = append(listeners, &N.UDPListener{PacketConn: pc})
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
