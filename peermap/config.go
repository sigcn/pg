package peermap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/rkonfj/peerguard/peermap/oidc"
	"gopkg.in/yaml.v2"
)

type RateLimiter struct {
	Limit int
	Burst int
}

type Config struct {
	Listen               string                    `yaml:"listen"`
	SecretKey            string                    `yaml:"secret_key"`
	STUNs                []string                  `yaml:"stuns"`
	PublicNetwork        string                    `yaml:"public_network"`
	OIDCProviders        []oidc.OIDCProviderConfig `yaml:"oidc_providers"`
	RateLimiter          *RateLimiter              `yaml:"rate_limiter,omitempty"`
	SecretRotationPeriod time.Duration             `yaml:"secret_rotation_period"`
	SecretValidityPeriod time.Duration             `yaml:"secret_validity_period"`
	StateFile            string                    `yaml:"state_file"`
}

func (cfg *Config) applyDefaults() error {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:9987"
	}
	if cfg.SecretKey == "" {
		secretKey := make([]byte, 16)
		rand.Read(secretKey)
		cfg.SecretKey = hex.EncodeToString(secretKey)
		slog.Info("SecretKey " + cfg.SecretKey)
	}
	if len(cfg.STUNs) == 0 {
		slog.Warn("No STUN servers is set up, NAT traversal is disabled")
	}
	if cfg.RateLimiter != nil {
		if cfg.RateLimiter.Burst < cfg.RateLimiter.Limit {
			return errors.New("burst must greater than limit")
		}
		if cfg.RateLimiter.Limit < 0 {
			return errors.New("limit must greater than 0")
		}
	}
	if cfg.SecretValidityPeriod == 0 {
		cfg.SecretValidityPeriod = 4 * time.Hour
	}
	if cfg.SecretRotationPeriod == 0 {
		cfg.SecretRotationPeriod = max(cfg.SecretValidityPeriod-time.Hour, time.Minute)
	}
	if cfg.SecretRotationPeriod >= cfg.SecretValidityPeriod {
		return errors.New("secret rotation period must less than validity period")
	}
	if cfg.StateFile == "" {
		cfg.StateFile = "state.json"
	}
	for _, provider := range cfg.OIDCProviders {
		oidc.AddProvider(provider)
	}
	return nil
}

func (cfg *Config) Overwrite(cfg1 Config) {
	if len(cfg1.SecretKey) > 0 {
		cfg.SecretKey = cfg1.SecretKey
	}
	if len(cfg1.Listen) > 0 {
		cfg.Listen = cfg1.Listen
	}
	if len(cfg1.STUNs) > 0 {
		cfg.STUNs = cfg1.STUNs
	}
	if len(cfg1.PublicNetwork) > 0 {
		cfg.PublicNetwork = cfg1.PublicNetwork
	}
}

func ReadConfig(configFile string) (cfg Config, err error) {
	f, err := os.Open(configFile)
	if err != nil {
		return
	}
	defer f.Close()
	err = yaml.NewDecoder(f).Decode(&cfg)
	return
}
