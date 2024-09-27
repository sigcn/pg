package p2p

import (
	"errors"
	"net/url"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/secure"
	"github.com/sigcn/pg/secure/chacha20poly1305"
)

var defaultSymmAlgo func(secure.ProvideSecretKey) secure.SymmAlgo = chacha20poly1305.New

func SetDefaultSymmAlgo(symmAlgo func(secure.ProvideSecretKey) secure.SymmAlgo) {
	defaultSymmAlgo = symmAlgo
}

type Config struct {
	UDPPort         int
	PeerID          disco.PeerID
	DisableIPv6     bool
	DisableIPv4     bool
	SymmAlgo        secure.SymmAlgo
	Metadata        url.Values
	OnPeer          OnPeer
	KeepAlivePeriod time.Duration
	MinDiscoPeriod  time.Duration
}

type Option func(cfg *Config) error
type OnPeer func(disco.PeerID, url.Values)

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
			cfg.PeerID = peerID
		}
		return nil
	}
}

func ListenPeerSecure() Option {
	return func(cfg *Config) error {
		priv, err := secure.GenerateCurve25519()
		if err != nil {
			return err
		}
		return ListenPeerCurve25519(priv.String())(cfg)
	}
}

func ListenPeerCurve25519(privateKey string) Option {
	return func(cfg *Config) error {
		if cfg.SymmAlgo != nil {
			return errors.New("repeat secure options")
		}
		priv, err := secure.Curve25519PrivateKey(privateKey)
		if err != nil {
			return err
		}
		cfg.SymmAlgo = defaultSymmAlgo(priv.SharedKey)
		cfg.PeerID = disco.PeerID(priv.PublicKey.String())
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

func FileSecretStore(storeFilePath string) disco.SecretStore {
	return &disco.FileSecretStore{StoreFilePath: storeFilePath}
}

func PeerSilenceMode() Option {
	return func(cfg *Config) error {
		if cfg.Metadata == nil {
			cfg.Metadata = url.Values{}
		}
		cfg.Metadata.Set("silenceMode", "")
		return nil
	}
}

func PeerAlias1(alias string) Option {
	return func(cfg *Config) error {
		if cfg.Metadata == nil {
			cfg.Metadata = url.Values{}
		}
		cfg.Metadata.Set("alias1", alias)
		return nil
	}
}

func PeerAlias2(alias string) Option {
	return func(cfg *Config) error {
		if cfg.Metadata == nil {
			cfg.Metadata = url.Values{}
		}
		cfg.Metadata.Set("alias2", alias)
		return nil
	}
}

func PeerMeta(key string, value string) Option {
	return func(cfg *Config) error {
		if cfg.Metadata == nil {
			cfg.Metadata = url.Values{}
		}
		cfg.Metadata.Add(key, value)
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
