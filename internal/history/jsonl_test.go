package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type payload struct {
	TS   time.Time `json:"ts"`
	Name string    `json:"name"`
}

func TestLog_AppendAndRecent(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(filepath.Join(dir, "events.jsonl"), 24, 1, 16)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()
	for i := 0; i < 5; i++ {
		if err := l.Append("test", payload{TS: time.Now().UTC(), Name: "p"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got := l.Recent(10)
	if len(got) != 5 {
		t.Fatalf("expected 5 recent entries, got %d", len(got))
	}
	// newest-first
	if !got[0].Timestamp.After(got[len(got)-1].Timestamp) && !got[0].Timestamp.Equal(got[len(got)-1].Timestamp) {
		t.Fatalf("expected newest-first ordering")
	}
}

func TestLog_RetentionDropsOldEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// 1 hour retention; we'll write entries 2 hours in the past so
	// they're past the cutoff.
	l, err := Open(path, 1, 1, 4)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 4; i++ {
		_ = l.Append("test", payload{TS: time.Now().Add(-2 * time.Hour).UTC(), Name: "old"})
	}
	if err := l.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	l.Close()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(contents) != 0 {
		t.Fatalf("expected empty file after retention rotate, got %d bytes", len(contents))
	}
}

func TestLog_NoPathStillBuffersInMemory(t *testing.T) {
	l, err := Open("", 1, 1, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = l.Append("test", payload{TS: time.Now().UTC(), Name: "p"})
	got := l.Recent(10)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry in memory ring, got %d", len(got))
	}
}
