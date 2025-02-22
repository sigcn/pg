package udp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"

	"github.com/sigcn/pg/disco"
)

type localAddrs []string

func (la localAddrs) pubIP6() (addrs []*net.UDPAddr) {
	for _, addr := range la {
		udpAddr, _ := net.ResolveUDPAddr("udp", addr)
		if udpAddr.IP.To4() == nil && udpAddr.IP.IsGlobalUnicast() && !udpAddr.IP.IsPrivate() && !disco.IsCGN(udpAddr.IP) {
			addrs = append(addrs, udpAddr)
		}
	}
	return
}

func (la localAddrs) pubIP4() (addrs []*net.UDPAddr) {
	for _, addr := range la {
		udpAddr, _ := net.ResolveUDPAddr("udp", addr)
		if udpAddr.IP.To4() != nil && udpAddr.IP.IsGlobalUnicast() && !udpAddr.IP.IsPrivate() && !disco.IsCGN(udpAddr.IP) {
			addrs = append(addrs, udpAddr)
		}
	}
	return
}

func (c *UDPConn) localAddrs() (addrs localAddrs) {
	ips, err := disco.ListLocalIPs()
	if err != nil {
		slog.Error("[UDP] LocalAddrs", "err", err)
		return
	}
	var detectIPs []string
	for _, ip := range ips {
		if ip.To4() != nil { // ipv4
			if c.cfg.DisableIPv4 {
				continue
			}
		} else { // ipv6
			if c.cfg.DisableIPv6 {
				continue
			}
		}
		addr := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", c.cfg.Port))
		if slices.Contains(detectIPs, addr) {
			continue
		}
		detectIPs = append(detectIPs, addr)
	}
	slog.Log(context.Background(), -2, "[UDP] LocalAddrs", "addrs", detectIPs)
	return detectIPs
}

func (c *UDPConn) lanAddrsGenerate(peerID disco.PeerID) {
	c.closedWG.Add(1)
	defer c.closedWG.Done()
	for _, addr := range c.localAddrs() {
		uaddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			slog.Error("Resolve local udp addr error", "err", err)
			continue
		}
		natType := disco.Internal
		if uaddr.IP.IsGlobalUnicast() && !uaddr.IP.IsPrivate() && !disco.IsCGN(uaddr.IP) {
			if uaddr.IP.To4() != nil {
				natType = disco.IP4
			} else {
				natType = disco.IP6
			}
		}
		c.udpAddrSends <- &disco.PeerUDPAddr{
			ID:   peerID,
			Addr: uaddr,
			Type: natType,
		}
	}
}
