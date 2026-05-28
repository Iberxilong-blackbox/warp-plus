package app

import (
	"io"
	"log/slog"
	"net/netip"
	"os/exec"
	"testing"

	"github.com/bepass-org/warp-plus/egresscheck"
)

func TestRegistrationAllowedRejectsSameActiveIP(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.10")
	pool := testChildPool(t, nil, true)
	pool.activeID = 1
	pool.children[1] = &poolChild{id: 1, state: childActive, ip: ip}

	child := &poolChild{id: 2, state: childWarming, ip: ip}
	accepted, status := pool.registrationAllowedLocked(child)
	if accepted {
		t.Fatal("expected registration to be rejected")
	}
	if status != "rejected_same_ip" {
		t.Fatalf("unexpected status: %s", status)
	}
}

func TestRegistrationAllowedRejectsRecentIPBeforeFirstActive(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.20")
	recent := testRecentIPs(t, ip)
	pool := testChildPool(t, recent, true)

	child := &poolChild{id: 1, state: childWarming, ip: ip}
	accepted, status := pool.registrationAllowedLocked(child)
	if accepted {
		t.Fatal("expected registration to be rejected")
	}
	if status != "rejected_recent_ip" {
		t.Fatalf("unexpected status: %s", status)
	}
}

func TestAcquireReadyDiscardsChildThatFailsAdmission(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.30")
	pool := testChildPool(t, nil, true)
	pool.activeID = 1
	pool.children[1] = &poolChild{id: 1, state: childActive, ip: ip}
	pool.children[2] = &poolChild{
		id:       2,
		cmd:      &exec.Cmd{},
		cancel:   func() {},
		state:    childReady,
		ip:       ip,
		waitDone: closedChannel(),
	}
	pool.readyOrder = []int{2}

	child, _, status := pool.acquireReady()
	if child != nil {
		t.Fatal("expected no acceptable child")
	}
	if status != rotateStatusNoAcceptableEgress {
		t.Fatalf("unexpected status: %s", status)
	}
	if _, ok := pool.children[2]; ok {
		t.Fatal("expected rejected ready child to be removed")
	}
}

func testChildPool(t *testing.T, recent *egresscheck.RecentIPs, requireChange bool) *childPool {
	t.Helper()
	return newChildPool(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WarpOptions{},
		nil,
		recent,
		requireChange,
	)
}

func testRecentIPs(t *testing.T, ips ...netip.Addr) *egresscheck.RecentIPs {
	t.Helper()
	recent, err := egresscheck.LoadRecentIPs("", len(ips))
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		if err := recent.Add(ip); err != nil {
			t.Fatal(err)
		}
	}
	return recent
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
