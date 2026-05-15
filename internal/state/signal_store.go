package state

import (
	"sync"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

// SignalStore holds active ActivitySignals. It is goroutine-safe.
// Signals live until ClearAt is called or their ExpiresAt passes.
//
// lastAt is a high-water mark: the most recent time any signal was
// known to be active (from Upsert) or explicitly ended (from ClearAt
// or Prune). It never resets, so DeriveHouseState can apply the
// QuietAfter / EmptyAfter windows to signal-sourced activity in the
// same way it does for devices.
type SignalStore struct {
	mu      sync.Mutex
	signals map[string]model.ActivitySignal // keyed by signal ID
	lastAt  time.Time
}

func newSignalStore() *SignalStore {
	return &SignalStore{signals: make(map[string]model.ActivitySignal)}
}

// Upsert inserts or replaces the signal with s.ID, advancing lastAt
// from the signal's Since time.
func (ss *SignalStore) Upsert(s model.ActivitySignal) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.signals[s.ID] = s
	if s.Since.After(ss.lastAt) {
		ss.lastAt = s.Since
	}
}

// ClearAt removes the signal with the given ID and advances lastAt
// from ts (the event time, e.g. the hangup timestamp). No-op on ID if
// not present, but lastAt is still advanced.
func (ss *SignalStore) ClearAt(id string, ts time.Time) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.signals, id)
	if ts.After(ss.lastAt) {
		ss.lastAt = ts
	}
}

// Prune removes all signals whose ExpiresAt is before now, advancing
// lastAt from each expired signal's ExpiresAt so the QuietAfter window
// starts from when the safety TTL fired rather than from zero.
func (ss *SignalStore) Prune(now time.Time) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for id, s := range ss.signals {
		if s.IsExpired(now) {
			delete(ss.signals, id)
			if s.ExpiresAt.After(ss.lastAt) {
				ss.lastAt = s.ExpiresAt
			}
		}
	}
}

// Active returns all non-expired signals as of now.
func (ss *SignalStore) Active(now time.Time) []model.ActivitySignal {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	out := make([]model.ActivitySignal, 0, len(ss.signals))
	for _, s := range ss.signals {
		if !s.IsExpired(now) {
			out = append(out, s)
		}
	}
	return out
}

// LastAt returns the high-water mark of signal activity.
func (ss *SignalStore) LastAt() time.Time {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.lastAt
}
