package history

import (
	"bufio"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is the wire format of one line in the JSONL recent log.
type Entry struct {
	Timestamp time.Time       `json:"ts"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

// Log is an append-only bounded JSONL event log. It keeps a sliding
// in-memory window for cheap HTTP queries and also persists each
// entry to disk so the engine has a warm-restart trail.
type Log struct {
	mu        sync.Mutex
	path      string
	retention time.Duration
	maxBytes  int64

	bytesWritten int64
	memory       *list.List // most-recent-last
	memoryLimit  int

	file *os.File
}

// Open creates or opens the JSONL log. Path may be empty, in which
// case only the in-memory ring buffer is used.
func Open(path string, retentionHours, maxSizeMB, memoryLimit int) (*Log, error) {
	l := &Log{
		path:        path,
		retention:   time.Duration(retentionHours) * time.Hour,
		maxBytes:    int64(maxSizeMB) * 1024 * 1024,
		memory:      list.New(),
		memoryLimit: memoryLimit,
	}
	if l.memoryLimit <= 0 {
		l.memoryLimit = 2048
	}
	if path == "" {
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	if stat, err := f.Stat(); err == nil {
		l.bytesWritten = stat.Size()
	}
	l.file = f
	if err := l.warmFromDisk(); err != nil {
		return nil, fmt.Errorf("warm memory: %w", err)
	}
	return l, nil
}

// Append serialises payload as JSON and writes it to the log.
func (l *Log) Append(kind string, payload any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	entry := Entry{Timestamp: time.Now().UTC(), Kind: kind, Payload: raw}
	if t, ok := timestampOf(payload); ok && !t.IsZero() {
		entry.Timestamp = t.UTC()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := l.writeLine(line); err != nil {
		return err
	}
	l.memory.PushBack(entry)
	if l.memory.Len() > l.memoryLimit {
		l.memory.Remove(l.memory.Front())
	}
	return nil
}

// Recent returns up to limit most-recent entries newest-first.
func (l *Log) Recent(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > l.memory.Len() {
		limit = l.memory.Len()
	}
	out := make([]Entry, 0, limit)
	for e := l.memory.Back(); e != nil && len(out) < limit; e = e.Prev() {
		out = append(out, e.Value.(Entry))
	}
	return out
}

// Stats returns the number of events held in the in-memory window and
// the total bytes written to the on-disk log file (0 when path-less).
func (l *Log) Stats() (events int, bytesWritten int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memory.Len(), l.bytesWritten
}

// Close releases the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Rotate is exposed for tests so they can force the retention sweep.
func (l *Log) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotateLocked(true)
}

func (l *Log) writeLine(line []byte) error {
	if l.file == nil {
		return nil
	}
	if _, err := l.file.Write(line); err != nil {
		return err
	}
	if _, err := l.file.Write([]byte("\n")); err != nil {
		return err
	}
	l.bytesWritten += int64(len(line)) + 1
	if l.maxBytes > 0 && l.bytesWritten > l.maxBytes {
		return l.rotateLocked(false)
	}
	if l.retention > 0 && l.bytesWritten > l.maxBytes/4 {
		// Light retention check when growing.
		return l.rotateLocked(false)
	}
	return nil
}

// rotateLocked rewrites the log keeping only entries within the
// retention window and below the max byte cap. `force` causes
// rotation even if the file is small enough.
func (l *Log) rotateLocked(force bool) error {
	if l.file == nil {
		return nil
	}
	if !force && l.maxBytes > 0 && l.bytesWritten <= l.maxBytes {
		return nil
	}
	if err := l.file.Sync(); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(l.file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	cutoff := time.Now().UTC().Add(-l.retention)
	keep := make([][]byte, 0, 1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if l.retention > 0 && e.Timestamp.Before(cutoff) {
			continue
		}
		dup := make([]byte, len(line))
		copy(dup, line)
		keep = append(keep, dup)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	// If still over max, drop oldest until under.
	if l.maxBytes > 0 {
		var total int64
		for _, k := range keep {
			total += int64(len(k)) + 1
		}
		for len(keep) > 0 && total > l.maxBytes {
			total -= int64(len(keep[0])) + 1
			keep = keep[1:]
		}
	}
	tmpPath := l.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	var written int64
	for _, k := range keep {
		if _, err := w.Write(k); err != nil {
			tmp.Close()
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			tmp.Close()
			return err
		}
		written += int64(len(k)) + 1
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, l.path); err != nil {
		return err
	}
	if err := l.file.Close(); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.file = f
	l.bytesWritten = written
	return nil
}

func (l *Log) warmFromDisk() error {
	if l.file == nil {
		return nil
	}
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(l.file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		l.memory.PushBack(e)
		if l.memory.Len() > l.memoryLimit {
			l.memory.Remove(l.memory.Front())
		}
	}
	// Restore file position to end for appends.
	if _, err := l.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return nil
}

// timestampOf optionally extracts a typed timestamp from common
// payload shapes so the JSONL line records when the underlying event
// happened, not when it was logged.
func timestampOf(payload any) (time.Time, bool) {
	type hasTS interface {
		GetTimestamp() time.Time
	}
	if v, ok := payload.(hasTS); ok {
		return v.GetTimestamp(), true
	}
	// Reflection-light: try common fields via marshalling.
	type generic struct {
		TS time.Time `json:"ts"`
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return time.Time{}, false
	}
	var g generic
	if err := json.Unmarshal(raw, &g); err == nil && !g.TS.IsZero() {
		return g.TS, true
	}
	return time.Time{}, false
}
