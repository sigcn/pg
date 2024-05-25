package vpn

import (
	"log/slog"
	"net"
	"sync"

	"github.com/rkonfj/peerguard/lru"
	"github.com/rkonfj/peerguard/vpn/link"
)

type RoutingTable interface {
	GetPeer(ip string) (net.Addr, bool)
}

var _ RoutingTable = (*SimpleRoutingTable)(nil)

type SimpleRoutingTable struct {
	ipv6       *lru.Cache[string, []*net.IPNet]
	ipv4       *lru.Cache[string, []*net.IPNet]
	peers      *lru.Cache[string, net.Addr]
	peersMutex sync.RWMutex
}

func NewRoutingTable() *SimpleRoutingTable {
	return &SimpleRoutingTable{
		ipv6:  lru.New[string, []*net.IPNet](256),
		ipv4:  lru.New[string, []*net.IPNet](256),
		peers: lru.New[string, net.Addr](1024),
	}
}

func (r *SimpleRoutingTable) GetPeer(ip string) (net.Addr, bool) {
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

func (r *SimpleRoutingTable) AddPeer(ipv4, ipv6 string, peer net.Addr) {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	r.peers.Put(ipv4, peer)
	r.peers.Put(ipv6, peer)
}

func (r *SimpleRoutingTable) AddRoute4(cidr *net.IPNet, viaIP, ifName string) {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	cidrs, _ := r.ipv4.Get(viaIP)
	cidrs = append(cidrs, cidr)
	r.ipv4.Put(viaIP, cidrs)
	r.updateRoute(ifName, net.ParseIP(viaIP), cidrs[:len(cidrs)-1], cidrs)
}

func (r *SimpleRoutingTable) AddRoute6(cidr *net.IPNet, viaIP, ifName string) {
	r.peersMutex.Lock()
	defer r.peersMutex.Unlock()
	cidrs, _ := r.ipv6.Get(viaIP)
	cidrs = append(cidrs, cidr)
	r.ipv6.Put(viaIP, cidrs)
	r.updateRoute(ifName, net.ParseIP(viaIP), cidrs[:len(cidrs)-1], cidrs)
}

func (r *SimpleRoutingTable) DelRoute(cidr *net.IPNet, viaIP, ifName string) {
	link.DelRoute(ifName, cidr, net.ParseIP(viaIP))
}

func (r *SimpleRoutingTable) updateRoute(ifName string, viaIP net.IP, oldTo []*net.IPNet, cidrs []*net.IPNet) {
	for _, cidr := range oldTo {
		err := link.DelRoute(ifName, cidr, viaIP)
		if err != nil {
			slog.Error("DelRoute error", "detail", err, "to", cidr, "via", viaIP)
		} else {
			slog.Info("DelRoute", "to", cidr, "via", viaIP)
		}
	}
	for _, cidr := range cidrs {
		err := link.AddRoute(ifName, cidr, viaIP)
		if err != nil {
			slog.Error("AddRoute error", "detail", err, "to", cidr, "via", viaIP)
		} else {
			slog.Info("AddRoute", "to", cidr, "via", viaIP)
		}
	}
}
