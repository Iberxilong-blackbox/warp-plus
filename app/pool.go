package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/bepass-org/warp-plus/egresscheck"
)

type childState int

const (
	childWarming childState = iota
	childReady
	childActive
)

func (s childState) String() string {
	switch s {
	case childWarming:
		return "warming"
	case childReady:
		return "ready"
	case childActive:
		return "active"
	default:
		return "unknown"
	}
}

type poolChild struct {
	id       int
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	port     netip.AddrPort
	ip       netip.Addr
	state    childState
	token    string
	waitDone chan struct{} // closed when cmd.Wait() returns
	exitErr  error
	exitCode int
}

type childPool struct {
	mu         sync.Mutex
	rotateMu   sync.Mutex
	children   map[int]*poolChild
	nextID     int
	activeID   int   // -1 if none
	readyOrder []int // FIFO order of ready children
	minReady   int

	opts   WarpOptions
	logger *slog.Logger
	relay  *tcpRelay
	recent *egresscheck.RecentIPs
	events *egresscheck.EventLogger

	registryLn   net.Listener
	registrySrv  *http.Server
	registryAddr string // "127.0.0.1:XXXXX"

	requireChange bool

	// channel used to signal the maintenance goroutine that a child registered or died.
	wakeMaintain chan struct{}
}

const minChildStartupTimeout = 120 * time.Second

func newChildPool(l *slog.Logger, opts WarpOptions, relay *tcpRelay, recent *egresscheck.RecentIPs, requireChange bool) *childPool {
	var events *egresscheck.EventLogger
	if opts.EgressCheck != nil {
		events = egresscheck.NewEventLogger(opts.EgressCheck.EventLogPath)
	}
	p := &childPool{
		children:      make(map[int]*poolChild),
		activeID:      -1,
		minReady:      opts.RotateControl.poolMinReady(),
		logger:        l.With("subsystem", "pool"),
		opts:          opts,
		relay:         relay,
		recent:        recent,
		events:        events,
		requireChange: requireChange,
		wakeMaintain:  make(chan struct{}, 8),
	}
	if p.minReady <= 0 {
		p.minReady = 1
	}
	return p
}

// startRegistry begins listening for child registrations on a random loopback port.
func (p *childPool) startRegistry() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("pool: registry listen: %w", err)
	}
	p.registryLn = ln
	p.registryAddr = ln.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/child/register", p.handleRegister)
	p.registrySrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		if err := p.registrySrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			p.logger.Error("child registry server failed", "error", err)
		}
	}()

	p.logger.Info("child registry listening", "addr", p.registryAddr)
	return nil
}

// start initializes the pool: launch the first child, wait for it, and set it active.
func (p *childPool) start(ctx context.Context) error {
	if err := p.startRegistry(); err != nil {
		return err
	}

	maxAttempts := 1
	if p.opts.EgressCheck != nil && p.opts.EgressCheck.MaxRetry > maxAttempts {
		maxAttempts = p.opts.EgressCheck.MaxRetry
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		child, err := p.startChild(ctx)
		if err != nil {
			return fmt.Errorf("pool: start first child: %w", err)
		}

		if err := p.waitFirstChild(ctx, child); err != nil {
			p.logger.Warn("first child failed", "id", child.id, "attempt", attempt, "max_attempts", maxAttempts, "error", err)
			if attempt == maxAttempts {
				return err
			}
			continue
		}

		p.logger.Info("first child active", "id", p.activeID)
		break
	}

	// Start background tasks.
	go p.maintain(ctx)

	return nil
}

func (p *childPool) waitFirstChild(ctx context.Context, child *poolChild) error {
	timer := time.NewTimer(p.childStartupTimeout())
	defer timer.Stop()

	for {
		p.mu.Lock()
		active := p.activeID == child.id
		_, stillTracked := p.children[child.id]
		p.mu.Unlock()

		if active {
			return nil
		}
		if !stillTracked {
			return fmt.Errorf("pool: first child exited before registration")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.waitDone:
			return fmt.Errorf("pool: first child exited before registration")
		case <-p.wakeMaintain:
		case <-timer.C:
			p.mu.Lock()
			p.killChildLocked(child.id)
			p.mu.Unlock()
			return fmt.Errorf("pool: timeout waiting for first child to become ready")
		}
	}
}

