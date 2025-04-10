package p2p

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"net/url"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/secure"
	"github.com/sigcn/pg/secure/chacha20poly1305"
	"storj.io/common/base58"
)

var defaultSymmAlgo func(secure.ProvideSecretKey) secure.SymmAlgo = chacha20poly1305.New

func SetDefaultSymmAlgo(symmAlgo func(secure.ProvideSecretKey) secure.SymmAlgo) {
	defaultSymmAlgo = symmAlgo
}

type Config struct {
	UDPPort         int
	DisableIPv6     bool
	DisableIPv4     bool
	PeerInfo        disco.Peer
	SymmAlgo        secure.SymmAlgo
	OnPeer          OnPeer
	OnPeerLeave     OnPeerLeave
	KeepAlivePeriod time.Duration
	MinDiscoPeriod  time.Duration
}

type Option func(cfg *Config) error
type OnPeer func(disco.PeerID, url.Values)
type OnPeerLeave func(disco.PeerID)

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
		if cfg.SymmAlgo != nil {
			return errors.New("options ListenPeerID and ListenPeerSecure/Curve25519 conflict")
		}
		peerID := disco.PeerID(id)
		if peerID.Len() > 0 {
			cfg.PeerInfo.ID = peerID
		}
		return nil
	}
}

func ListenPeerSecure() Option {
	return func(cfg *Config) error {
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		return ListenPeerCurve25519(base58.Encode(priv.Bytes()))(cfg)
	}
}

func ListenPeerCurve25519(privateKey string) Option {
	return func(cfg *Config) error {
		if cfg.SymmAlgo != nil {
			return errors.New("repeat secure options")
		}
		curve := ecdh.X25519()
		priv, err := curve.NewPrivateKey(base58.Decode(privateKey))
		if err != nil {
			return err
		}
		cfg.SymmAlgo = defaultSymmAlgo(func(pubKey string) ([]byte, error) {
			pub, err := curve.NewPublicKey(base58.Decode(pubKey))
			if err != nil {
				return nil, err
			}
			return priv.ECDH(pub)
		})
		cfg.PeerInfo.ID = disco.PeerID(base58.Encode(priv.PublicKey().Bytes()))
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

func ListenPeerLeave(onPeerLeave OnPeerLeave) Option {
	return func(cfg *Config) error {
		cfg.OnPeerLeave = onPeerLeave
		return nil
	}
}

func PeerSilenceMode() Option {
	return func(cfg *Config) error {
		cfg.PeerInfo.WithSilenceMode()
		return nil
	}
}

func PeerAlias1(alias string) Option {
	return func(cfg *Config) error {
		cfg.PeerInfo.WithMeta("alias1", alias)
		return nil
	}
}

func PeerAlias2(alias string) Option {
	return func(cfg *Config) error {
		cfg.PeerInfo.WithMeta("alias2", alias)
		return nil
	}
}

func PeerMeta(key string, value string) Option {
	return func(cfg *Config) error {
		cfg.PeerInfo.WithMeta(key, value)
		return nil
	}
}

func KeepAlivePeriod(period time.Duration) Option {
	return func(cfg *Config) error {
		cfg.KeepAlivePeriod = period
		return nil
	}
}

func MinDiscoPeriod(period time.Duration) Option {
	return func(cfg *Config) error {
		cfg.MinDiscoPeriod = period
		return nil
	}
}

type TransportMode string

const (
	MODE_DEFAULT          TransportMode = ""
	MODE_FORCE_PEER_RELAY TransportMode = "PEER_RELAY"
	MODE_FORCE_RELAY      TransportMode = "RELAY"
)

func (mode TransportMode) String() string {
	if mode == "" {
		return "DEFAULT"
	}
	return string(mode)
}
