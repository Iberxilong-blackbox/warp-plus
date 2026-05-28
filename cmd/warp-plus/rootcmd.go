package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/bepass-org/warp-plus/app"
	"github.com/bepass-org/warp-plus/egresscheck"
	p "github.com/bepass-org/warp-plus/psiphon"
	"github.com/bepass-org/warp-plus/warp"
	"github.com/bepass-org/warp-plus/wiresocks"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffval"
)

const defaultEgressBlacklistPath = "blacklist.example.txt"

type rootConfig struct {
	flags   *ff.FlagSet
	command *ff.Command

	verbose  bool
	v4       bool
	v6       bool
	bind     string
	endpoint string
	key      string
	dns      string
	gool     bool
	psiphon  bool
	country  string
	scan     bool
	rtt      time.Duration
	cacheDir string
	fwmark   uint32
	reserved string
	wgConf   string
	testUrl  string
	config   string

	egressCheck         bool
	egressIPURL         string
	egressBlacklistPath string
	egressScoreAPI      string
	egressScoreMax      int
	egressMaxRetry      int
	egressCheckInterval time.Duration

	controlBind          string
	controlToken         string
	controlPath          string
	controlRequireChange bool
	recentIPFile         string
	recentIPLimit        int
	poolMinReady         int

	child      bool
	childID    int
	childToken string
	parentAddr string
}

