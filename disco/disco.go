package disco

import (
	"slices"
	"time"

	"github.com/rkonfj/peerguard/peer"
)

var defaultDiscoConfig = DiscoConfig{
	PortScanCount:             2000,
	ChallengesRetry:           3,
	ChallengesInitialInterval: 200 * time.Millisecond,
	ChallengesBackoffRate:     1.75,
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

type Disco struct {
	Magic func() []byte
}

func (d *Disco) NewPing(peerID peer.ID) []byte {
	return slices.Concat(d.magic(), peerID.Bytes())
}

func (d *Disco) ParsePing(b []byte) peer.ID {
	magic := d.magic()
	if len(b) <= len(magic) || len(b) > 255+len(magic) {
		return ""
	}
	if slices.Equal(magic, b[:len(magic)]) {
		return peer.ID(b[len(magic):])
	}
	return ""
}

func (d *Disco) magic() []byte {
	var magic []byte
	if d.Magic != nil {
		magic = d.Magic()
	}
	if len(magic) == 0 {
		return []byte("_ping")
	}
	return magic
}
