package egresscheck

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type RecentIPs struct {
	path  string
	limit int

	mu  sync.Mutex
	ips []netip.Addr
}

func LoadRecentIPs(path string, limit int) (*RecentIPs, error) {
	if path == "" || limit <= 0 {
		return &RecentIPs{path: path, limit: limit}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &RecentIPs{path: path, limit: limit}, nil
		}
		return nil, err
	}
	defer file.Close()

	var ips []netip.Addr
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		ip, err := netip.ParseAddr(line)
		if err != nil {
			return nil, fmt.Errorf("invalid recent ip %s:%d: %w", path, lineNo, err)
		}
		ips = append(ips, ip)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(ips) > limit {
		ips = ips[len(ips)-limit:]
	}
	return &RecentIPs{path: path, limit: limit, ips: ips}, nil
}

func (r *RecentIPs) Contains(ip netip.Addr) bool {
	if r == nil || r.limit <= 0 || !ip.IsValid() {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return containsIP(r.ips, ip)
}

func (r *RecentIPs) Add(ip netip.Addr) error {
	if r == nil || r.limit <= 0 || !ip.IsValid() {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if containsIP(r.ips, ip) {
		r.ips = removeIP(r.ips, ip)
	}
	r.ips = append(r.ips, ip)
	if len(r.ips) > r.limit {
		r.ips = r.ips[len(r.ips)-r.limit:]
	}

	if r.path == "" {
		return nil
	}
	return writeRecentIPs(r.path, r.ips)
}

func containsIP(ips []netip.Addr, ip netip.Addr) bool {
	for _, item := range ips {
		if item == ip {
			return true
		}
	}
	return false
}

func removeIP(ips []netip.Addr, ip netip.Addr) []netip.Addr {
	dst := ips[:0]
	for _, item := range ips {
		if item != ip {
			dst = append(dst, item)
		}
	}
	return dst
}

func writeRecentIPs(path string, ips []netip.Addr) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".recent_ips_*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	writer := bufio.NewWriter(tmp)
	for _, ip := range ips {
		if _, err := fmt.Fprintln(writer, ip.String()); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpName, path)
}