func newRootCmd() *rootConfig {
	var cfg rootConfig
	cfg.flags = ff.NewFlagSet(appName)
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: 'v',
		LongName:  "verbose",
		Value:     ffval.NewValueDefault(&cfg.verbose, false),
		Usage:     "enable verbose logging",
		NoDefault: true,
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: '4',
		Value:     ffval.NewValueDefault(&cfg.v4, false),
		Usage:     "only use IPv4 for random warp endpoint",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: '6',
		Value:     ffval.NewValueDefault(&cfg.v6, false),
		Usage:     "only use IPv6 for random warp endpoint",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: 'b',
		LongName:  "bind",
		Value:     ffval.NewValueDefault(&cfg.bind, "127.0.0.1:8086"),
		Usage:     "socks bind address",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: 'e',
		LongName:  "endpoint",
		Value:     ffval.NewValueDefault(&cfg.endpoint, ""),
		Usage:     "warp endpoint",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: 'k',
		LongName:  "key",
		Value:     ffval.NewValueDefault(&cfg.key, ""),
		Usage:     "warp key",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "dns",
		Value:    ffval.NewValueDefault(&cfg.dns, "1.1.1.1"),
		Usage:    "DNS address",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "gool",
		Value:    ffval.NewValueDefault(&cfg.gool, false),
		Usage:    "enable gool mode (warp in warp)",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "cfon",
		Value:    ffval.NewValueDefault(&cfg.psiphon, false),
		Usage:    "enable psiphon mode (must provide country as well)",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "country",
		Value:    ffval.NewEnum(&cfg.country, p.Countries...),
		Usage:    "psiphon country code",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "scan",
		Value:    ffval.NewValueDefault(&cfg.scan, false),
		Usage:    "enable warp scanning",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "rtt",
		Value:    ffval.NewValueDefault(&cfg.rtt, 1000*time.Millisecond),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "cache-dir",
		Value:    ffval.NewValueDefault(&cfg.cacheDir, ""),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "fwmark",
		Value:    ffval.NewValueDefault(&cfg.fwmark, 0x0),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "reserved",
		Value:    ffval.NewValueDefault(&cfg.reserved, ""),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "wgconf",
		Value:    ffval.NewValueDefault(&cfg.wgConf, ""),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "test-url",
		Value:    ffval.NewValueDefault(&cfg.testUrl, "http://connectivity.cloudflareclient.com/cdn-cgi/trace"),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		ShortName: 'c',
		LongName:  "config",
		Value:     ffval.NewValueDefault(&cfg.config, ""),
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName:  "egress-check",
		Value:     ffval.NewValueDefault(&cfg.egressCheck, false),
		Usage:     "enable final egress IP checking for cfon mode",
		NoDefault: true,
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "egress-ip-url",
		Value:    ffval.NewValueDefault(&cfg.egressIPURL, egresscheck.DefaultIPURL),
		Usage:    "URL that returns the current egress IP",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "egress-blacklist",
		Value:    ffval.NewValueDefault(&cfg.egressBlacklistPath, defaultEgressBlacklistPath),
		Usage:    "egress blacklist file path",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "egress-score-api",
		Value:    ffval.NewValueDefault(&cfg.egressScoreAPI, ""),
		Usage:    "egress score API URL; {ip} is replaced with the detected IP",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "egress-score-max",
		Value:    ffval.NewValueDefault(&cfg.egressScoreMax, -1),
		Usage:    "maximum allowed egress score",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "egress-max-retry",
		Value:    ffval.NewValueDefault(&cfg.egressMaxRetry, 3),
		Usage:    "maximum egress rebuild attempts during selection",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "egress-check-interval",
		Value:    ffval.NewValueDefault(&cfg.egressCheckInterval, 5*time.Minute),
		Usage:    "egress check interval while running; set 0 to disable monitoring",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "control-bind",
		Value:    ffval.NewValueDefault(&cfg.controlBind, ""),
		Usage:    "control API bind address; enables manual egress refresh when set",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "control-token",
		Value:    ffval.NewValueDefault(&cfg.controlToken, ""),
		Usage:    "bearer token for control API",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "control-path",
		Value:    ffval.NewValueDefault(&cfg.controlPath, "/connectivity/refresh"),
		Usage:    "control API refresh path",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "control-require-change",
		Value:    ffval.NewValueDefault(&cfg.controlRequireChange, true),
		Usage:    "require refreshed egress IP to differ from current IP",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "recent-ip-file",
		Value:    ffval.NewValueDefault(&cfg.recentIPFile, ""),
		Usage:    "recently used egress IP file",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "recent-ip-limit",
		Value:    ffval.NewValueDefault(&cfg.recentIPLimit, 50),
		Usage:    "maximum number of recent egress IPs to retain",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "pool-min-ready",
		Value:    ffval.NewValueDefault(&cfg.poolMinReady, 1),
		Usage:    "minimum ready children in the pool",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "parent-addr",
		Value:    ffval.NewValueDefault(&cfg.parentAddr, ""),
		Usage:    "parent registry address (internal use)",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName:  "child",
		Value:     ffval.NewValueDefault(&cfg.child, false),
		Usage:     "run in child process mode (internal use)",
		NoDefault: true,
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "child-id",
		Value:    ffval.NewValueDefault(&cfg.childID, 0),
		Usage:    "child process ID (internal use)",
	})
	cfg.flags.AddFlag(ff.FlagConfig{
		LongName: "child-token",
		Value:    ffval.NewValueDefault(&cfg.childToken, ""),
		Usage:    "child registration token (internal use)",
	})
	cfg.command = &ff.Command{
		Name:  appName,
		Flags: cfg.flags,
		Exec:  cfg.exec,
	}
	return &cfg
}

