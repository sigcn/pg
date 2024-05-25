package vpn

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/rkonfj/peerguard/disco"
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
	IPv4             string
	IPv6             string
	InboundHandlers  []InboundHandler
	OutboundHandlers []OutboundHandler
}

type VPN struct {
	routingTable RoutingTable
	packetConn   net.PacketConn
	cfg          Config
	outbound     chan []byte
	inbound      chan []byte
	newBuf       func() []byte
}

func New(routingTable RoutingTable, packetConn net.PacketConn, cfg Config) *VPN {
	return &VPN{
		routingTable: routingTable,
		packetConn:   packetConn,
		cfg:          cfg,
		outbound:     make(chan []byte, 512),
		inbound:      make(chan []byte, 512),
		newBuf:       func() []byte { return make([]byte, cfg.MTU+IPPacketOffset+40) },
	}
}

func (vpn *VPN) RunTun(ctx context.Context, tunName string) error {
	device, err := tun.CreateTUN(tunName, vpn.cfg.MTU)
	if err != nil {
		return fmt.Errorf("create tun device (%s) failed: %w", tunName, err)
	}
	if vpn.cfg.IPv4 != "" {
		link.SetupLink(tunName, vpn.cfg.IPv4)
	}
	if vpn.cfg.IPv6 != "" {
		link.SetupLink(tunName, vpn.cfg.IPv6)
	}
	return vpn.run(ctx, device)
}

func (vpn *VPN) run(ctx context.Context, device tun.Device) error {
	var wg sync.WaitGroup
	wg.Add(4)
	go vpn.runTunReadEventLoop(&wg, device)
	go vpn.runTunWriteEventLoop(&wg, device)
	go vpn.runPacketConnReadEventLoop(&wg, vpn.packetConn)
	go vpn.runPacketConnWriteEventLoop(&wg, vpn.packetConn)

	<-ctx.Done()
	close(vpn.inbound)
	close(vpn.outbound)
	device.Close()
	vpn.packetConn.Close()
	wg.Wait()
	return nil
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
		if peer, ok := vpn.routingTable.GetPeer(dstIP.String()); ok {
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
