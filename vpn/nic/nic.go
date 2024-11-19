package nic

import (
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/sigcn/pg/lru"
)

const (
	IPPacketOffset = 16
)

type Config struct {
	Name       string
	MTU        int
	IPv4, IPv6 string
}

type NIC interface {
	io.Closer
	Write(*Packet) error
	Read() (*Packet, error)
}

type VirtualNIC struct {
	NIC

	routing    *lru.Cache[string, string]   // cidr as key, via ip as value
	peers      *lru.Cache[string, net.Addr] // ip as key
	nicInit    sync.Once
	peersMutex sync.RWMutex
}

func (r *VirtualNIC) init() {
	if r.NIC == nil {
		panic("NIC is required")
	}
	r.nicInit.Do(func() {
		r.routing = lru.New[string, string](512)
		r.peers = lru.New[string, net.Addr](1024)
	})
}

func (r *VirtualNIC) GetPeer(ip string) (net.Addr, bool) {
	r.init()
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

func (r *VirtualNIC) AddPeer(peer net.Addr, ipv4, ipv6 string) {
	r.init()
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	if ipv4 != "" {
		r.peers.Put(ipv4, peer)
	}
	if ipv6 != "" {
		r.peers.Put(ipv6, peer)
	}
}

func (r *VirtualNIC) AddRoute(dst *net.IPNet, via net.IP) bool {
	r.init()
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	slog.Info("AddRoute", "dst", dst, "via", via)
	r.routing.Put(dst.String(), via.String())
	return true
}

func (r *VirtualNIC) DelRoute(dst *net.IPNet, via net.IP) bool {
	r.init()
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	slog.Info("DelRoute", "dst", dst, "via", via)
	r.routing.Put(dst.String(), "")
	return true
}
