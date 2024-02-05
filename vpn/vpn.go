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
	CIDR          string
	NetworkSecret peer.NetworkSecret
	Peermap       peer.PeermapCluster
	PrivateKey    string
}

type VPN struct {
	cfg        Config
	outbound   chan []byte
	inbound    chan []byte
	bufPool    sync.Pool
	peers      *lru.Cache[string, peer.PeerID]
	peersMutex sync.RWMutex
}

func New(cfg Config) *VPN {
	return &VPN{
		cfg:      cfg,
		outbound: make(chan []byte, 1000),
		inbound:  make(chan []byte, 1000),
		bufPool: sync.Pool{New: func() any {
			buf := make([]byte, cfg.MTU+IPPacketOffset+40)
			return &buf
		}},
		peers: lru.New[string, peer.PeerID](1024),
	}
}

func (vpn *VPN) RunTun(ctx context.Context, tunName string) error {
	device, err := tun.CreateTUN(tunName, vpn.cfg.MTU)
	if err != nil {
		return fmt.Errorf("create tun device (%s) failed: %w", tunName, err)
	}
	link.SetupLink(device, vpn.cfg.CIDR)
	return vpn.run(ctx, device)
}

func (vpn *VPN) run(ctx context.Context, device tun.Device) error {
	cidr, err := netip.ParsePrefix(vpn.cfg.CIDR)
	if err != nil {
		return err
	}

	disco.SetIgnoredLocalCIDRs(vpn.cfg.CIDR)
	disco.SetIgnoredLocalInterfaceNamePrefixs("pg", "wg", "veth", "docker", "nerdctl")
	secureOption := p2p.ListenPeerSecure()
	if vpn.cfg.PrivateKey != "" {
		secureOption = p2p.ListenPeerCurve25519(vpn.cfg.PrivateKey)
	}
	packetConn, err := p2p.ListenPacket(
		vpn.cfg.NetworkSecret,
		vpn.cfg.Peermap,
		secureOption,
		p2p.PeerAlias1(cidr.Addr().String()),
	)
	if err != nil {
		return err
	}
	packetConn.SetOnPeer(vpn.setPeer)
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
}

func (vpn *VPN) getPeer(ip string) (peer.PeerID, bool) {
	vpn.peersMutex.RLock()
	defer vpn.peersMutex.RUnlock()
	return vpn.peers.Get(ip)
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
				slog.Debug("DropMulticastIPv4", "dst", header.Dst)
				continue
			}
			if peer, ok := vpn.getPeer(header.Dst.String()); ok {
				_, err = packetConn.WriteTo(pkt, peer)
				if err != nil {
					panic(err)
				}
				continue
			}
			slog.Debug("DropPacketPeerNotFound", "ip", header.Dst)
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
			if peer, ok := vpn.getPeer(header.Dst.String()); ok {
				_, err = packetConn.WriteTo(pkt, peer)
				if err != nil {
					panic(err)
				}
				continue
			}
			slog.Debug("DropPacketPeerNotFound", "ip", header.Dst)
			continue
		}
		slog.Warn("Received invalid packet", "packet", hex.EncodeToString(pkt))
	}
}
