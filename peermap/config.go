package peermap

import (
	"errors"
	"os"

	"github.com/rkonfj/peerguard/peermap/oidc"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Listen        string                    `yaml:"listen"`
	AdvertiseURL  string                    `yaml:"advertise_url"`
	ClusterKey    string                    `yaml:"cluster_key"`
	STUNs         []string                  `yaml:"stuns"`
	OIDCProviders []oidc.OIDCProviderConfig `yaml:"oidc_providers"`
}

func (cfg *Config) applyDefaults() error {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:9987"
	}
	if cfg.ClusterKey == "" {
		return errors.New("cluster key is required")
	}
	for _, provider := range cfg.OIDCProviders {
		oidc.AddProvider(provider)
	}
	return nil
}

func (cfg *Config) Overwrite(cfg1 Config) {
	if len(cfg1.AdvertiseURL) > 0 {
		cfg.AdvertiseURL = cfg1.AdvertiseURL
	}
	if len(cfg1.ClusterKey) > 0 {
		cfg.ClusterKey = cfg1.ClusterKey
	}
	if len(cfg1.Listen) > 0 {
		cfg.Listen = cfg1.Listen
	}
	if len(cfg1.STUNs) > 0 {
		cfg.STUNs = cfg1.STUNs
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
