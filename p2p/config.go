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
	AES         *secure.AESCBC
	Metadata    peer.Metadata
	OnPeer      OnPeer
}

type Option func(cfg *Config) error
type OnPeer func(peer.PeerID, peer.Metadata)

var (
	OptionNoOp Option = func(cfg *Config) error { return nil }
)

func ListenUDPPort(port int) Option {
	return func(cfg *Config) error {
		cfg.UDPPort = port
		return nil
	}
}

func ListenPeerID(id string) Option {
	return func(cfg *Config) error {
		if cfg.AES != nil {
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
		if cfg.AES != nil {
			return errors.New("repeat secure options")
		}
		priv, err := secure.GenerateCurve25519()
		if err != nil {
			return err
		}
		cfg.AES = secure.NewAESCBC(priv)
		cfg.PeerID = peer.PeerID(priv.PublicKey.String())
		return nil
	}
}

func ListenPeerCurve25519(privateKey string) Option {
	return func(cfg *Config) error {
		if cfg.AES != nil {
			return errors.New("repeat secure options")
		}
		priv, err := secure.Curve25519PrivateKey(privateKey)
		if err != nil {
			return err
		}
		cfg.AES = secure.NewAESCBC(priv)
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

func ListenPeerUp(onPeer OnPeer) Option {
	return func(cfg *Config) error {
		cfg.OnPeer = onPeer
		return nil
	}
}

func NetworkSecret(secret string) peer.NetworkSecret {
	return peer.NetworkSecret(secret)
}

func Peermap(servers ...string) peer.PeermapCluster {
	return peer.PeermapCluster(servers)
}

func PeerSilenceMode() Option {
	return func(cfg *Config) error {
		cfg.Metadata.SilenceMode = true
		return nil
	}
}

func PeerAlias1(alias string) Option {
	return func(cfg *Config) error {
		cfg.Metadata.Alias1 = alias
		return nil
	}
}

func PeerAlias2(alias string) Option {
	return func(cfg *Config) error {
		cfg.Metadata.Alias2 = alias
		return nil
	}
}

func PeerMeta(key string, value any) Option {
	return func(cfg *Config) error {
		if cfg.Metadata.Extra == nil {
			cfg.Metadata.Extra = make(map[string]any)
		}
		cfg.Metadata.Extra[key] = value
		return nil
	}
}