// startChild launches one child process and adds it to the warming map.
func (p *childPool) startChild(ctx context.Context) (*poolChild, error) {
	p.mu.Lock()
	id := p.nextID
	p.nextID++
	p.mu.Unlock()

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("pool: generate token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	childCacheDir := filepath.Join(p.opts.CacheDir, fmt.Sprintf("child_%d", id))
	if err := os.MkdirAll(childCacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("pool: create child cache dir: %w", err)
	}

	childCtx, cancel := context.WithCancel(ctx)

	args := p.buildChildArgs(id, token, childCacheDir)
	cmd := exec.CommandContext(childCtx, os.Args[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("pool: start child process: %w", err)
	}

	child := &poolChild{
		id:       id,
		cmd:      cmd,
		cancel:   cancel,
		state:    childWarming,
		token:    token,
		waitDone: make(chan struct{}),
		exitCode: -1,
	}

	p.mu.Lock()
	p.children[id] = child
	p.mu.Unlock()

	p.logger.Info("started child process", "id", id, "pid", cmd.Process.Pid)
	p.logEvent(egresscheck.Event{
		Event:        "child_started",
		Mode:         "pool",
		ChildID:      intPtr(id),
		Country:      p.psiphonCountry(),
		PoolMinReady: p.minReady,
	})

	go p.waitChild(child)
	go p.watchWarmingTimeout(child)

	return child, nil
}

func (p *childPool) waitChild(child *poolChild) {
	err := child.cmd.Wait()
	exitCode := -1
	if child.cmd.ProcessState != nil {
		exitCode = child.cmd.ProcessState.ExitCode()
	}

	p.mu.Lock()
	if tracked := p.children[child.id]; tracked == child {
		child.exitErr = err
		child.exitCode = exitCode
	}
	p.mu.Unlock()

	close(child.waitDone)
	p.handleChildExit(child.id, err, exitCode)
}

func (p *childPool) watchWarmingTimeout(child *poolChild) {
	timeout := p.childStartupTimeout()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-child.waitDone:
		return
	case <-timer.C:
		p.mu.Lock()
		current := p.children[child.id]
		if current == child && child.state == childWarming {
			p.logger.Warn("warming child timed out", "id", child.id, "timeout", timeout)
			p.killChildLocked(child.id)
		}
		p.mu.Unlock()
	}
}

func (p *childPool) childStartupTimeout() time.Duration {
	maxRetry := 1
	if p.opts.EgressCheck != nil && p.opts.EgressCheck.MaxRetry > maxRetry {
		maxRetry = p.opts.EgressCheck.MaxRetry
	}

	// Each Psiphon attempt can run for roughly 75s, plus WARP setup and egress
	// checking. Keep the floor for the common single-attempt path.
	timeout := time.Duration(maxRetry)*90*time.Second + 30*time.Second
	if timeout < minChildStartupTimeout {
		return minChildStartupTimeout
	}
	return timeout
}

// buildChildArgs constructs the CLI arguments for a child process.
// It copies all relevant flags from the parent's configuration.
func (p *childPool) buildChildArgs(id int, token, cacheDir string) []string {
	o := p.opts
	args := []string{
		"--child",
		fmt.Sprintf("--child-id=%d", id),
		fmt.Sprintf("--child-token=%s", token),
		fmt.Sprintf("--parent-addr=%s", p.registryAddr),
		fmt.Sprintf("--cache-dir=%s", cacheDir),
		fmt.Sprintf("--bind=%s", "127.0.0.1:0"),
	}

	// WARP flags
	if o.Endpoint != "" {
		args = append(args, fmt.Sprintf("--endpoint=%s", o.Endpoint))
	}
	if o.License != "" {
		args = append(args, fmt.Sprintf("--key=%s", o.License))
	}
	args = append(args, fmt.Sprintf("--dns=%s", o.DnsAddr))
	if o.Reserved != "" {
		args = append(args, fmt.Sprintf("--reserved=%s", o.Reserved))
	}
	if o.TestURL != "" {
		args = append(args, fmt.Sprintf("--test-url=%s", o.TestURL))
	}

	// Psiphon flags
	if o.Psiphon != nil {
		args = append(args, "--cfon")
		args = append(args, fmt.Sprintf("--country=%s", o.Psiphon.Country))
	}

	// Scan flags (re-scan in each child for endpoint diversity)
	if o.Scan != nil {
		args = append(args, "--scan")
		if o.Scan.V4 && !o.Scan.V6 {
			args = append(args, "-4")
		}
		if o.Scan.V6 && !o.Scan.V4 {
			args = append(args, "-6")
		}
		args = append(args, fmt.Sprintf("--rtt=%s", o.Scan.MaxRTT))
	}

	// Egress check flags
	if o.EgressCheck != nil {
		args = append(args, "--egress-check")
		args = append(args, fmt.Sprintf("--egress-ip-url=%s", o.EgressCheck.IPURL))
		if o.EgressCheck.Blacklist != nil {
			// Pass the raw blacklist file path — child loads it independently.
			// Use the original path from RotateControl if available.
			args = append(args, fmt.Sprintf("--egress-blacklist=%s", p.opts.blacklistPath()))
		}
		if o.EgressCheck.ScoreAPI != "" {
			args = append(args, fmt.Sprintf("--egress-score-api=%s", o.EgressCheck.ScoreAPI))
			args = append(args, fmt.Sprintf("--egress-score-max=%d", o.EgressCheck.ScoreMax))
		}
		args = append(args, fmt.Sprintf("--egress-max-retry=%d", o.EgressCheck.MaxRetry))
		if o.EgressCheck.EventLogPath != "" {
			args = append(args, fmt.Sprintf("--egress-event-log=%s", o.EgressCheck.EventLogPath))
		}
		// Child doesn't need periodic check — set to 0.
		args = append(args, "--egress-check-interval=0")
	}

	return args
}

// handleRegister processes a POST /child/register from a child process.
func (p *childPool) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlStatus(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}

	var reg childRegistration
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		writeControlStatus(w, http.StatusBadRequest, "bad_request")
		return
	}

	p.mu.Lock()
	child, ok := p.children[reg.ChildID]
	if !ok {
		p.mu.Unlock()
		p.logger.Warn("registration from unknown child", "id", reg.ChildID)
		writeControlStatus(w, http.StatusForbidden, "unknown_child")
		return
	}
	select {
	case <-child.waitDone:
		p.mu.Unlock()
		p.logger.Warn("registration from exited child", "id", reg.ChildID)
		writeControlStatus(w, http.StatusForbidden, "exited_child")
		return
	default:
	}
	if child.state != childWarming {
		p.mu.Unlock()
		p.logger.Warn("registration from non-warming child", "id", reg.ChildID, "state", child.state.String())
		writeControlStatus(w, http.StatusForbidden, "invalid_state")
		return
	}

	if child.token != reg.Token {
		p.mu.Unlock()
		p.logger.Warn("registration with bad token", "id", reg.ChildID)
		writeControlStatus(w, http.StatusUnauthorized, "bad_token")
		return
	}

	port, err := netip.ParseAddrPort(fmt.Sprintf("127.0.0.1:%d", reg.Port))
	if err != nil {
		p.mu.Unlock()
		writeControlStatus(w, http.StatusBadRequest, "bad_port")
		return
	}

	ip, err := netip.ParseAddr(reg.IP)
	if err != nil {
		p.mu.Unlock()
		writeControlStatus(w, http.StatusBadRequest, "bad_ip")
		return
	}

	child.port = port
	child.ip = ip
	activeIP := netip.Addr{}
	if p.activeID >= 0 && p.children[p.activeID] != nil {
		activeIP = p.children[p.activeID].ip
	}

	if accepted, status := p.registrationAllowedLocked(child); !accepted {
		p.logger.Info("child registration rejected", "id", child.id, "ip", ip, "reason", status)
		p.logEvent(egresscheck.Event{
			Event:         "child_registration_rejected",
			Mode:          "pool",
			ChildID:       intPtr(child.id),
			IP:            ip.String(),
			ActiveIP:      egresscheck.StringAddr(activeIP),
			Status:        "rejected",
			Reason:        status,
			RequireChange: boolPtr(p.requireChange),
			RecentIPLimit: p.opts.RotateControl.recentIPLimit(),
			Country:       p.psiphonCountry(),
		})
		p.killChildLocked(child.id)
		p.mu.Unlock()
		writeControlStatus(w, http.StatusConflict, status)
		select {
		case p.wakeMaintain <- struct{}{}:
		default:
		}
		return
	}

	if p.activeID < 0 {
		// No active child yet — make this one active.
		child.state = childActive
		p.activeID = child.id
		p.mu.Unlock()

		p.logger.Info("child activated (first)", "id", child.id, "port", port.Port(), "ip", ip)
		p.logEvent(egresscheck.Event{
			Event:         "child_activated_first",
			Mode:          "pool",
			ChildID:       intPtr(child.id),
			IP:            ip.String(),
			NewIP:         ip.String(),
			Status:        "ok",
			RequireChange: boolPtr(p.requireChange),
			RecentIPLimit: p.opts.RotateControl.recentIPLimit(),
			Country:       p.psiphonCountry(),
		})
		p.relay.setUpstream(port)
		writeControlStatus(w, http.StatusOK, "ok")
		select {
		case p.wakeMaintain <- struct{}{}:
		default:
		}
		return
	}

	// Already have an active child — add to ready pool.
	child.state = childReady
	p.readyOrder = append(p.readyOrder, child.id)
	p.mu.Unlock()

	p.logger.Info("child ready", "id", child.id, "port", port.Port(), "ip", ip)
	p.logEvent(egresscheck.Event{
		Event:         "child_ready",
		Mode:          "pool",
		ChildID:       intPtr(child.id),
		IP:            ip.String(),
		ActiveIP:      egresscheck.StringAddr(activeIP),
		Status:        "ok",
		RequireChange: boolPtr(p.requireChange),
		RecentIPLimit: p.opts.RotateControl.recentIPLimit(),
		Country:       p.psiphonCountry(),
	})
	writeControlStatus(w, http.StatusOK, "ok")
	select {
	case p.wakeMaintain <- struct{}{}:
	default:
	}
}