func (c *rootConfig) exec(ctx context.Context, args []string) error {
	l := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if c.verbose {
		l = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	// ---- Child mode: run a child process, then exit. ----
	if c.child {
		if c.parentAddr == "" {
			fatal(l, errors.New("--parent-addr is required in child mode"))
		}
		childCfg := app.ChildConfig{
			ID:         c.childID,
			Token:      c.childToken,
			ParentAddr: c.parentAddr,
		}
		return app.RunChild(ctx, l, c.buildWarpOptions(), childCfg)
	}

	if c.psiphon && c.gool {
		fatal(l, errors.New("can't use cfon and gool at the same time"))
	}

	if c.egressCheck && !c.psiphon {
		fatal(l, errors.New("egress-check is currently supported only with cfon mode"))
	}

	if c.controlBind != "" && (!c.psiphon || !c.egressCheck) {
		fatal(l, errors.New("control API is currently supported only with cfon and egress-check mode"))
	}

	if c.controlPath != "" && !strings.HasPrefix(c.controlPath, "/") {
		fatal(l, errors.New("control path must start with /"))
	}

	if c.recentIPLimit < 0 {
		fatal(l, errors.New("recent-ip-limit must be greater than or equal to 0"))
	}

	if c.v4 && c.v6 {
		fatal(l, errors.New("can't force v4 and v6 at the same time"))
	}

	if !c.v4 && !c.v6 {
		c.v4, c.v6 = true, true
	}

	opts := c.buildWarpOptions()

	go func() {
		if err := app.RunWarp(ctx, l, opts); err != nil {
			fatal(l, err)
		}
	}()

	<-ctx.Done()

	return nil
}

func (c *rootConfig) buildWarpOptions() app.WarpOptions {
	bindAddrPort, _ := netip.ParseAddrPort(c.bind)
	dnsAddr, _ := netip.ParseAddr(c.dns)

	opts := app.WarpOptions{
		Bind:            bindAddrPort,
		Endpoint:        c.endpoint,
		License:         c.key,
		DnsAddr:         dnsAddr,
		Gool:            c.gool,
		FwMark:          c.fwmark,
		WireguardConfig: c.wgConf,
		Reserved:        c.reserved,
		TestURL:         c.testUrl,
	}

	switch {
	case c.cacheDir != "":
		opts.CacheDir = c.cacheDir
	case xdg.CacheHome != "":
		opts.CacheDir = path.Join(xdg.CacheHome, appName)
	case os.Getenv("HOME") != "":
		opts.CacheDir = path.Join(os.Getenv("HOME"), ".cache", appName)
	default:
		opts.CacheDir = "warp_plus_cache"
	}

	if c.psiphon {
		opts.Psiphon = &app.PsiphonOptions{Country: c.country}
	}

	if c.egressCheck {
		blacklist, err := egresscheck.LoadBlacklist(c.egressBlacklistPath)
		if err != nil {
			fatal(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), fmt.Errorf("invalid egress blacklist: %w", err))
		}
		opts.EgressCheck = &egresscheck.Config{
			IPURL:         c.egressIPURL,
			Blacklist:     blacklist,
			BlacklistPath: c.egressBlacklistPath,
			ScoreAPI:      c.egressScoreAPI,
			ScoreMax:      c.egressScoreMax,
			MaxRetry:      c.egressMaxRetry,
			CheckInterval: c.egressCheckInterval,
		}
	}

	if c.controlBind != "" {
		controlBind, err := netip.ParseAddrPort(c.controlBind)
		if err != nil {
			fatal(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), fmt.Errorf("invalid control bind address: %w", err))
		}
		if !controlBind.Addr().IsLoopback() && c.controlToken == "" {
			fatal(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), errors.New("control-token is required when control-bind is not loopback"))
		}
		recentIPFile := c.recentIPFile
		if recentIPFile == "" {
			recentIPFile = path.Join(opts.CacheDir, "recent_ips.txt")
		}
		opts.RotateControl = &app.RotateControlOptions{
			Bind:          controlBind,
			Token:         c.controlToken,
			Path:          c.controlPath,
			RequireChange: c.controlRequireChange,
			RecentIPFile:  recentIPFile,
			RecentIPLimit: c.recentIPLimit,
			PoolMinReady:  c.poolMinReady,
			BlacklistPath: c.egressBlacklistPath,
		}
	}

	if c.scan {
		opts.Scan = &wiresocks.ScanOptions{V4: c.v4, V6: c.v6, MaxRTT: c.rtt}
	}

	if opts.Endpoint == "" {
		addrPort, err := warp.RandomWarpEndpoint(c.v4, c.v6)
		if err != nil {
			fatal(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), err)
		}
		opts.Endpoint = addrPort.String()
	}

	return opts
}
