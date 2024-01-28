package p2p

import "github.com/rkonfj/peerguard/peer"

type Config struct {
	UDPPort     int
	PeerID      peer.PeerID
	DisableIPv6 bool
	DisableIPv4 bool
}

type Option func(cfg *Config) error

func ListenUDPPort(port int) Option {
	return func(cfg *Config) error {
		cfg.UDPPort = port
		return nil
	}
}

func ListenPeerID(id string) Option {
	return func(cfg *Config) error {
		peerID := peer.PeerID(id)
		if peerID.Len() > 0 {
			cfg.PeerID = peerID
		}
		return nil
	}
}

func ListenIPv6Only() Option {
	return func(cfg *Config) error {
		cfg.DisableIPv4 = true
		cfg.DisableIPv6 = false
		return nil
	}
}

func ListenIPv4Only() Option {
	return func(cfg *Config) error {
		cfg.DisableIPv4 = false
		cfg.DisableIPv6 = true
		return nil
	}
}

func Nwtwork(network string) peer.NwtworkSecret {
	return peer.NwtworkSecret(network)
}

func Peermap(servers ...string) peer.PeermapCluster {
	return peer.PeermapCluster(servers)
}
