package iface

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/sigcn/pg/lru"
	"github.com/sigcn/pg/netlink"
	"golang.zx2c4.com/wireguard/tun"
)

type Interface interface {
	io.Closer
	RoutingTable
	Device() tun.Device
}

type Config struct {
	MTU        int
	IPv4, IPv6 string
}

var _ RoutingTable = (*TunInterface)(nil)

type TunInterface struct {
	dev        tun.Device
	ifName     string
	routing    *lru.Cache[string, string]   // cidr as key, via ip as value
	peers      *lru.Cache[string, net.Addr] // ip as key
	peersMutex sync.RWMutex
}

func Create(tunName string, cfg Config) (*TunInterface, error) {
	device, err := tun.CreateTUN(tunName, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("create tun device (%s): %w", tunName, err)
	}
	deviceName, err := device.Name()
	if err != nil {
		return nil, fmt.Errorf("get tun device name: %w", err)
	}
	if cfg.IPv4 != "" {
		netlink.SetupLink(deviceName, cfg.IPv4)
	}
	if cfg.IPv6 != "" {
		netlink.SetupLink(deviceName, cfg.IPv6)
	}
	return &TunInterface{
		dev:     device,
		ifName:  deviceName,
		routing: lru.New[string, string](512),
		peers:   lru.New[string, net.Addr](1024),
	}, nil
}

func (r *TunInterface) GetPeer(ip string) (net.Addr, bool) {
	r.peersMutex.RLock()
	defer r.peersMutex.RUnlock()
	peerID, ok := r.peers.Get(ip)
	if ok {
		return peerID, true
	}
	dstIP := net.ParseIP(ip)
	_, v, _ := r.routing.Find(func(k string, v string) bool {
		_, cidr, err := net.ParseCIDR(k)
		if err != nil {
			return false
		}
		return cidr.Contains(dstIP)
	})
	peerID, ok = r.peers.Get(v)
	if v == "" || !ok {
		return nil, false
	}
	return peerID, true
}

func (r *TunInterface) AddPeer(peer net.Addr, ipv4, ipv6 string) {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	if ipv4 != "" {
		r.peers.Put(ipv4, peer)
	}
	if ipv6 != "" {
		r.peers.Put(ipv6, peer)
	}
}

func (r *TunInterface) AddRoute(dst *net.IPNet, via net.IP) bool {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	slog.Info("AddRoute", "dst", dst, "via", via)
	r.routing.Put(dst.String(), via.String())
	return true
}

func (r *TunInterface) DelRoute(dst *net.IPNet, via net.IP) bool {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	slog.Info("DelRoute", "dst", dst, "via", via)
	r.routing.Put(dst.String(), "")
	return true
}

func (r *TunInterface) Device() tun.Device {
	return r.dev
}

func (r *TunInterface) Close() error {
	return r.dev.Close()
}
