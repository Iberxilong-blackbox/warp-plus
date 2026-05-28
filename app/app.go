package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"path"
	"sync"
	"time"

	"github.com/bepass-org/warp-plus/egresscheck"
	"github.com/bepass-org/warp-plus/iputils"
	"github.com/bepass-org/warp-plus/psiphon"
	"github.com/bepass-org/warp-plus/warp"
	"github.com/bepass-org/warp-plus/wireguard/tun"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
	"github.com/bepass-org/warp-plus/wiresocks"
)

const singleMTU = 1330
const doubleMTU = 1280 // minimum mtu for IPv6, may cause frag reassembly somewhere

type WarpOptions struct {
	Bind            netip.AddrPort
	Endpoint        string
	License         string
	DnsAddr         netip.Addr
	Psiphon         *PsiphonOptions
	Gool            bool
	Scan            *wiresocks.ScanOptions
	CacheDir        string
	FwMark          uint32
	WireguardConfig string
	Reserved        string
	TestURL         string
	EgressCheck     *egresscheck.Config
	RotateControl   *RotateControlOptions
}

type PsiphonOptions struct {
	Country string
}

type RotateControlOptions struct {
	Bind          netip.AddrPort
	Token         string
	Path          string
	RequireChange bool
	RecentIPFile  string
	RecentIPLimit int
	PoolMinReady  int
	BlacklistPath string
}

func RunWarp(ctx context.Context, l *slog.Logger, opts WarpOptions) error {
	if opts.WireguardConfig != "" {
		if err := runWireguard(ctx, l, opts); err != nil {
			return err
		}

		return nil
	}

	if opts.Psiphon != nil && opts.Gool {
		return errors.New("can't use psiphon and gool at the same time")
	}

	if opts.Psiphon != nil && opts.Psiphon.Country == "" {
		return errors.New("must provide country for psiphon")
	}

	// Decide Working Scenario
	endpoints := []string{opts.Endpoint, opts.Endpoint}

	if opts.Scan != nil {
		// make primary identity
		ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
		if err != nil {
			l.Error("couldn't load primary warp identity")
			return err
		}

		// Reading the private key from the 'Interface' section
		opts.Scan.PrivateKey = ident.PrivateKey

		// Reading the public key from the 'Peer' section
		opts.Scan.PublicKey = ident.Config.Peers[0].PublicKey

		res, err := wiresocks.RunScan(ctx, l, *opts.Scan)
		if err != nil {
			return err
		}

		l.Debug("scan results", "endpoints", res)

		endpoints = make([]string, len(res))
		for i := 0; i < len(res); i++ {
			endpoints[i] = res[i].AddrPort.String()
		}
	}
	l.Info("using warp endpoints", "endpoints", endpoints)

	var warpErr error
	switch {
	case opts.Psiphon != nil && opts.RotateControl != nil:
		l.Info("running in Psiphon pool mode")
		warpErr = runWarpWithPool(ctx, l, opts)
	case opts.Psiphon != nil:
		l.Info("running in Psiphon (cfon) mode")
		// run primary warp on a random tcp port and run psiphon on bind address
		warpErr = runWarpWithPsiphon(ctx, l, opts, endpoints[0])
	case opts.Gool:
		l.Info("running in warp-in-warp (gool) mode")
		// run warp in warp
		warpErr = runWarpInWarp(ctx, l, opts, endpoints)
	default:
		l.Info("running in normal warp mode")
		// just run primary warp on bindAddress
		warpErr = runWarp(ctx, l, opts, endpoints[0])
	}

	return warpErr
}