// registrationAllowedLocked applies pool admission rules to a child that has
// reported its real egress IP. Must hold p.mu.
func (p *childPool) registrationAllowedLocked(child *poolChild) (bool, string) {
	if child == nil {
		return false, "invalid_child"
	}
	hasActive := p.activeID >= 0
	if hasActive && p.requireChange && child.ip.IsValid() {
		active := p.children[p.activeID]
		if active != nil && active.ip.IsValid() && child.ip == active.ip {
			return false, "rejected_same_ip"
		}
	}
	if hasActive && p.recent != nil && p.recent.Contains(child.ip) {
		return false, "rejected_recent_ip"
	}
	return true, "ok"
}

// acquireReady switches the relay to the next ready child that satisfies IP constraints.
// Called from rotate(). Returns nil if no acceptable child is available.
func (p *childPool) acquireReady() (*poolChild, netip.Addr, rotateStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.activeID < 0 {
		return nil, netip.Addr{}, rotateStatusRefreshFailed
	}

	active := p.children[p.activeID]
	if active == nil {
		return nil, netip.Addr{}, rotateStatusRefreshFailed
	}
	oldID := p.activeID
	oldIP := active.ip

	for len(p.readyOrder) > 0 {
		id := p.readyOrder[0]
		p.readyOrder = p.readyOrder[1:]

		child := p.children[id]
		if child == nil || child.state != childReady {
			continue
		}

		if accepted, status := p.registrationAllowedLocked(child); !accepted {
			p.logger.Info("ready child no longer acceptable, discarding", "id", id, "ip", child.ip, "reason", status)
			p.logEvent(egresscheck.Event{
				Event:         "ready_child_rejected",
				Mode:          "pool",
				ChildID:       intPtr(id),
				IP:            egresscheck.StringAddr(child.ip),
				ActiveIP:      egresscheck.StringAddr(oldIP),
				Status:        "rejected",
				Reason:        status,
				RequireChange: boolPtr(p.requireChange),
				RecentIPLimit: p.opts.RotateControl.recentIPLimit(),
				Country:       p.psiphonCountry(),
			})
			p.killChildLocked(id)
			continue
		}

		// Accept this child.
		p.relay.setUpstream(child.port)
		child.state = childActive
		p.activeID = id

		p.logger.Info("switched active child", "old_id", oldID, "new_id", id, "old_ip", oldIP, "new_ip", child.ip)
		p.logEvent(egresscheck.Event{
			Event:         "active_switched",
			Mode:          "pool",
			ChildID:       intPtr(id),
			OldIP:         egresscheck.StringAddr(oldIP),
			NewIP:         egresscheck.StringAddr(child.ip),
			Status:        "ok",
			RequireChange: boolPtr(p.requireChange),
			RecentIPLimit: p.opts.RotateControl.recentIPLimit(),
			Country:       p.psiphonCountry(),
		})
		p.killChildLocked(oldID)
		select {
		case p.wakeMaintain <- struct{}{}:
		default:
		}
		return child, oldIP, rotateStatusOK
	}

	return nil, netip.Addr{}, rotateStatusNoAcceptableEgress
}

