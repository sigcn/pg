package iface

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/rkonfj/peerguard/lru"
	"github.com/rkonfj/peerguard/vpn/link"
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
	ipv6       *lru.Cache[string, []*net.IPNet]
	ipv4       *lru.Cache[string, []*net.IPNet]
	peers      *lru.Cache[string, net.Addr]
	peersMutex sync.RWMutex
}

func Create(tunName string, cfg Config) (*TunInterface, error) {
	device, err := tun.CreateTUN(tunName, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("create tun device (%s) failed: %w", tunName, err)
	}
	if cfg.IPv4 != "" {
		link.SetupLink(tunName, cfg.IPv4)
	}
	if cfg.IPv6 != "" {
		link.SetupLink(tunName, cfg.IPv6)
	}
	return &TunInterface{
		dev:    device,
		ifName: tunName,
		ipv6:   lru.New[string, []*net.IPNet](256),
		ipv4:   lru.New[string, []*net.IPNet](256),
		peers:  lru.New[string, net.Addr](1024),
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
	if dstIP.To4() != nil {
		k, _, _ := r.ipv4.Find(func(k string, v []*net.IPNet) bool {
			for _, cidr := range v {
				if cidr.Contains(dstIP) {
					return true
				}
			}
			return false
		})
		return r.peers.Get(k)
	}
	k, _, _ := r.ipv6.Find(func(k string, v []*net.IPNet) bool {
		for _, cidr := range v {
			if cidr.Contains(dstIP) {
				return true
			}
		}
		return false
	})
	return r.peers.Get(k)
}

func (r *TunInterface) AddPeer(ipv4, ipv6 string, peer net.Addr) {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	if ipv4 != "" {
		r.peers.Put(ipv4, peer)
	}
	if ipv6 != "" {
		r.peers.Put(ipv6, peer)
	}
}

func (r *TunInterface) AddRoute(cidr *net.IPNet, via net.IP) {
	slog.Debug("AddRoute", "to", cidr, "via", via)
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	var cidrs []*net.IPNet
	if via.To4() != nil {
		cidrs, _ = r.ipv4.Get(via.String())
		cidrs = append(cidrs, cidr)
		r.ipv4.Put(via.String(), cidrs)
	} else {
		cidrs, _ := r.ipv6.Get(via.String())
		cidrs = append(cidrs, cidr)
		r.ipv6.Put(via.String(), cidrs)
	}
	r.updateRoute(via, cidrs[:len(cidrs)-1], cidrs)
}

func (r *TunInterface) DelRoute(cidr *net.IPNet, viaIP string) {
	link.DelRoute(r.ifName, cidr, net.ParseIP(viaIP))
}

func (r *TunInterface) updateRoute(viaIP net.IP, oldTo []*net.IPNet, cidrs []*net.IPNet) {
	if r.ifName == "" {
		slog.Debug("Ignore os routing")
		return
	}
	for _, cidr := range oldTo {
		err := link.DelRoute(r.ifName, cidr, viaIP)
		if err != nil {
			slog.Error("DelRoute error", "detail", err, "to", cidr, "via", viaIP)
		} else {
			slog.Info("DelRoute", "to", cidr, "via", viaIP)
		}
	}
	for _, cidr := range cidrs {
		err := link.AddRoute(r.ifName, cidr, viaIP)
		if err != nil {
			slog.Error("AddRoute error", "detail", err, "to", cidr, "via", viaIP)
		} else {
			slog.Info("AddRoute", "to", cidr, "via", viaIP)
		}
	}
}

func (r *TunInterface) Device() tun.Device {
	return r.dev
}

func (r *TunInterface) Close() error {
	return r.dev.Close()
}