func runWireguard(ctx context.Context, l *slog.Logger, opts WarpOptions) error {
	conf, err := wiresocks.ParseConfig(opts.WireguardConfig)
	if err != nil {
		return err
	}

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Trick = true
		peer.KeepAlive = 5

		// Try resolving if the endpoint is a domain
		addr, err := iputils.ParseResolveAddressPort(peer.Endpoint, false, opts.DnsAddr.String())
		if err == nil {
			peer.Endpoint = addr.String()
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack
	var werr error
	var tnet *netstack.Net
	var tunDev tun.Device
	for _, t := range []string{"t1", "t2"} {
		// Create userspace tun network stack
		tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		werr = establishWireguard(l, conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Run a proxy on the userspace stack
	_, err = wiresocks.StartProxy(ctx, l, tnet, opts.Bind)
	if err != nil {
		return err
	}

	l.Info("serving proxy", "address", opts.Bind)

	return nil
}

func runWarp(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoint string) error {
	// make primary identity
	ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
	if err != nil {
		l.Error("couldn't load primary warp identity")
		return err
	}

	conf := generateWireguardConfig(ident)

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = endpoint
		peer.Trick = true
		peer.KeepAlive = 5

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack
	var werr error
	var tnet *netstack.Net
	var tunDev tun.Device
	for _, t := range []string{"t1", "t2"} {
		tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		werr = establishWireguard(l, &conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Run a proxy on the userspace stack
	_, err = wiresocks.StartProxy(ctx, l, tnet, opts.Bind)
	if err != nil {
		return err
	}

	l.Info("serving proxy", "address", opts.Bind)
	return nil
}

func runWarpInWarp(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoints []string) error {
	// make primary identity
	ident1, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
	if err != nil {
		l.Error("couldn't load primary warp identity")
		return err
	}

	conf := generateWireguardConfig(ident1)

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = endpoints[0]
		peer.Trick = true
		peer.KeepAlive = 5

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack and bind the wireguard sockets to the default interface and apply
	var werr error
	var tnet1 *netstack.Net
	var tunDev tun.Device
	for _, t := range []string{"t1", "t2"} {
		// Create userspace tun network stack
		tunDev, tnet1, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		werr = establishWireguard(l.With("gool", "outer"), &conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet1, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Create a UDP port forward between localhost and the remote endpoint
	addr, err := wiresocks.NewVtunUDPForwarder(ctx, netip.MustParseAddrPort("127.0.0.1:0"), endpoints[0], tnet1, singleMTU)
	if err != nil {
		return err
	}

	// make secondary
	ident2, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "secondary"), opts.License)
	if err != nil {
		l.Error("couldn't load secondary warp identity")
		return err
	}

	conf = generateWireguardConfig(ident2)

	// Set up MTU
	conf.Interface.MTU = doubleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = addr.String()
		peer.KeepAlive = 20

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Create userspace tun network stack
	tunDev, tnet2, err := netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
	if err != nil {
		return err
	}

	// Establish wireguard on userspace stack
	if err := establishWireguard(l.With("gool", "inner"), &conf, tunDev, opts.FwMark, "t0"); err != nil {
		return err
	}

	// Test wireguard connectivity
	if err := usermodeTunTest(ctx, l, tnet2, opts.TestURL); err != nil {
		return err
	}

	_, err = wiresocks.StartProxy(ctx, l, tnet2, opts.Bind)
	if err != nil {
		return err
	}

	l.Info("serving proxy", "address", opts.Bind)
	return nil
}

func runWarpWithPsiphon(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoint string) error {
	// make primary identity
	ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
	if err != nil {
		l.Error("couldn't load primary warp identity")
		return err
	}

	conf := generateWireguardConfig(ident)

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = endpoint
		peer.Trick = true
		peer.KeepAlive = 5

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack
	var werr error
	var tnet *netstack.Net
	var tunDev tun.Device
	for _, t := range []string{"t1", "t2"} {
		// Create userspace tun network stack
		tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		werr = establishWireguard(l, &conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Run a proxy on the userspace stack
	warpBind, err := wiresocks.StartProxy(ctx, l, tnet, netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		return err
	}

	if opts.EgressCheck != nil {
		return runWarpWithCheckedPsiphon(ctx, l, opts, warpBind)
	}

	// run psiphon
	err = psiphon.RunPsiphon(ctx, l.With("subsystem", "psiphon"), warpBind, opts.CacheDir, opts.Bind, opts.Psiphon.Country)
	if err != nil {
		return fmt.Errorf("unable to run psiphon %w", err)
	}

	l.Info("serving proxy", "address", opts.Bind)
	return nil
}

func runWarpWithPool(ctx context.Context, l *slog.Logger, opts WarpOptions) error {
	checker, err := egresscheck.New(*opts.EgressCheck)
	if err != nil {
		return err
	}

	recent, err := egresscheck.LoadRecentIPs(opts.RotateControl.recentIPFile(), opts.RotateControl.recentIPLimit())
	if err != nil {
		return err
	}

	// Create relay with a placeholder upstream; the pool sets the real upstream
	// once the first child registers.
	relay, relayAddr, err := startTCPRelay(ctx, l, opts.Bind, netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		return fmt.Errorf("pool: start relay: %w", err)
	}

	l.Info("serving proxy", "address", relayAddr)

	pool := newChildPool(l, opts, relay, recent, opts.RotateControl.RequireChange)
	if err := pool.start(ctx); err != nil {
		relay.listener.Close()
		pool.shutdown()
		return fmt.Errorf("pool: start: %w", err)
	}

	// Start control API server.
	if err := startRotateControlServer(ctx, l, opts.RotateControl, pool); err != nil {
		return fmt.Errorf("pool: start control server: %w", err)
	}

	// Periodic egress monitoring (optional).
	interval := opts.EgressCheck.CheckInterval
	if interval <= 0 {
		<-ctx.Done()
		pool.shutdown()
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			pool.shutdown()
			return nil
		case <-ticker.C:
			currentIP := pool.currentIP()
			if !currentIP.IsValid() {
				continue
			}

			// Re-check current egress through the relay.
			checkResult, err := checker.Check(ctx, relayAddr)
			if err != nil {
				l.Warn("egress monitor check failed", "error", err)
				continue
			}
			if checkResult.IP.IsValid() && checkResult.IP != currentIP {
				l.Info("egress ip changed", "old", currentIP, "new", checkResult.IP)
			}
			if checkResult.Pass {
				if recent != nil && checkResult.IP != currentIP && recent.Contains(checkResult.IP) {
					checkResult.Pass = false
					checkResult.Reason = "recent_ip_matched"
				}
			}
			if checkResult.Pass {
				logEgressAccepted(l, checkResult)
				continue
			}

			logEgressRejected(l, checkResult, 0)
			status := pool.rotate(ctx)
			if status != rotateStatusOK {
				l.Warn("egress monitor rotate failed", "status", status)
			}
		}
	}
}

// rotate is the pool-based rotation used by the control API and monitor.
func (p *childPool) rotate(ctx context.Context) rotateStatus {
	if !p.rotateMu.TryLock() {
		return rotateStatusBusy
	}
	defer p.rotateMu.Unlock()

	p.mu.Lock()
	readyCount := len(p.readyOrder)
	p.mu.Unlock()

	if readyCount == 0 {
		// No ready child — trigger pool maintenance to start a new one.
		select {
		case p.wakeMaintain <- struct{}{}:
		default:
		}
		return rotateStatusBusy
	}

	child, oldIP, status := p.acquireReady()
	if child == nil {
		return status
	}

	// Record the old IP so later rotations avoid immediately returning to it.
	if p.recent != nil && oldIP.IsValid() {
		if err := p.recent.Add(oldIP); err != nil {
			p.logger.Warn("unable to update recent ip file", "error", err)
		}
	}

	// Trigger replenishment.
	select {
	case p.wakeMaintain <- struct{}{}:
	default:
	}

	return rotateStatusOK
}

type checkedPsiphon struct {
	logger   *slog.Logger
	warpBind netip.AddrPort
	opts     WarpOptions
	checker  *egresscheck.Checker
	relay    *tcpRelay
	recent   *egresscheck.RecentIPs

	mu       sync.Mutex
	current  checkedTunnel
	hasState bool
}

type checkedTunnel struct {
	tunnel *psiphon.Tunnel
	addr   netip.AddrPort
	result egresscheck.Result
}

func runWarpWithCheckedPsiphon(ctx context.Context, l *slog.Logger, opts WarpOptions, warpBind netip.AddrPort) error {
	checker, err := egresscheck.New(*opts.EgressCheck)
	if err != nil {
		return err
	}

	recent, err := egresscheck.LoadRecentIPs(opts.RotateControl.recentIPFile(), opts.RotateControl.recentIPLimit())
	if err != nil {
		return err
	}

	manager := &checkedPsiphon{
		logger:   l,
		warpBind: warpBind,
		opts:     opts,
		checker:  checker,
		recent:   recent,
	}

	current, err := manager.startAccepted(ctx, false, netip.Addr{}, false)
	if err != nil {
		return err
	}
	defer func() {
		manager.closeCurrent()
	}()

	relay, relayAddr, err := startTCPRelay(ctx, l, opts.Bind, current.addr)
	if err != nil {
		current.tunnel.Close()
		return err
	}
	manager.relay = relay
	manager.current = current
	manager.hasState = true
	if err := manager.remember(current.result); err != nil {
		current.tunnel.Close()
		return err
	}

	l.Info("serving proxy", "address", relayAddr)

	interval := opts.EgressCheck.CheckInterval
	if interval <= 0 {
		<-ctx.Done()
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current := manager.snapshot()
			result, err := checker.Check(ctx, current.addr)
			if err != nil {
				l.Warn("egress check failed", "reason", result.Reason, "error", err)
				result.Pass = false
			}
			if result.IP.IsValid() && result.IP != current.result.IP {
				l.Info("egress ip changed", "old", current.result.IP, "new", result.IP)
			}
			if result.Pass && manager.recent != nil && result.IP != current.result.IP && manager.recent.Contains(result.IP) {
				result.Pass = false
				result.Reason = "recent_ip_matched"
			}
			if result.Pass {
				manager.updateResult(result)
				if err := manager.remember(result); err != nil {
					l.Warn("unable to update recent ip file", "error", err)
				}
				logEgressAccepted(l, result)
				continue
			}

			logEgressRejected(l, result, 0)
			status := manager.rotate(ctx, false)
			if status != rotateStatusOK {
				return fmt.Errorf("unable to rotate after egress monitor failure: %s", status)
			}
		}
	}
}

func (m *checkedPsiphon) startAccepted(ctx context.Context, filterRecent bool, oldIP netip.Addr, requireChange bool) (checkedTunnel, error) {
	maxRetry := m.opts.EgressCheck.MaxRetry
	if maxRetry <= 0 {
		maxRetry = 3
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetry; attempt++ {
		startedAt := time.Now()
		m.logger.Info("starting egress candidate", "attempt", attempt, "max_retry", maxRetry)
		addr, err := freeLocalAddr()
		if err != nil {
			return checkedTunnel{}, err
		}

		handshakeStartedAt := time.Now()
		tunnel, err := psiphon.StartPsiphon(ctx, m.logger.With("subsystem", "psiphon"), m.warpBind, m.opts.CacheDir, addr, m.opts.Psiphon.Country)
		if err != nil {
			lastErr = err
			m.logger.Warn("egress candidate handshake failed", "attempt", attempt, "duration", time.Since(handshakeStartedAt), "error", err)
			continue
		}
		m.logger.Info("egress candidate handshake succeeded", "attempt", attempt, "duration", time.Since(handshakeStartedAt), "local_addr", addr)

		checkStartedAt := time.Now()
		result, err := m.checker.Check(ctx, addr)
		if err != nil {
			lastErr = err
			m.logger.Warn("egress check failed", "reason", result.Reason, "retry", attempt, "duration", time.Since(checkStartedAt), "error", err)
			tunnel.Close()
			continue
		}
		m.logger.Info("egress candidate checked", "attempt", attempt, "duration", time.Since(checkStartedAt), "ip", result.IP, "pass", result.Pass, "reason", result.Reason)
		if result.Pass {
			if requireChange && result.IP.IsValid() && result.IP == oldIP {
				result.Pass = false
				result.Reason = "same_ip"
				logEgressRejected(m.logger, result, attempt, time.Since(startedAt))
				tunnel.Close()
				continue
			}
			if m.recent != nil && m.recent.Contains(result.IP) {
				if filterRecent {
					result.Pass = false
					result.Reason = "recent_ip_matched"
					logEgressRejected(m.logger, result, attempt, time.Since(startedAt))
					tunnel.Close()
					continue
				}
			}
			logEgressAccepted(m.logger, result, time.Since(startedAt))
			return checkedTunnel{tunnel: tunnel, addr: addr, result: result}, nil
		}

		logEgressRejected(m.logger, result, attempt, time.Since(startedAt))
		tunnel.Close()
	}

	if lastErr != nil {
		return checkedTunnel{}, fmt.Errorf("unable to find acceptable egress after %d attempts: %w", maxRetry, lastErr)
	}
	return checkedTunnel{}, fmt.Errorf("unable to find acceptable egress after %d attempts", maxRetry)
}

type rotateStatus string

const (
	rotateStatusOK                 rotateStatus = "ok"
	rotateStatusBusy               rotateStatus = "busy"
	rotateStatusNoAcceptableEgress rotateStatus = "no_acceptable_egress"
	rotateStatusRefreshFailed      rotateStatus = "refresh_failed"
)

func (m *checkedPsiphon) rotate(ctx context.Context, forceChange bool) rotateStatus {
	if !m.mu.TryLock() {
		return rotateStatusBusy
	}
	defer m.mu.Unlock()

	old := m.current
	old.tunnel.Close()

	next, err := m.startAccepted(ctx, true, old.result.IP, forceChange)
	if err != nil {
		m.logger.Warn("manual egress refresh failed", "error", err)
		m.hasState = false
		return rotateStatusNoAcceptableEgress
	}

	m.relay.setUpstream(next.addr)
	m.current = next
	m.hasState = true
	if err := m.remember(next.result); err != nil {
		m.logger.Warn("unable to update recent ip file", "error", err)
	}
	return rotateStatusOK
}

func (m *checkedPsiphon) snapshot() checkedTunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

func (m *checkedPsiphon) updateResult(result egresscheck.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current.result = result
}

func (m *checkedPsiphon) closeCurrent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hasState {
		m.current.tunnel.Close()
		m.hasState = false
	}
}

func (m *checkedPsiphon) remember(result egresscheck.Result) error {
	if m.recent == nil || !result.IP.IsValid() {
		return nil
	}
	return m.recent.Add(result.IP)
}

func (o *WarpOptions) blacklistPath() string {
	if o == nil || o.RotateControl == nil {
		return ""
	}
	return o.RotateControl.BlacklistPath
}

func (o *RotateControlOptions) recentIPFile() string {
	if o == nil {
		return ""
	}
	return o.RecentIPFile
}

func (o *RotateControlOptions) recentIPLimit() int {
	if o == nil {
		return 0
	}
	return o.RecentIPLimit
}

func freeLocalAddr() (netip.AddrPort, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return netip.AddrPort{}, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).AddrPort(), nil
}

func logEgressAccepted(l *slog.Logger, result egresscheck.Result, duration ...time.Duration) {
	args := []any{"ip", result.IP}
	if result.Score != nil {
		args = append(args, "score", *result.Score)
	}
	if len(duration) > 0 {
		args = append(args, "duration", duration[0])
	}
	l.Info("egress accepted", args...)
}

func logEgressRejected(l *slog.Logger, result egresscheck.Result, retry int, duration ...time.Duration) {
	args := []any{"ip", result.IP, "reason", result.Reason, "retry", retry}
	if result.Rule != "" {
		args = append(args, "rule", result.Rule)
	}
	if result.Score != nil {
		args = append(args, "score", *result.Score)
	}
	if len(duration) > 0 {
		args = append(args, "duration", duration[0])
	}
	l.Info("egress rejected", args...)
}

func generateWireguardConfig(i *warp.Identity) wiresocks.Configuration {
	priv, _ := wiresocks.EncodeBase64ToHex(i.PrivateKey)
	pub, _ := wiresocks.EncodeBase64ToHex(i.Config.Peers[0].PublicKey)
	clientID, _ := base64.StdEncoding.DecodeString(i.Config.ClientID)
	return wiresocks.Configuration{
		Interface: &wiresocks.InterfaceConfig{
			PrivateKey: priv,
			Addresses: []netip.Addr{
				netip.MustParseAddr(i.Config.Interface.Addresses.V4),
				netip.MustParseAddr(i.Config.Interface.Addresses.V6),
			},
		},
		Peers: []wiresocks.PeerConfig{{
			PublicKey:    pub,
			PreSharedKey: "0000000000000000000000000000000000000000000000000000000000000000",
			AllowedIPs: []netip.Prefix{
				netip.MustParsePrefix("0.0.0.0/0"),
				netip.MustParsePrefix("::/0"),
			},
			Endpoint: i.Config.Peers[0].Endpoint.Host,
			Reserved: [3]byte{clientID[0], clientID[1], clientID[2]},
		}},
	}
}
