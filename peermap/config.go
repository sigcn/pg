package peermap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sigcn/pg/peermap/oidc"
	"gopkg.in/yaml.v2"
)

type RateLimiter struct {
	Limit int `yaml:"limit"`
	Burst int `yaml:"burst"`
}

type RateLimiterConfig struct {
	Limit   int         `yaml:"limit"`
	Burst   int         `yaml:"burst"`
	Relay   RateLimiter `yaml:"relay"`
	StreamR RateLimiter `yaml:"stream_r"`
	StreamW RateLimiter `yaml:"stream_w"`
}

func (c *RateLimiterConfig) check() error {
	if c.Relay.Burst == 0 && c.Burst > 0 {
		c.Relay.Burst = c.Burst
	}

	if c.Relay.Limit == 0 && c.Limit > 0 {
		c.Relay.Limit = c.Limit
	}

	if c.StreamR.Burst == 0 && c.Burst > 0 {
		c.StreamR.Burst = c.Burst
	}

	if c.StreamR.Limit == 0 && c.Limit > 0 {
		c.StreamR.Limit = c.Limit
	}

	if c.StreamW.Burst == 0 && c.Burst > 0 {
		c.StreamW.Burst = c.Burst
	}

	if c.StreamW.Limit == 0 && c.Limit > 0 {
		c.StreamW.Limit = c.Limit
	}

	if c.Relay.Burst < c.Relay.Limit {
		return errors.New("relay.burst must greater than relay.limit")
	}
	if c.Relay.Limit < 0 {
		return errors.New("relay.limit must greater than 0")
	}
	if c.StreamR.Burst < c.StreamR.Limit {
		return errors.New("stream_r.burst must greater than relay.limit")
	}
	if c.StreamR.Limit < 0 {
		return errors.New("stream_r.limit must greater than 0")
	}
	if c.StreamW.Burst < c.StreamW.Limit {
		return errors.New("stream_w.burst must greater than relay.limit")
	}
	if c.StreamW.Limit < 0 {
		return errors.New("stream_w.limit must greater than 0")
	}
	return nil
}

type Config struct {
	Listen               string                    `yaml:"listen"`
	SecretKey            string                    `yaml:"secret_key"`
	STUNs                []string                  `yaml:"stuns"`
	PublicNetwork        string                    `yaml:"public_network"`
	OIDCProviders        []oidc.OIDCProviderConfig `yaml:"oidc_providers"`
	RateLimiter          *RateLimiterConfig        `yaml:"rate_limiter,omitempty"`
	SecretRotationPeriod time.Duration             `yaml:"secret_rotation_period"`
	SecretValidityPeriod time.Duration             `yaml:"secret_validity_period"`
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
		if err := cfg.RateLimiter.check(); err != nil {
			return fmt.Errorf("ratelimiter: %w", err)
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