func (p *childPool) currentIP() netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentIPLocked()
}

func (p *childPool) currentIPLocked() netip.Addr {
	if p.activeID < 0 {
		return netip.Addr{}
	}
	active := p.children[p.activeID]
	if active == nil {
		return netip.Addr{}
	}
	return active.ip
}

func (p *childPool) warmingCountLocked() int {
	count := 0
	for _, ch := range p.children {
		if ch.state == childWarming {
			count++
		}
	}
	return count
}

func (p *childPool) logEvent(event egresscheck.Event) {
	if err := p.events.Log(event); err != nil {
		p.logger.Warn("unable to write egress event log", "error", err)
	}
}

func (p *childPool) psiphonCountry() string {
	if p.opts.Psiphon == nil {
		return ""
	}
	return p.opts.Psiphon.Country
}

func intPtr(v int) *int {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

// killChildLocked kills a child and removes it from tracking. Must hold p.mu.
func (p *childPool) killChildLocked(id int) {
	child := p.children[id]
	if child == nil {
		return
	}

	p.logger.Info("killing child", "id", id, "state", child.state.String())
	child.cancel()

	// Wait for process to exit in background.
	go func(ch *poolChild) {
		select {
		case <-ch.waitDone:
		case <-time.After(10 * time.Second):
			// Force kill if still running.
			if ch.cmd.Process != nil {
				ch.cmd.Process.Kill()
			}
		}
	}(child)

	delete(p.children, id)
	p.removeReadyLocked(id)
	if p.activeID == id {
		p.activeID = -1
	}
}

func (p *childPool) removeReadyLocked(id int) {
	for i := 0; i < len(p.readyOrder); {
		if p.readyOrder[i] == id {
			p.readyOrder = append(p.readyOrder[:i], p.readyOrder[i+1:]...)
			continue
		}
		i++
	}
}

// maintain is the background pool maintenance loop.
func (p *childPool) maintain(ctx context.Context) {
	p.logger.Info("pool maintenance started", "min_ready", p.minReady)

	// Initial: after first child is active, start a second to have a ready spare.
	p.startChildIfNeeded()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wakeMaintain:
			p.startChildIfNeeded()
		case <-time.After(5 * time.Second):
			p.startChildIfNeeded()
		}
	}
}

