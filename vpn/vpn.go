package vpn

import (
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
	outbound chan []byte
	inbound  chan []byte
}

func New(cfg Config) *VPN {
	return &VPN{
		cfg:      cfg,
		outbound: make(chan []byte, 512),
		inbound:  make(chan []byte, 512),
	}
}

func (vpn *VPN) Run(ctx context.Context, nic *nic.VirtualNIC, packetConn net.PacketConn) error {
	vpn.nic = nic
	var wg sync.WaitGroup
	wg.Add(5)
	go vpn.runRoutingTableUpdateEventLoop(ctx, &wg)
	go vpn.runNICReadEventLoop(&wg, nic)
	go vpn.runNICWriteEventLoop(&wg, nic)
	go vpn.runPacketConnReadEventLoop(&wg, packetConn)
	go vpn.runPacketConnWriteEventLoop(&wg, packetConn)

	<-ctx.Done()
	packetConn.Close()
	nic.Close()
	close(vpn.inbound)
	close(vpn.outbound)
	wg.Wait()
	return nil
}

func (vpn *VPN) runRoutingTableUpdateEventLoop(ctx context.Context, wg *sync.WaitGroup) {
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

func (vpn *VPN) runNICReadEventLoop(wg *sync.WaitGroup, nic *nic.VirtualNIC) {
	defer wg.Done()
	for {
		pkt, err := nic.Read()
		if err != nil && strings.Contains(err.Error(), os.ErrClosed.Error()) {
			return
		}
		if err != nil {
			panic(err)
		}
		vpn.outbound <- pkt
	}
}

func (vpn *VPN) runNICWriteEventLoop(wg *sync.WaitGroup, nic *nic.VirtualNIC) {
	defer wg.Done()
	handle := func(pkt []byte) []byte {
		for _, in := range vpn.cfg.InboundHandlers {
			if pkt = in.In(pkt); pkt == nil {
				slog.Debug("DropInbound", "handler", in.Name())
				return nil
			}
		}
		return pkt
	}
	for {
		pkt, ok := <-vpn.inbound
		if !ok {
			return
		}
		if pkt = handle(pkt); pkt == nil {
			continue
		}
		err := nic.Write(pkt)
		if err != nil {
			slog.Debug("WriteToTunError", "detail", err.Error())
		}
	}
}

func (vpn *VPN) runPacketConnReadEventLoop(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	buf := make([]byte, vpn.cfg.MTU+40)
	for {
		n, _, err := packetConn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			panic(err)
		}
		pkt := make([]byte, n+nic.IPPacketOffset)
		copy(pkt[nic.IPPacketOffset:], buf[:n])
		vpn.inbound <- pkt
	}
}

func (vpn *VPN) runPacketConnWriteEventLoop(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	sendPacketToPeer := func(packet []byte, dstIP net.IP) {
		if dstIP.IsMulticast() {
			slog.Log(context.Background(), -10, "DropMulticastIP", "dst", dstIP)
			return
		}
		if peer, ok := vpn.nic.GetPeer(dstIP.String()); ok {
			_, err := packetConn.WriteTo(packet[nic.IPPacketOffset:], peer)
			if err != nil {
				slog.Error("WriteTo peer failed", "peer", peer, "detail", err)
			}
			return
		}
		slog.Log(context.Background(), -1, "DropPacketPeerNotFound", "ip", dstIP)
	}
	handle := func(pkt []byte) []byte {
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
			continue
		}
		pkt := packet[nic.IPPacketOffset:]
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
	}
}
