package vpn

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
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
		nic.IPPacketPool = &nic.PacketPool{MTU: cfg.MTU}
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

func (vpn *VPN) nicRead(wg *sync.WaitGroup, nic *nic.VirtualNIC) {
	defer wg.Done()
	for {
		packet, err := nic.Read()
		if err != nil && strings.Contains(err.Error(), os.ErrClosed.Error()) {
			return
		}
		if err != nil {
			panic(err)
		}
		vpn.outbound <- packet
	}
}

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
	for {
		packet, ok := <-vpn.inbound
		if !ok {
			return
		}
		if packet = handle(packet); packet == nil {
			nic.IPPacketPool.Put(packet)
			continue
		}
		err := vnic.Write(packet)
		if err != nil {
			slog.Debug("WriteToTunError", "detail", err.Error())
		}
		nic.IPPacketPool.Put(packet)
	}
}

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
		pkt := nic.IPPacketPool.Get()
		pkt.Write(buf[:n])
		vpn.inbound <- pkt
	}
}

func (vpn *VPN) packetConnWrite(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	sendPacketToPeer := func(packet *nic.Packet, dstIP net.IP) {
		defer nic.IPPacketPool.Put(packet)
		if dstIP.IsMulticast() {
			slog.Log(context.Background(), -10, "DropMulticastIP", "dst", dstIP)
			return
		}
		if peer, ok := vpn.nic.GetPeer(dstIP.String()); ok {
			_, err := packetConn.WriteTo(packet.AsBytes(), peer)
			if err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Error("PacketConnWrite", "peer", peer, "err", err)
			}
			return
		}
		slog.Log(context.Background(), -1, "DropPacketPeerNotFound", "ip", dstIP)
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
	for {
		packet, ok := <-vpn.outbound
		if !ok {
			return
		}
		if packet = handle(packet); packet == nil {
			nic.IPPacketPool.Put(packet)
			continue
		}
		pkt := packet.AsBytes()
		if pkt[0]>>4 == 4 {
			header, err := ipv4.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst.String() == netlink.Show().IPv4 {
				vpn.inbound <- packet
				continue
			}
			sendPacketToPeer(packet, header.Dst)
			continue
		}
		if pkt[0]>>4 == 6 {
			header, err := ipv6.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst.String() == netlink.Show().IPv6 {
				vpn.inbound <- packet
				continue
			}
			sendPacketToPeer(packet, header.Dst)
			continue
		}
		slog.Warn("Received invalid packet", "packet", hex.EncodeToString(pkt))
		nic.IPPacketPool.Put(packet)
	}
}
