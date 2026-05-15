package state

import (
	"sync"

	"github.com/sweeney/statehouse/internal/model"
)

const ActivityLogSize = 50

// activityLog is a fixed-size circular buffer of ActivityRecord entries.
// Oldest entries are evicted when the buffer is full. All methods are
// goroutine-safe.
type activityLog struct {
	mu    sync.Mutex
	buf   [ActivityLogSize]model.ActivityRecord
	head  int // next write slot
	count int // valid entries (0..ActivityLogSize)
}

func newActivityLog() *activityLog { return &activityLog{} }

// Append adds r to the log, evicting the oldest entry when full.
func (l *activityLog) Append(r model.ActivityRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf[l.head] = r
	l.head = (l.head + 1) % ActivityLogSize
	if l.count < ActivityLogSize {
		l.count++
	}
}

// Update finds the most recent entry with the given ID and calls fn on it.
// No-op if no entry with that ID exists.
func (l *activityLog) Update(id string, fn func(*model.ActivityRecord)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := 0; i < l.count; i++ {
		idx := (l.head - 1 - i + ActivityLogSize) % ActivityLogSize
		if l.buf[idx].ID == id {
			fn(&l.buf[idx])
			return
		}
	}
}

// Recent returns up to limit entries, newest first.
func (l *activityLog) Recent(limit int) []model.ActivityRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > l.count {
		limit = l.count
	}
	out := make([]model.ActivityRecord, limit)
	for i := 0; i < limit; i++ {
		idx := (l.head - 1 - i + ActivityLogSize) % ActivityLogSize
		out[i] = l.buf[idx]
	}
	return out
}
