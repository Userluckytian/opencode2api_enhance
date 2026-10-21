package main

import (
	"testing"
	"time"
)

func TestLoggerWriteAndRead(t *testing.T) {
	l := NewLogger(t.TempDir())
	now := time.Now()
	if err := l.Write(LogEntry{Time: now.Format(time.RFC3339), KeyID: "k1", Model: "glm-4", Total: 100, Status: 200, Ms: 5}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(LogEntry{Time: now.Format(time.RFC3339), KeyID: "k2", Model: "kimi-k2", Total: 50, Status: 200, Ms: 3}); err != nil {
		t.Fatal(err)
	}
	date := now.Format("2006-01-02")
	all, err := l.ReadLogs(date, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	onlyK1, err := l.ReadLogs(date, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyK1) != 1 || onlyK1[0].KeyID != "k1" {
		t.Fatalf("filter failed: %+v", onlyK1)
	}
	none, err := l.ReadLogs("2099-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("missing file should give empty list")
	}
}