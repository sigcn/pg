package vpn

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
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

type Route struct {
	Device tun.Device
	OldDst []*net.IPNet
	NewDst []*net.IPNet
	Via    net.IP
}

type Config struct {
	MTU               int
	IPv4              string
	IPv6              string
	AllowedIPs        []string
	Peers             []string
	SecretStore       peer.SecretStore
	Peermap           peer.PeermapCluster
	PrivateKey        string
	OnRoute           func(route Route)
	ModifyDiscoConfig func(cfg *disco.DiscoConfig)
}

type VPN struct {
	cfg      Config
	outbound chan []byte
	inbound  chan []byte
	newBuf   func() []byte

	ipv6Routes *lru.Cache[string, []*net.IPNet]
	ipv4Routes *lru.Cache[string, []*net.IPNet]
	peers      *lru.Cache[string, peer.PeerID]
	peersMutex sync.RWMutex
}

func New(cfg Config) *VPN {
	disco.SetModifyDiscoConfig(cfg.ModifyDiscoConfig)
	return &VPN{
		cfg:        cfg,
		outbound:   make(chan []byte, 512),
		inbound:    make(chan []byte, 512),
		newBuf:     func() []byte { return make([]byte, cfg.MTU+IPPacketOffset+40) },
		ipv6Routes: lru.New[string, []*net.IPNet](256),
		ipv4Routes: lru.New[string, []*net.IPNet](256),
		peers:      lru.New[string, peer.PeerID](1024),
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
	disco.SetIgnoredLocalInterfaceNamePrefixs("pg", "wg", "veth", "docker", "nerdctl", "tailscale")
	disco.AddIgnoredLocalCIDRs(vpn.cfg.AllowedIPs...)
	p2pOptions := []p2p.Option{
		p2p.PeerMeta("allowedIPs", vpn.cfg.AllowedIPs),
		p2p.ListenPeerUp(func(pi peer.PeerID, m peer.Metadata) { vpn.setPeer(device, pi, m) }),
	}

	if len(vpn.cfg.Peers) > 0 {
		p2pOptions = append(p2pOptions, p2p.PeerSilenceMode())
	}

	for _, peerURL := range vpn.cfg.Peers {
		pgPeer, err := url.Parse(peerURL)
		if err != nil {
			continue
		}
		if pgPeer.Scheme != "pg" {
			return fmt.Errorf("unsupport scheme %s", pgPeer.Scheme)
		}
		extra := make(map[string]any)
		for k, v := range pgPeer.Query() {
			extra[k] = v[0]
		}
		vpn.setPeer(device, peer.PeerID(pgPeer.Host), peer.Metadata{
			Alias1: pgPeer.Query().Get("alias1"),
			Alias2: pgPeer.Query().Get("alias2"),
			Extra:  extra,
		})
	}

	if vpn.cfg.IPv4 != "" {
		ipv4, err := netip.ParsePrefix(vpn.cfg.IPv4)
		if err != nil {
			return err
		}
		disco.AddIgnoredLocalCIDRs(vpn.cfg.IPv4)
		p2pOptions = append(p2pOptions, p2p.PeerAlias1(ipv4.Addr().String()))
	}

	if vpn.cfg.IPv6 != "" {
		ipv6, err := netip.ParsePrefix(vpn.cfg.IPv6)
		if err != nil {
			return err
		}
		disco.AddIgnoredLocalCIDRs(vpn.cfg.IPv6)
		p2pOptions = append(p2pOptions, p2p.PeerAlias2(ipv6.Addr().String()))
	}

	if vpn.cfg.PrivateKey != "" {
		p2pOptions = append(p2pOptions, p2p.ListenPeerCurve25519(vpn.cfg.PrivateKey))
	} else {
		p2pOptions = append(p2pOptions, p2p.ListenPeerSecure())
	}

	packetConn, err := p2p.ListenPacket(vpn.cfg.SecretStore, vpn.cfg.Peermap, p2pOptions...)
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

func (vpn *VPN) setPeer(device tun.Device, peer peer.PeerID, metadata peer.Metadata) {
	vpn.peersMutex.Lock()
	defer vpn.peersMutex.Unlock()
	vpn.peers.Put(metadata.Alias1, peer)
	vpn.peers.Put(metadata.Alias2, peer)
	var allowedIPv4s, allowedIPv6s []*net.IPNet
	if allowedIPs := metadata.Extra["allowedIPs"]; allowedIPs != nil {
		for _, allowIP := range allowedIPs.([]any) {
			_, cidr, err := net.ParseCIDR(allowIP.(string))
			if err != nil {
				continue
			}
			if cidr.IP.To4() != nil {
				allowedIPv4s = append(allowedIPv4s, cidr)
			} else {
				allowedIPv6s = append(allowedIPv6s, cidr)
			}
		}
	}
	if len(allowedIPv4s) > 0 {
		oldTo, _ := vpn.ipv4Routes.Get(metadata.Alias1)
		vpn.ipv4Routes.Put(metadata.Alias1, allowedIPv4s)
		if onRoute := vpn.cfg.OnRoute; onRoute != nil {
			onRoute(Route{
				Device: device,
				OldDst: oldTo,
				NewDst: allowedIPv4s,
				Via:    net.ParseIP(metadata.Alias1),
			})
		}
	}
	if len(allowedIPv6s) > 0 {
		oldTo, _ := vpn.ipv6Routes.Get(metadata.Alias2)
		vpn.ipv6Routes.Put(metadata.Alias2, allowedIPv6s)
		if onRoute := vpn.cfg.OnRoute; onRoute != nil {
			onRoute(Route{
				Device: device,
				OldDst: oldTo,
				NewDst: allowedIPv6s,
				Via:    net.ParseIP(metadata.Alias2),
			})
		}
	}
}

func (vpn *VPN) getPeer(ip string) (peer.PeerID, bool) {
	vpn.peersMutex.RLock()
	defer vpn.peersMutex.RUnlock()
	peerID, ok := vpn.peers.Get(ip)
	if ok {
		return peerID, true
	}
	dstIP := net.ParseIP(ip)
	if dstIP.To4() != nil {
		k, _, _ := vpn.ipv4Routes.Find(func(k string, v []*net.IPNet) bool {
			for _, cidr := range v {
				if cidr.Contains(dstIP) {
					return true
				}
			}
			return false
		})
		return vpn.peers.Get(k)
	}
	k, _, _ := vpn.ipv6Routes.Find(func(k string, v []*net.IPNet) bool {
		for _, cidr := range v {
			if cidr.Contains(dstIP) {
				return true
			}
		}
		return false
	})
	return vpn.peers.Get(k)
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
		if peer, ok := vpn.getPeer(dstIP.String()); ok {
			_, err := packetConn.WriteTo(packet[IPPacketOffset:], peer)
			if err != nil {
				slog.Error("WriteTo peer failed", "peer", peer, "detail", err)
			}
			return
		}
		slog.Log(context.Background(), -10, "DropPacketPeerNotFound", "ip", dstIP)
	}
	for {
		packet, ok := <-vpn.outbound
		if !ok {
			return
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
