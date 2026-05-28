package egresscheck

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestRecentIPsAddContainsAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent_ips.txt")
	recent, err := LoadRecentIPs(path, 2)
	if err != nil {
		t.Fatal(err)
	}

	ip1 := netip.MustParseAddr("203.0.113.1")
	ip2 := netip.MustParseAddr("203.0.113.2")
	ip3 := netip.MustParseAddr("203.0.113.3")

	if err := recent.Add(ip1); err != nil {
		t.Fatal(err)
	}
	if err := recent.Add(ip2); err != nil {
		t.Fatal(err)
	}
	if err := recent.Add(ip3); err != nil {
		t.Fatal(err)
	}

	if recent.Contains(ip1) {
		t.Fatal("oldest IP should be evicted")
	}
	if !recent.Contains(ip2) || !recent.Contains(ip3) {
		t.Fatal("recent IPs should be retained")
	}

	loaded, err := LoadRecentIPs(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contains(ip1) || !loaded.Contains(ip2) || !loaded.Contains(ip3) {
		t.Fatal("persisted recent IPs do not match expected window")
	}
}

func TestLoadRecentIPsRejectsInvalidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent_ips.txt")
	if err := os.WriteFile(path, []byte("not-an-ip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadRecentIPs(path, 10); err == nil {
		t.Fatal("expected invalid recent IP to be rejected")
	}
}
