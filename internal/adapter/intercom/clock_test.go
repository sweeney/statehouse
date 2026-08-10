package intercom

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// These tests pin the adapter to its injected clock.
//
// The adapter derives two times from "now": the sanitisation window that
// clamps publisher-supplied timestamps (timeutil.Sanitise), and the safety
// TTL stamped on call signals (ExpiresAt). If either reads the wall clock
// instead of the injected clock, every signal the adapter emits lands in a
// different time base from the engine that consumes it, and the house-state
// windows in state.DeriveHouseState compute against a mixed epoch.
//
// replayEpoch is deliberately far in the past. Sanitise rejects anything
// more than 30 days behind "now", so a wall-clock read collapses these
// payload timestamps to the real current time and the assertions below
// fail — at any date the suite is ever run. Anchoring the assertions to a
// fixed epoch rather than to time.Now() is what keeps these tests from
// becoming the very kind of time bomb they exist to catch.
var replayEpoch = time.Date(2020, 3, 1, 9, 0, 0, 0, time.UTC)

func ringingPayload(callID string, ts time.Time) string {
	return `{"event":"ringing","call_id":"` + callID + `","from":{"extension":"11","name":"Office"},` +
		`"to":{"extension":"12","name":"Games Room"},"timestamp":"` + ts.Format(time.RFC3339) + `"}`
}

func hungupPayload(callID string, ts time.Time) string {
	return `{"event":"hungup","call_id":"` + callID + `","from":{"extension":"11","name":"Office"},` +
		`"to":{"extension":"12","name":"Games Room"},"timestamp":"` + ts.Format(time.RFC3339) + `","cause":"cancelled"}`
}

// TestClock_SignalTimesComeFromInjectedClock is the core of the failure
// mode: both times on an emitted signal must derive from the injected
// clock, never from the wall clock.
func TestClock_SignalTimesComeFromInjectedClock(t *testing.T) {
	eng := newFakeEngineAt(replayEpoch)
	a := New(eng, "asterisk", nil)

	ringAt := replayEpoch.Add(90 * time.Second)
	eng.clock.Set(ringAt)
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(ringingPayload("call-1", ringAt)), false)

	if len(eng.ingested) != 1 {
		t.Fatalf("expected 1 IngestSignal call, got %d", len(eng.ingested))
	}
	s := eng.ingested[0]

	// Since must be the payload timestamp, passed through sanitisation
	// unchanged because it agrees with the injected clock.
	if !s.Since.Equal(ringAt) {
		t.Errorf("signal Since = %s, want payload timestamp %s (a wall-clock read would put this near %s)",
			s.Since.UTC(), ringAt, time.Now().UTC())
	}
	// ExpiresAt is the safety TTL and must be anchored to the injected
	// clock so the engine can ever observe it expiring.
	if want := ringAt.Add(CallTTL); !s.ExpiresAt.Equal(want) {
		t.Errorf("signal ExpiresAt = %s, want %s", s.ExpiresAt.UTC(), want)
	}
}

