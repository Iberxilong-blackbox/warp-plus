package egresscheck

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type EventLogger struct {
	path string
	mu   sync.Mutex
}

type Event struct {
	Time                    time.Time `json:"time"`
	Event                   string    `json:"event"`
	Mode                    string    `json:"mode,omitempty"`
	ChildID                 *int      `json:"child_id,omitempty"`
	Attempt                 int       `json:"attempt,omitempty"`
	MaxRetry                int       `json:"max_retry,omitempty"`
	IP                      string    `json:"ip,omitempty"`
	ActiveIP                string    `json:"active_ip,omitempty"`
	OldIP                   string    `json:"old_ip,omitempty"`
	NewIP                   string    `json:"new_ip,omitempty"`
	Status                  string    `json:"status,omitempty"`
	Reason                  string    `json:"reason,omitempty"`
	Rule                    string    `json:"rule,omitempty"`
	Score                   *int      `json:"score,omitempty"`
	DurationMS              int64     `json:"duration_ms,omitempty"`
	ReadyCount              int       `json:"ready_count,omitempty"`
	WarmingCount            int       `json:"warming_count,omitempty"`
	RecentIPLimit           int       `json:"recent_ip_limit,omitempty"`
	RequireChange           *bool     `json:"require_change,omitempty"`
	PoolMinReady            int       `json:"pool_min_ready,omitempty"`
	Country                 string    `json:"country,omitempty"`
	ServerEntryIP           string    `json:"server_entry_ip,omitempty"`
	ServerEntryRegion       string    `json:"server_entry_region,omitempty"`
	ServerEntryProviderID   string    `json:"server_entry_provider_id,omitempty"`
	ServerEntryDiagnosticID string    `json:"server_entry_diagnostic_id,omitempty"`
	Protocol                string    `json:"protocol,omitempty"`
}

func NewEventLogger(path string) *EventLogger {
	if path == "" {
		return nil
	}
	return &EventLogger{path: path}
}

func (l *EventLogger) Log(event Event) error {
	if l == nil {
		return nil
	}

	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func StringAddr(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	return ip.String()
}
