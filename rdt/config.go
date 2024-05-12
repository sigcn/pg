package rdt

import "time"

type Config struct {
	MTU               int
	Interval          time.Duration
	StatsServerListen string
}

type Option func(cfg *Config) error

func MTU(mtu int) Option {
	return func(cfg *Config) error {
		cfg.MTU = mtu
		return nil
	}
}

func StateInterval(interval time.Duration) Option {
	return func(cfg *Config) error {
		cfg.Interval = interval
		return nil
	}
}

func EnableStatsServer(listenAddr string) Option {
	return func(cfg *Config) error {
		cfg.StatsServerListen = listenAddr
		return nil
	}
}
