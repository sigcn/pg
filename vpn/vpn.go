package vpn

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/p2p"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/vpn/link"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	IPPacketOffset = 16
)

type Config struct {
	MTU     int
	Network string
	Peermap []string
	CIDR    string
}

type VPN struct {
	cfg      Config
	outbound chan []byte
	inbound  chan []byte
	bufPool  sync.Pool
}

func NewVPN(cfg Config) *VPN {
	return &VPN{
		cfg:      cfg,
		outbound: make(chan []byte, 1000),
		inbound:  make(chan []byte, 1000),
		bufPool: sync.Pool{New: func() any {
			buf := make([]byte, cfg.MTU+IPPacketOffset+40)
			return &buf
		}},
	}
}

func (vpn *VPN) RunTun(ctx context.Context, tunName string) error {
	device, err := tun.CreateTUN(tunName, vpn.cfg.MTU)
	if err != nil {
		return err
	}
	link.SetupLink(device, vpn.cfg.CIDR)
	return vpn.run(ctx, device)
}

func (vpn *VPN) run(ctx context.Context, device tun.Device) error {
	cidr, err := netip.ParsePrefix(vpn.cfg.CIDR)
	if err != nil {
		return err
	}

	packetConn, err := p2p.ListenPacket(vpn.cfg.Network, vpn.cfg.Peermap, p2p.ListenPeerID(cidr.Addr().String()))
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(4)
	go vpn.runTunReadEventLoop(&wg, device)
	go vpn.runTunWriteEventLoop(&wg, device)
	go vpn.runPacketConnReadEventLoop(&wg, packetConn)
	go vpn.runPacketConnWriteEventLoop(&wg, packetConn)
	<-ctx.Done()
	close(vpn.inbound)
	close(vpn.outbound)
	device.Close()
	packetConn.Close()
	wg.Wait()
	return nil
}

func (vpn *VPN) runTunReadEventLoop(wg *sync.WaitGroup, device tun.Device) {
	defer wg.Done()

	bufs := make([][]byte, device.BatchSize())
	sizes := make([]int, device.BatchSize())

	for i := range bufs {
		bufs[i] = make([]byte, vpn.cfg.MTU+40)
	}

	for {
		n, err := device.Read(bufs, sizes, 0)
		if err != nil && strings.Contains(err.Error(), os.ErrClosed.Error()) {
			return
		}
		if err != nil {
			panic(err)
		}
		for i := 0; i < n; i++ {
			packet := vpn.bufPool.Get().(*[]byte)
			copy(*packet, bufs[i][:sizes[i]])
			vpn.outbound <- (*packet)[:sizes[i]]
		}
	}
}

func (vpn *VPN) runTunWriteEventLoop(wg *sync.WaitGroup, device tun.Device) {
	defer wg.Done()
	for {
		pkt, ok := <-vpn.inbound
		if !ok {
			return
		}
		_, err := device.Write([][]byte{pkt}, IPPacketOffset)
		if err != nil {
			slog.Error("write to tun", "err", err.Error())
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
		pkt := *vpn.bufPool.Get().(*[]byte)
		copy(pkt[IPPacketOffset:], buf[:n])
		vpn.inbound <- pkt[:n+IPPacketOffset]
	}
}

func (vpn *VPN) runPacketConnWriteEventLoop(wg *sync.WaitGroup, packetConn net.PacketConn) {
	defer wg.Done()
	for {
		pkt, ok := <-vpn.outbound
		if !ok {
			return
		}
		if pkt[0]>>4 == 4 {
			header, err := ipv4.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst.To4()[0] >= 224 && header.Dst.To4()[0] <= 239 {
				slog.Debug("DropMulticastIPv4", "dst", header.Dst)
				continue
			}
			_, err = packetConn.WriteTo(pkt, peer.PeerID(header.Dst.String()))
			if err != nil {
				panic(err)
			}
			continue
		}
		if pkt[0]>>4 == 6 {
			header, err := ipv6.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst[0] == 0xff {
				slog.Debug("DropMulticastIPv6", "dst", header.Dst)
				continue
			}
			_, err = packetConn.WriteTo(pkt, peer.PeerID(header.Dst.String()))
			if err != nil {
				panic(err)
			}
			continue
		}
		slog.Warn("Received invalid packet", "packet", hex.EncodeToString(pkt))
	}
}