func (p *childPool) startChildIfNeeded() {
	p.mu.Lock()
	readyCount := len(p.readyOrder)
	warmingCount := 0
	for _, ch := range p.children {
		if ch.state == childWarming {
			warmingCount++
		}
	}
	p.mu.Unlock()

	need := p.minReady - readyCount - warmingCount
	for i := 0; i < need; i++ {
		if _, err := p.startChild(context.Background()); err != nil {
			p.logger.Warn("failed to start child for pool", "error", err)
			break
		}
	}
}

func (p *childPool) handleChildExit(id int, err error, exitCode int) {
	p.mu.Lock()
	child := p.children[id]
	if child == nil {
		p.mu.Unlock()
		return
	}

	state := child.state
	p.logger.Warn("child process exited", "id", id, "state", state.String(), "exit_code", exitCode, "error", err)

	delete(p.children, id)
	p.removeReadyLocked(id)

	if state == childActive {
		p.activeID = -1
		for len(p.readyOrder) > 0 {
			readyID := p.readyOrder[0]
			p.readyOrder = p.readyOrder[1:]
			ready := p.children[readyID]
			if ready == nil || ready.state != childReady {
				continue
			}
			ready.state = childActive
			p.activeID = readyID
			p.relay.setUpstream(ready.port)
			p.logger.Warn("promoted ready child after active died", "id", readyID)
			break
		}
		if p.activeID < 0 {
			p.logger.Error("active child died and no ready child is available")
		}
	}
	p.mu.Unlock()

	select {
	case p.wakeMaintain <- struct{}{}:
	default:
	}
}

// shutdown kills all children and closes the registry server.
func (p *childPool) shutdown() {
	p.logger.Info("shutting down pool")

	if p.registrySrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.registrySrv.Shutdown(shutdownCtx)
	}

	p.mu.Lock()
	children := make([]*poolChild, 0, len(p.children))
	for id, child := range p.children {
		p.logger.Info("killing child during shutdown", "id", id)
		children = append(children, child)
	}
	p.children = nil
	p.readyOrder = nil
	p.activeID = -1
	p.mu.Unlock()

	for _, child := range children {
		child.cancel()
		if child.cmd.Process != nil {
			// Give it a moment to exit gracefully.
			select {
			case <-child.waitDone:
			case <-time.After(5 * time.Second):
				child.cmd.Process.Kill()
				<-child.waitDone
			}
		}
	}
}

func (o *RotateControlOptions) poolMinReady() int {
	if o == nil {
		return 1
	}
	return o.PoolMinReady
}
