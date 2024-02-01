package p2p

import (
	"errors"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/secure"
)

type Config struct {
	UDPPort     int
	PeerID      peer.PeerID
	DisableIPv6 bool
	DisableIPv4 bool
	PrivateKey  *secure.PrivateKey
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
		if cfg.PrivateKey != nil {
			return errors.New("options ListenPeerID and ListenPeerSecure/Curve25519 conflict")
		}
		peerID := peer.PeerID(id)
		if peerID.Len() > 0 {
			cfg.PeerID = peerID
		}
		return nil
	}
}

func ListenPeerSecure() Option {
	return func(cfg *Config) error {
		if cfg.PrivateKey != nil {
			return errors.New("repeat secure options")
		}
		priv, err := secure.GenerateCurve25519()
		if err != nil {
			return err
		}
		cfg.PrivateKey = priv
		cfg.PeerID = peer.PeerID(priv.PublicKey.String())
		return nil
	}
}

func ListenPeerCurve25519(privateKey string) Option {
	return func(cfg *Config) error {
		if cfg.PrivateKey != nil {
			return errors.New("repeat secure options")
		}
		priv, err := secure.Curve25519PrivateKey(privateKey)
		if err != nil {
			return err
		}
		cfg.PrivateKey = priv
		cfg.PeerID = peer.PeerID(priv.PublicKey.String())
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

func NetworkSecret(secret string) peer.NetworkSecret {
	return peer.NetworkSecret(secret)
}

func Peermap(servers ...string) peer.PeermapCluster {
	return peer.PeermapCluster(servers)
}
