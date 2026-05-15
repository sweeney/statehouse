package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

var t0 = time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)

func makeSignal(id, source, typ string, ttl time.Duration) model.ActivitySignal {
	s := model.ActivitySignal{
		ID:         id,
		Source:     source,
		Type:       typ,
		Confidence: 0.9,
		Since:      t0,
	}
	if ttl > 0 {
		s.ExpiresAt = t0.Add(ttl)
	}
	return s
}

func TestSignalStore_ActiveReturnsLiveSignals(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("s1", "intercom", "call_active", 0))
	ss.Upsert(makeSignal("s2", "pir", "motion", 5*time.Minute))

	got := ss.Active(t0.Add(1 * time.Minute))
	if len(got) != 2 {
		t.Fatalf("expected 2 active signals, got %d", len(got))
	}
}

func TestSignalStore_TTLExpiry(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("s1", "pir", "motion", 5*time.Minute))

	// Before expiry.
	if got := ss.Active(t0.Add(4 * time.Minute)); len(got) != 1 {
		t.Fatalf("expected signal before expiry, got %d", len(got))
	}
	// After expiry.
	if got := ss.Active(t0.Add(6 * time.Minute)); len(got) != 0 {
		t.Fatalf("expected no signals after expiry, got %d", len(got))
	}
}

func TestSignalStore_ZeroExpiryNeverExpires(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("s1", "intercom", "call_active", 0)) // no TTL

	far := t0.Add(365 * 24 * time.Hour)
	if got := ss.Active(far); len(got) != 1 {
		t.Fatalf("expected signal with no expiry to survive indefinitely, got %d", len(got))
	}
}

func TestSignalStore_Clear(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("s1", "intercom", "call_active", 0))
	ss.Upsert(makeSignal("s2", "intercom", "call_active", 0))

	ss.ClearAt("s1", t0)

	got := ss.Active(t0)
	if len(got) != 1 || got[0].ID != "s2" {
		t.Fatalf("expected only s2 after clearing s1, got %v", got)
	}
}

func TestSignalStore_ClearNoopOnMissing(t *testing.T) {
	ss := newSignalStore()
	ss.ClearAt("nonexistent", t0) // must not panic
}

func TestSignalStore_Prune(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("s1", "pir", "motion", 2*time.Minute))
	ss.Upsert(makeSignal("s2", "pir", "motion", 10*time.Minute))

	ss.Prune(t0.Add(5 * time.Minute)) // s1 is expired, s2 is not

	got := ss.Active(t0.Add(5 * time.Minute))
	if len(got) != 1 || got[0].ID != "s2" {
		t.Fatalf("expected only s2 after prune, got %v", got)
	}
}

func TestSignalStore_Upsert(t *testing.T) {
	ss := newSignalStore()
	s := makeSignal("s1", "intercom", "call_ringing", 5*time.Minute)
	ss.Upsert(s)

	// Upgrade to call_active with no TTL.
	s.Type = "call_active"
	s.ExpiresAt = time.Time{}
	ss.Upsert(s)

	got := ss.Active(t0.Add(10 * time.Minute)) // beyond original TTL
	if len(got) != 1 || got[0].Type != "call_active" {
		t.Fatalf("expected upserted signal with call_active and no expiry, got %v", got)
	}
}

func TestSignalStore_LastAtAdvancesOnUpsert(t *testing.T) {
	ss := newSignalStore()
	if !ss.LastAt().IsZero() {
		t.Fatal("expected zero LastAt before any signal")
	}
	s := makeSignal("s1", "intercom", "call_ringing", 0)
	s.Since = t0.Add(5 * time.Minute)
	ss.Upsert(s)
	if !ss.LastAt().Equal(s.Since) {
		t.Errorf("expected LastAt=%v, got %v", s.Since, ss.LastAt())
	}
}

func TestSignalStore_LastAtAdvancesOnClear(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("s1", "intercom", "call_active", 0))
	hangupAt := t0.Add(10 * time.Minute)
	ss.ClearAt("s1", hangupAt)
	if !ss.LastAt().Equal(hangupAt) {
		t.Errorf("expected LastAt=%v after clear, got %v", hangupAt, ss.LastAt())
	}
}

func TestSignalStore_LastAtAdvancesOnPrune(t *testing.T) {
	ss := newSignalStore()
	s := makeSignal("s1", "pir", "motion", 5*time.Minute)
	ss.Upsert(s)
	pruneAt := t0.Add(6 * time.Minute)
	ss.Prune(pruneAt)
	if !ss.LastAt().Equal(s.ExpiresAt) {
		t.Errorf("expected LastAt=%v from ExpiresAt after prune, got %v", s.ExpiresAt, ss.LastAt())
	}
}

func TestSignalStore_LastAtIsHighWaterMark(t *testing.T) {
	ss := newSignalStore()
	// Clear at t+10, then upsert signal with Since=t+3 — lastAt stays at t+10.
	ss.ClearAt("ghost", t0.Add(10*time.Minute))
	s := makeSignal("s1", "pir", "motion", 0)
	s.Since = t0.Add(3 * time.Minute)
	ss.Upsert(s)
	if !ss.LastAt().Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("expected LastAt to stay at high-water mark, got %v", ss.LastAt())
	}
}

func TestSignalStore_ConcurrentOverlappingCalls(t *testing.T) {
	ss := newSignalStore()
	ss.Upsert(makeSignal("call-1", "intercom", "call_active", time.Hour))
	ss.Upsert(makeSignal("call-2", "intercom", "call_active", time.Hour))

	// Only first call ends.
	ss.ClearAt("call-1", t0)

	got := ss.Active(t0)
	if len(got) != 1 || got[0].ID != "call-2" {
		t.Fatalf("expected call-2 still active after call-1 cleared, got %v", got)
	}
}
