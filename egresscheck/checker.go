package egresscheck

import (
	"context"
	"fmt"
	"net/netip"
)

type Checker struct {
	cfg Config
}

type Result struct {
	IP     netip.Addr
	Pass   bool
	Reason string
	Rule   string
	Score  *int
}

func New(cfg Config) (*Checker, error) {
	if cfg.IPURL == "" {
		cfg.IPURL = DefaultIPURL
	}
	if cfg.ScoreAPI != "" && cfg.ScoreMax < 0 {
		return nil, fmt.Errorf("egress score max must be set when score api is configured")
	}
	return &Checker{cfg: cfg}, nil
}

func (c *Checker) Check(ctx context.Context, proxyAddr netip.AddrPort) (Result, error) {
	ip, err := QueryIP(ctx, proxyAddr, c.cfg.IPURL)
	if err != nil {
		return Result{Pass: false, Reason: "ip_query_failed"}, err
	}

	if rule, ok := c.cfg.Blacklist.Match(ip); ok {
		return Result{IP: ip, Pass: false, Reason: "blacklist_matched", Rule: rule}, nil
	}

	if c.cfg.scoreEnabled() {
		score, err := FetchScore(ctx, c.cfg.ScoreAPI, ip)
		if err != nil {
			return Result{IP: ip, Pass: false, Reason: "score_failed"}, err
		}
		if score > c.cfg.ScoreMax {
			return Result{IP: ip, Pass: false, Reason: "score_exceeded", Score: &score}, nil
		}
		return Result{IP: ip, Pass: true, Reason: "accepted", Score: &score}, nil
	}

	return Result{IP: ip, Pass: true, Reason: "accepted"}, nil
}
