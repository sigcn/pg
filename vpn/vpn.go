package vpn

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/lru"
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
	MTU           int
	IPv4          string
	IPv6          string
	AllowedIPs    []string
	NetworkSecret peer.NetworkSecret
	Peermap       peer.PeermapCluster
	PrivateKey    string
}

type VPN struct {
	cfg        Config
	outbound   chan []byte
	inbound    chan []byte
	bufPool    sync.Pool
	routes     *lru.Cache[peer.PeerID, []*net.IPNet]
	peers      *lru.Cache[string, peer.PeerID]
	peersMutex sync.RWMutex
}

func New(cfg Config) *VPN {
	return &VPN{
		cfg:      cfg,
		outbound: make(chan []byte, 512),
		inbound:  make(chan []byte, 512),
		bufPool: sync.Pool{New: func() any {
			buf := make([]byte, cfg.MTU+IPPacketOffset+40)
			return &buf
		}},
		routes: lru.New[peer.PeerID, []*net.IPNet](1024),
		peers:  lru.New[string, peer.PeerID](1024),
	}
}

func (vpn *VPN) RunTun(ctx context.Context, tunName string) error {
	device, err := tun.CreateTUN(tunName, vpn.cfg.MTU)
	if err != nil {
		return fmt.Errorf("create tun device (%s) failed: %w", tunName, err)
	}
	if vpn.cfg.IPv4 != "" {
		link.SetupLink(device, vpn.cfg.IPv4)
	}
	if vpn.cfg.IPv6 != "" {
		link.SetupLink(device, vpn.cfg.IPv6)
	}
	return vpn.run(ctx, device)
}

func (vpn *VPN) run(ctx context.Context, device tun.Device) error {
	disco.SetIgnoredLocalInterfaceNamePrefixs("pg", "wg", "veth", "docker", "nerdctl")

	p2pOptions := []p2p.Option{
		p2p.PeerMeta("allowedIPs", vpn.cfg.AllowedIPs),
		p2p.ListenPeerUp(vpn.setPeer),
	}

	if vpn.cfg.IPv4 != "" {
		ipv4, err := netip.ParsePrefix(vpn.cfg.IPv4)
		if err != nil {
			return err
		}
		disco.SetIgnoredLocalCIDRs(vpn.cfg.IPv4)
		p2pOptions = append(p2pOptions, p2p.PeerAlias1(ipv4.Addr().String()))
	}

	if vpn.cfg.IPv6 != "" {
		ipv6, err := netip.ParsePrefix(vpn.cfg.IPv6)
		if err != nil {
			return err
		}
		disco.SetIgnoredLocalCIDRs(vpn.cfg.IPv6)
		p2pOptions = append(p2pOptions, p2p.PeerAlias2(ipv6.Addr().String()))
	}

	if vpn.cfg.PrivateKey != "" {
		p2pOptions = append(p2pOptions, p2p.ListenPeerCurve25519(vpn.cfg.PrivateKey))
	} else {
		p2pOptions = append(p2pOptions, p2p.ListenPeerSecure())
	}

	packetConn, err := p2p.ListenPacket(vpn.cfg.NetworkSecret, vpn.cfg.Peermap, p2pOptions...)
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

func (vpn *VPN) setPeer(peer peer.PeerID, metadata peer.Metadata) {
	vpn.peersMutex.Lock()
	defer vpn.peersMutex.Unlock()
	vpn.peers.Put(metadata.Alias1, peer)
	vpn.peers.Put(metadata.Alias2, peer)
	var allowIPs []*net.IPNet
	for _, allowIP := range metadata.Extra["allowedIPs"].([]any) {
		_, cidr, err := net.ParseCIDR(allowIP.(string))
		if err != nil {
			continue
		}
		slog.Info("Route", "to", cidr, "via", metadata.Alias1)
		allowIPs = append(allowIPs, cidr)
	}
	vpn.routes.Put(peer, allowIPs)
}

func (vpn *VPN) getPeer(ip string) (peer.PeerID, bool) {
	vpn.peersMutex.RLock()
	defer vpn.peersMutex.RUnlock()
	peerID, ok := vpn.peers.Get(ip)
	if ok {
		return peerID, true
	}
	k, _, ok := vpn.routes.Find(func(k peer.PeerID, v []*net.IPNet) bool {
		for _, cidr := range v {
			if cidr.Contains(net.ParseIP(ip)) {
				return true
			}
		}
		return false
	})
	return k, ok
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
				slog.Log(context.Background(), -10, "DropMulticastIPv4", "dst", header.Dst)
				continue
			}
			if peer, ok := vpn.getPeer(header.Dst.String()); ok {
				_, err = packetConn.WriteTo(pkt, peer)
				if err != nil {
					panic(err)
				}
				continue
			}
			slog.Log(context.Background(), -10, "DropPacketPeerNotFound", "ip", header.Dst)
			continue
		}
		if pkt[0]>>4 == 6 {
			header, err := ipv6.ParseHeader(pkt)
			if err != nil {
				panic(err)
			}
			if header.Dst[0] == 0xff {
				slog.Log(context.Background(), -10, "DropMulticastIPv6", "dst", header.Dst)
				continue
			}
			if peer, ok := vpn.getPeer(header.Dst.String()); ok {
				_, err = packetConn.WriteTo(pkt, peer)
				if err != nil {
					panic(err)
				}
				continue
			}
			slog.Log(context.Background(), -10, "DropPacketPeerNotFound", "ip", header.Dst)
			continue
		}
		slog.Warn("Received invalid packet", "packet", hex.EncodeToString(pkt))
	}
}
