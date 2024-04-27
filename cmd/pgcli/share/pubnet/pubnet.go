package pubnet

import (
	"fmt"
	"net"
	"net/url"

	"github.com/rkonfj/peerguard/p2p"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peer/peermap"
)

type PublicNetwork struct {
	Name       string
	Server     string
	PrivateKey string
}

func (pn *PublicNetwork) ListenPacket(udpPort int) (net.PacketConn, error) {
	pmapURL, err := url.Parse(pn.Server)
	if err != nil {
		return nil, fmt.Errorf("invalid peermap URL: %w", err)
	}

	pmap, err := peermap.New(pmapURL, &peer.NetworkSecret{
		Network: pn.Name,
		Secret:  pn.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("create peermap failed: %w", err)
	}

	return p2p.ListenPacket(pmap, pn.secureOption(), p2p.ListenUDPPort(udpPort))
}

func (pn *PublicNetwork) secureOption() p2p.Option {
	if len(pn.PrivateKey) > 0 {
		return p2p.ListenPeerCurve25519(pn.PrivateKey)
	}
	return p2p.ListenPeerSecure()
}
