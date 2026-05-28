package egresscheck

import "time"

const DefaultIPURL = "https://api.ipify.org"

type Config struct {
	IPURL         string
	Blacklist     *Blacklist
	BlacklistPath string
	ScoreAPI      string
	ScoreMax      int
	MaxRetry      int
	CheckInterval time.Duration
	EventLogPath  string
}

func (c Config) scoreEnabled() bool {
	return c.ScoreAPI != "" && c.ScoreMax >= 0
}
