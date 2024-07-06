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

	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/vpn/iface"
	"github.com/rkonfj/peerguard/vpn/link"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	IPPacketOffset = 16
)

type Config struct {
	MTU              int
	InboundHandlers  []InboundHandler
	OutboundHandlers []OutboundHandler
}

type VPN struct {
	rt       iface.RoutingTable
	cfg      Config
	outbound chan []byte
	inbound  chan []byte
	newBuf   func() []byte
}

func New(cfg Config) *VPN {
	return &VPN{
		cfg:      cfg,
		outbound: make(chan []byte, 512),
		inbound:  make(chan []byte, 512),
		newBuf:   func() []byte { return make([]byte, cfg.MTU+IPPacketOffset+40) },
	}
}

func (vpn *VPN) Run(ctx context.Context, iface iface.Interface, packetConn net.PacketConn) error {
	vpn.rt = iface
	var wg sync.WaitGroup
	wg.Add(5)
	go vpn.runRoutingTableUpdateEventLoop(ctx, &wg)
	go vpn.runTunReadEventLoop(&wg, iface.Device())
	go vpn.runTunWriteEventLoop(&wg, iface.Device())
	go vpn.runPacketConnReadEventLoop(&wg, packetConn)
	go vpn.runPacketConnWriteEventLoop(&wg, packetConn)

	<-ctx.Done()
	packetConn.Close()
	iface.Close()
	close(vpn.inbound)
	close(vpn.outbound)
	wg.Wait()
	return nil
}

func (vpn *VPN) runRoutingTableUpdateEventLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ch := make(chan link.RouteUpdate)
	if err := link.RouteSubscribe(ctx, ch); err != nil {
		slog.Debug("RouteSubscribe", "err", err)
		return
	}
	for r := range ch {
		switch r.Type {
		case 1:
			disco.AddIgnoredLocalCIDRs(r.Dst.String())
			vpn.rt.AddRoute(r.Dst, r.Via)
		case 2:
			vpn.rt.DelRoute(r.Dst, r.Via)
		}
	}
}

func (vpn *VPN) runTunReadEventLoop(wg *sync.WaitGroup, device tun.Device) {
	defer wg.Done()

	bufs := make([][]byte, device.BatchSize())
	sizes := make([]int, device.BatchSize())

	for i := range bufs {
		bufs[i] = make([]byte, vpn.cfg.MTU+IPPacketOffset+40)
	}

	for {
		n, err := device.Read(bufs, sizes, IPPacketOffset)
		if err != nil && strings.Contains(err.Error(), os.ErrClosed.Error()) {
			return
		}
		if err != nil {
			panic(err)
		}
		for i := 0; i < n; i++ {
			packet := vpn.newBuf()
			copy(packet, bufs[i][:sizes[i]+IPPacketOffset])
			vpn.outbound <- packet[:sizes[i]+IPPacketOffset]
		}
	}
}

func (vpn *VPN) runTunWriteEventLoop(wg *sync.WaitGroup, device tun.Device) {
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
		_, err := device.Write([][]byte{pkt}, IPPacketOffset)
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
			if errors.Is(err, disco.ErrUseOfClosedConnection) {
				return
			}
			panic(err)
		}
		pkt := vpn.newBuf()
		copy(pkt[IPPacketOffset:], buf[:n])
		vpn.inbound <- pkt[:n+IPPacketOffset]
	}
}

func (vpn *VPN) runPacketConnWriteEventLoop(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	sendPacketToPeer := func(packet []byte, dstIP net.IP) {
		if dstIP.IsMulticast() {
			slog.Log(context.Background(), -10, "DropMulticastIP", "dst", dstIP)
			return
		}
		if peer, ok := vpn.rt.GetPeer(dstIP.String()); ok {
			_, err := packetConn.WriteTo(packet[IPPacketOffset:], peer)
			if err != nil {
				slog.Error("WriteTo peer failed", "peer", peer, "detail", err)
			}
			return
		}
		slog.Log(context.Background(), -10, "DropPacketPeerNotFound", "ip", dstIP)
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
		pkt := packet[IPPacketOffset:]
		if pkt[0]>>4 == 4 {
			header, err := ipv4.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst.String() == link.Show().IPv4 {
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
			if header.Dst.String() == link.Show().IPv6 {
				vpn.inbound <- packet
				continue
			}
			sendPacketToPeer(packet, header.Dst)
			continue
		}
		slog.Warn("Received invalid packet", "packet", hex.EncodeToString(pkt))
	}
}
