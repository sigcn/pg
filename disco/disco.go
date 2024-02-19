package disco

import "time"

var defaultDiscoConfig = DiscoConfig{
	PortScanCount:             1000,
	ChallengesRetry:           7,
	ChallengesInitialInterval: 300 * time.Millisecond,
	ChallengesBackoffRate:     1.35,
}

type DiscoConfig struct {
	PortScanCount             int
	ChallengesRetry           int
	ChallengesInitialInterval time.Duration
	ChallengesBackoffRate     float64
}

func SetModifyDiscoConfig(modify func(cfg *DiscoConfig)) {
	if modify != nil {
		modify(&defaultDiscoConfig)
	}
	defaultDiscoConfig.PortScanCount = min(max(32, defaultDiscoConfig.PortScanCount), 65535-1024)
	defaultDiscoConfig.ChallengesRetry = max(2, defaultDiscoConfig.ChallengesRetry)
	defaultDiscoConfig.ChallengesInitialInterval = max(10*time.Millisecond, defaultDiscoConfig.ChallengesInitialInterval)
	defaultDiscoConfig.ChallengesBackoffRate = max(1, defaultDiscoConfig.ChallengesBackoffRate)
}
