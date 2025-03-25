package vpn

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/sigcn/pg/netlink"
	"github.com/sigcn/pg/vpn/nic"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type Config struct {
	MTU              int
	InboundHandlers  []InboundHandler
	OutboundHandlers []OutboundHandler
	OnRouteAdd       func(net.IPNet, net.IP)
	OnRouteRemove    func(net.IPNet, net.IP)
}

type VPN struct {
	nic      *nic.VirtualNIC
	cfg      Config
	outbound chan *nic.Packet
	inbound  chan *nic.Packet
}

func New(cfg Config) *VPN {
	if cfg.MTU > 0 {
		nic.SetPacketPool(&nic.PacketPool{MTU: cfg.MTU})
	}
	return &VPN{
		cfg:      cfg,
		outbound: make(chan *nic.Packet, 512),
		inbound:  make(chan *nic.Packet, 512),
	}
}

func (vpn *VPN) Run(ctx context.Context, nic *nic.VirtualNIC, packetConn net.PacketConn) error {
	vpn.nic = nic
	var wg sync.WaitGroup
	wg.Add(5)
	go vpn.routingTableUpdate(ctx, &wg)
	go vpn.nicRead(&wg, nic)
	go vpn.nicWrite(&wg, nic)
	go vpn.packetConnRead(&wg, packetConn)
	go vpn.packetConnWrite(&wg, packetConn)

	<-ctx.Done()
	packetConn.Close()
	nic.Close()
	close(vpn.inbound)
	close(vpn.outbound)
	wg.Wait()
	return nil
}

// routingTableUpdate subscribe system route changes and update nic route table
func (vpn *VPN) routingTableUpdate(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ch := make(chan netlink.RouteUpdate)
	if err := netlink.RouteSubscribe(ctx, ch); err != nil {
		slog.Debug("RouteSubscribe", "err", err)
		return
	}
	for r := range ch {
		if r.New {
			if vpn.nic.AddRoute(r.Dst, r.Via) && vpn.cfg.OnRouteAdd != nil {
				vpn.cfg.OnRouteAdd(*r.Dst, r.Via)
			}
			continue
		}
		if vpn.nic.DelRoute(r.Dst, r.Via) && vpn.cfg.OnRouteRemove != nil {
			vpn.cfg.OnRouteRemove(*r.Dst, r.Via)
		}
	}
}

// nicRead read ip packet from nic device and send to outbound channel
func (vpn *VPN) nicRead(wg *sync.WaitGroup, nic *nic.VirtualNIC) {
	defer wg.Done()
	for {
		packet, err := nic.Read()
		if err != nil {
			if errors.Is(err, os.ErrClosed) || errors.Is(err, net.ErrClosed) {
				return
			}
			panic(err)
		}
		vpn.outbound <- packet
	}
}

// nicWrite read ip packet from inbound channel and write to nic device
func (vpn *VPN) nicWrite(wg *sync.WaitGroup, vnic *nic.VirtualNIC) {
	defer wg.Done()
	handle := func(pkt *nic.Packet) *nic.Packet {
		for _, in := range vpn.cfg.InboundHandlers {
			if pkt = in.In(pkt); pkt == nil {
				slog.Debug("DropInbound", "handler", in.Name())
				return nil
			}
		}
		return pkt
	}
	for packet := range vpn.inbound {
		if packet = handle(packet); packet == nil {
			nic.RecyclePacket(packet)
			continue
		}
		err := vnic.Write(packet)
		if err != nil {
			slog.Debug("WriteTo nic device", "err", err.Error())
		}
		nic.RecyclePacket(packet)
	}
}

// packetConnRead read ip packet from packet conn and send to inbound channel
func (vpn *VPN) packetConnRead(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	buf := make([]byte, cmp.Or(vpn.cfg.MTU, (2<<15)-8-40-40)+40)
	for {
		n, _, err := packetConn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			panic(err)
		}
		vpn.inbound <- nic.GetPacket(buf[:n])
	}
}

// packetConnWrite read ip packet from outbound channel and write to packet conn
func (vpn *VPN) packetConnWrite(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	sendPacketToPeer := func(packet *nic.Packet, srcIP, dstIP net.IP) {
		defer nic.RecyclePacket(packet)
		if dstIP.IsMulticast() {
			slog.Log(context.Background(), -10, "DropMulticastIP", "dst", dstIP)
			return
		}
		if peer, ok := vpn.nic.GetPeer(dstIP.String()); ok {
			_, err := packetConn.WriteTo(packet.AsBytes(), peer)
			if err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Error("WriteTo packet conn", "peer", peer, "err", err)
			}
			return
		}
		// reject with icmp-host-unreachable
		vpn.inbound <- nic.GetPacket(ICMPHostUnreachable(dstIP, srcIP, packet.AsBytes()))
	}
	handle := func(pkt *nic.Packet) *nic.Packet {
		for _, out := range vpn.cfg.OutboundHandlers {
			if pkt = out.Out(pkt); pkt == nil {
				slog.Debug("DropOutbound", "handler", out.Name())
				return nil
			}
		}
		return pkt
	}
	for packet := range vpn.outbound {
		if packet = handle(packet); packet == nil {
			nic.RecyclePacket(packet)
			continue
		}
		pkt := packet.AsBytes()
		if packet.Ver() == 4 {
			header, err := ipv4.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst.String() == netlink.Show().IPv4 {
				vpn.inbound <- packet
				continue
			}
			sendPacketToPeer(packet, header.Src, header.Dst)
			continue
		}
		if packet.Ver() == 6 {
			header, err := ipv6.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst.String() == netlink.Show().IPv6 {
				vpn.inbound <- packet
				continue
			}
			sendPacketToPeer(packet, header.Src, header.Dst)
			continue
		}
		slog.Warn("Received invalid packet", "packet", hex.EncodeToString(pkt))
		nic.RecyclePacket(packet)
	}
}
