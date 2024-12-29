package udp

import (
	"time"

	"github.com/sigcn/pg/disco"
)

var defaultDiscoConfig = DiscoConfig{
	PortScanOffset:            -1000,
	PortScanCount:             3000,
	PortScanDuration:          5 * time.Second,
	ChallengesRetry:           5,
	ChallengesInitialInterval: 200 * time.Millisecond,
	ChallengesBackoffRate:     1.65,
}

type DiscoConfig struct {
	PortScanOffset            int           `yaml:"port_scan_offset"`
	PortScanCount             int           `yaml:"port_scan_count"`
	PortScanDuration          time.Duration `yaml:"port_scan_duration"`
	ChallengesRetry           int           `yaml:"challenges_retry"`
	ChallengesInitialInterval time.Duration `yaml:"challenges_initial_interval"`
	ChallengesBackoffRate     float64       `yaml:"challenges_backoff_rate"`
	IgnoredInterfaces         []string      `yaml:"ignored_interfaces"`
}

func SetModifyDiscoConfig(modify func(cfg *DiscoConfig)) {
	if modify != nil {
		modify(&defaultDiscoConfig)
	}
	defaultDiscoConfig.PortScanOffset = max(min(defaultDiscoConfig.PortScanOffset, 65535), -65535)
	defaultDiscoConfig.PortScanCount = min(max(32, defaultDiscoConfig.PortScanCount), 65535-1024)
	defaultDiscoConfig.PortScanDuration = max(time.Second, defaultDiscoConfig.PortScanDuration)
	defaultDiscoConfig.ChallengesRetry = max(1, defaultDiscoConfig.ChallengesRetry)
	defaultDiscoConfig.ChallengesInitialInterval = max(10*time.Millisecond, defaultDiscoConfig.ChallengesInitialInterval)
	defaultDiscoConfig.ChallengesBackoffRate = max(1, defaultDiscoConfig.ChallengesBackoffRate)
}

type UDPConfig struct {
	Port                  int
	DisableIPv4           bool
	DisableIPv6           bool
	ID                    disco.PeerID
	PeerKeepaliveInterval time.Duration
	DiscoMagic            func() []byte
}
