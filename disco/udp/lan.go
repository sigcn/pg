package udp

import (
	"log/slog"
	"net"

	"github.com/sigcn/pg/disco"
)

func (c *UDPConn) lanAddrsGenerate(peerID disco.PeerID) {
	c.closedWG.Add(1)
	defer c.closedWG.Done()
	var publicAddrs []*net.UDPAddr
	var publicType disco.NATType
	for _, addr := range c.localAddrs() {
		uaddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			slog.Error("Resolve local udp addr error", "err", err)
			continue
		}
		natType := disco.Internal
		if uaddr.IP.IsGlobalUnicast() && !uaddr.IP.IsPrivate() && !disco.IsCGN(uaddr.IP) {
			publicAddrs = append(publicAddrs, uaddr)
			if uaddr.IP.To4() != nil {
				natType = disco.IP4
				if publicType == "" {
					publicType = natType
				} else if publicType == disco.IP6 {
					publicType = disco.IP46
				}
			} else {
				natType = disco.IP6
				if publicType == "" {
					publicType = natType
				} else if publicType == disco.IP4 {
					publicType = disco.IP46
				}
			}
		}
		c.udpAddrSends <- &disco.PeerUDPAddr{
			ID:   peerID,
			Addr: uaddr,
			Type: natType,
		}
	}
	if publicAddrs != nil {
		select {
		case c.natEvents <- &disco.NATInfo{Type: publicType, Addrs: publicAddrs}:
		default:
		}
	}
}
