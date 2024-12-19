package gvisor

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/sigcn/pg/vpn/nic"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
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
	g.init()
	g.closeOnce.Do(func() {
		if g.addr4.Len() > 0 {
			g.Stack.RemoveAddress(g.nicID, g.addr4)
		}
		if g.addr6.Len() > 0 {
			g.Stack.RemoveAddress(g.nicID, g.addr6)
		}
		g.Stack.RemoveRoutes(func(r tcpip.Route) bool {
			return r.NIC == g.nicID
		})
		g.ep.Close()
	})
	return nil
}
