package nic

import (
	"io"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/sigcn/pg/cache/lru"
)

const (
	IPPacketOffset = 16
)

type Config struct {
	Name string `yaml:"name"`
	MTU  int    `yaml:"mtu"`
	IPv4 string `yaml:"ipv4"`
	IPv6 string `yaml:"ipv6"`
}

type NIC interface {
	io.Closer
	Write(*Packet) error
	Read() (*Packet, error)
}

type Peer struct {
	Addr       net.Addr
	IPv4, IPv6 string
	Meta       url.Values
}

type VirtualNIC struct {
	NIC

	routing    *lru.Cache[string, string] // cidr as key, via ip as value
	peers      *lru.Cache[string, *Peer]  // ip as key
	nicInit    sync.Once
	peersMutex sync.RWMutex
}

func (r *VirtualNIC) init() {
	if r.NIC == nil {
		panic("NIC is required")
	}
	r.nicInit.Do(func() {
		r.routing = lru.New[string, string](512)
		r.peers = lru.New[string, *Peer](1024)
	})
}

func (r *VirtualNIC) GetPeer(ip string) (net.Addr, bool) {
	r.init()
	r.peersMutex.RLock()
	defer r.peersMutex.RUnlock()
	peerID, ok := r.peers.Get(ip)
	if ok {
		return peerID.Addr, true
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
	return peerID.Addr, true
}

func (r *VirtualNIC) AddPeer(peer Peer) {
	r.init()
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	if peer.IPv4 != "" {
		r.peers.Put(peer.IPv4, &peer)
	}
	if peer.IPv6 != "" {
		r.peers.Put(peer.IPv6, &peer)
	}
}

func (r *VirtualNIC) RemovePeer(addr net.Addr) {
	r.init()
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	_, v, ok := r.peers.Find(func(s string, p *Peer) bool {
		return p.Addr == addr
	})
	if ok {
		r.peers.Del(v.IPv4)
		r.peers.Del(v.IPv6)
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

func (r *VirtualNIC) Peers() []*Peer {
	r.init()
	peerMap := make(map[string]*Peer)
	r.peersMutex.RLock()
	for _, v := range r.peers.Dump() {
		peerMap[v.Addr.String()] = v
	}
	r.peersMutex.RUnlock()
	var peers []*Peer
	for _, v := range peerMap {
		peers = append(peers, v)
	}
	sort.SliceStable(peers, func(i, j int) bool {
		return strings.Compare(peers[i].IPv4+peers[i].IPv6, peers[j].IPv4+peers[j].IPv6) > 0
	})
	return peers
}