// TestClock_MissingTimestampFallsBackToInjectedClock covers the fallback
// path: an absent or unparseable payload timestamp must resolve to the
// injected clock's now, not the wall clock.
func TestClock_MissingTimestampFallsBackToInjectedClock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"absent", `{"event":"ringing","call_id":"call-1"}`},
		{"empty", `{"event":"ringing","call_id":"call-1","timestamp":""}`},
		{"unparseable", `{"event":"ringing","call_id":"call-1","timestamp":"not-a-timestamp"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := newFakeEngineAt(replayEpoch)
			a := New(eng, "asterisk", nil)

			a.HandleMessage("asterisk/call/call-1/ringing", []byte(tc.payload), false)

			if len(eng.ingested) != 1 {
				t.Fatalf("expected 1 IngestSignal call, got %d", len(eng.ingested))
			}
			if s := eng.ingested[0]; !s.Since.Equal(replayEpoch) {
				t.Errorf("signal Since = %s, want injected clock now %s", s.Since.UTC(), replayEpoch)
			}
		})
	}
}

// TestClock_SanitisationIsRelativeToInjectedClock guards the security
// property added in #55: publisher-supplied timestamps are still clamped.
// The window must be measured against the injected clock, so a poisoned
// timestamp is rejected on its distance from the engine's now — not from
// whatever the wall clock happens to read.
func TestClock_SanitisationIsRelativeToInjectedClock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		payloadAt time.Time
		clamped   bool
	}{
		{"far future is clamped", replayEpoch.Add(48 * time.Hour), true},
		{"far past is clamped", replayEpoch.Add(-60 * 24 * time.Hour), true},
		{"just inside the future window is kept", replayEpoch.Add(12 * time.Hour), false},
		{"just inside the past window is kept", replayEpoch.Add(-10 * 24 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := newFakeEngineAt(replayEpoch)
			a := New(eng, "asterisk", nil)

			a.HandleMessage("asterisk/call/call-1/ringing", []byte(ringingPayload("call-1", tc.payloadAt)), false)

			if len(eng.ingested) != 1 {
				t.Fatalf("expected 1 IngestSignal call, got %d", len(eng.ingested))
			}
			got := eng.ingested[0].Since
			want := tc.payloadAt
			if tc.clamped {
				want = replayEpoch
			}
			if !got.Equal(want) {
				t.Errorf("signal Since = %s, want %s (clamped=%v)", got.UTC(), want, tc.clamped)
			}
		})
	}
}

// TestClock_CallSignalTTLExpiresOnInjectedClock proves the safety net is
// reachable through the engine. A ringing call whose hungup never arrives
// must be pruned once the injected clock passes CallTTL. If ExpiresAt were
// stamped from the wall clock it would sit in a different epoch entirely
// and the signal would never expire under the engine's clock.
func TestClock_CallSignalTTLExpiresOnInjectedClock(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(replayEpoch)
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "asterisk", nil)

	a.HandleMessage("asterisk/call/call-1/ringing", []byte(ringingPayload("call-1", replayEpoch)), false)

	engine.Tick()
	if got := len(store.ActiveSignals(clock.Now())); got != 1 {
		t.Fatalf("expected the ringing signal to be active, got %d active signals", got)
	}

	// No hungup ever arrives. Advance past the TTL.
	clock.Set(replayEpoch.Add(CallTTL + time.Minute))
	engine.Tick()

	if got := len(store.ActiveSignals(clock.Now())); got != 0 {
		t.Errorf("expected the signal to expire once the injected clock passed CallTTL, got %d active signals", got)
	}
}

// TestClock_HistoricalReplayDecaysOnInjectedClock is the end-to-end shape
// of the bug the fixture suite hit: replaying a recorded call whose
// timestamps are old in wall-clock terms must still drive house occupancy
// off the injected clock, so the QuietAfter window decays normally.
func TestClock_HistoricalReplayDecaysOnInjectedClock(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(replayEpoch)
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "asterisk", nil)

	ringAt := replayEpoch
	hungupAt := replayEpoch.Add(6 * time.Second)

	clock.Set(ringAt)
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(ringingPayload("call-1", ringAt)), false)
	clock.Set(hungupAt)
	a.HandleMessage("asterisk/call/call-1/hungup", []byte(hungupPayload("call-1", hungupAt)), false)

	// The hangup time is the high-water mark the QuietAfter window runs from.
	if got := store.LastSignalAt(); !got.Equal(hungupAt) {
		t.Fatalf("LastSignalAt = %s, want hangup time %s (a wall-clock read would put this near %s)",
			got.UTC(), hungupAt, time.Now().UTC())
	}

	// Still inside QuietAfter: occupied.
	engine.Tick()
	if got := store.House().Occupancy.State; got != model.OccupancyOccupied {
		t.Errorf("expected occupancy %q within QuietAfter of the hangup, got %q", model.OccupancyOccupied, got)
	}

	// Past QuietAfter: the call no longer counts as presence.
	clock.Set(hungupAt.Add(cfg.House.QuietAfter + time.Minute))
	engine.Tick()
	if got := store.House().Occupancy.State; got == model.OccupancyOccupied {
		t.Errorf("expected occupancy to decay past QuietAfter, still %q", got)
	}
}
