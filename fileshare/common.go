package fileshare

import (
	"cmp"
	"fmt"
	"io"
	"net"
	"net/url"

	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/p2p"
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
	network := cmp.Or(pn.Name, "pubnet")
	pmap, err := disco.NewPeermap(pmapURL, &disco.NetworkSecret{Network: network, Secret: network})
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

type ProgressBar interface {
	io.Writer
	Add(progress int) error
}

type NopProgress struct {
}

func (w NopProgress) Write(p []byte) (n int, err error) {
	n = len(p)
	return
}

func (w NopProgress) Add(int) error { return nil }
