package egresscheck

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEventLoggerAppendsJSONLinesWithTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	logger := NewEventLogger(path)

	if err := logger.Log(Event{Event: "first", Status: "ok"}); err != nil {
		t.Fatalf("log first event: %v", err)
	}
	if err := logger.Log(Event{Event: "second", Status: "rejected", Reason: "same_ip"}); err != nil {
		t.Fatalf("log second event: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var events []Event
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan event log: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Event != "first" || events[1].Event != "second" {
		t.Fatalf("events were not appended in order: %#v", events)
	}
	if events[0].Time.IsZero() || events[1].Time.IsZero() {
		t.Fatalf("expected timestamps on all events: %#v", events)
	}
}
