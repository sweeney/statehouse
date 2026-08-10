package ups

import (
	"fmt"
	"testing"
	"time"
)

// These tests pin the adapter's notion of "now" to the engine it feeds.
// See the equivalent file in the climate adapter for the full rationale;
// replayEpoch is deliberately ancient so a wall-clock read fails these
// assertions on any date the suite is ever run.
var replayEpoch = time.Date(2020, 3, 1, 9, 0, 0, 0, time.UTC)

func stateAt(ts time.Time) string {
	return fmt.Sprintf(
		`{"timestamp":"%s","ups_name":"cyberpower","variables":{"battery.charge":"100","input.voltage":"244"},`+
			`"computed":{"load_watts":72,"battery_runtime_mins":74.5,"on_battery":false,"low_battery":false}}`,
		ts.Format(time.RFC3339))
}

// TestClock_ReadingTimestampComesFromEngineClock is the core case: a payload
// timestamp agreeing with the engine clock survives sanitisation.
func TestClock_ReadingTimestampComesFromEngineClock(t *testing.T) {
	a, store, _ := mkAdapterAt(t, replayEpoch)

	readingAt := replayEpoch.Add(-6 * time.Second)
	a.HandleMessage("ups/cyberpower/state", []byte(stateAt(readingAt)), false)

	dev, ok := store.Get("cyberpower")
	if !ok {
		t.Fatal("device cyberpower not found in store")
	}
	if got := dev.Latest.LastSeen; !got.Equal(readingAt) {
		t.Errorf("LastSeen = %s, want payload timestamp %s (a wall-clock read would put this near %s)",
			got.UTC(), readingAt, time.Now().UTC())
	}
}

// TestClock_MissingTimestampFallsBackToEngineClock covers the fallback path.
func TestClock_MissingTimestampFallsBackToEngineClock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"absent", `{"ups_name":"cyberpower","computed":{"load_watts":72}}`},
		{"empty", `{"timestamp":"","ups_name":"cyberpower","computed":{"load_watts":72}}`},
		{"unparseable", `{"timestamp":"not-a-timestamp","ups_name":"cyberpower","computed":{"load_watts":72}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, store, _ := mkAdapterAt(t, replayEpoch)

			a.HandleMessage("ups/cyberpower/state", []byte(tc.payload), false)

			dev, ok := store.Get("cyberpower")
			if !ok {
				t.Fatal("device cyberpower not found in store")
			}
			if got := dev.Latest.LastSeen; !got.Equal(replayEpoch) {
				t.Errorf("LastSeen = %s, want engine clock now %s", got.UTC(), replayEpoch)
			}
		})
	}
}

// TestClock_SanitisationIsRelativeToEngineClock guards the poisoning
// defence: the window is measured from the engine clock and the substituted
// value is the engine's now, exactly.
func TestClock_SanitisationIsRelativeToEngineClock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		at      time.Time
		clamped bool
	}{
		{"far future is clamped", replayEpoch.Add(50 * 365 * 24 * time.Hour), true},
		{"far past is clamped", replayEpoch.Add(-60 * 24 * time.Hour), true},
		{"inside the window is kept", replayEpoch.Add(-10 * 24 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, store, _ := mkAdapterAt(t, replayEpoch)

			a.HandleMessage("ups/cyberpower/state", []byte(stateAt(tc.at)), false)

			dev, ok := store.Get("cyberpower")
			if !ok {
				t.Fatal("device cyberpower not found in store")
			}
			want := tc.at
			if tc.clamped {
				want = replayEpoch
			}
			if got := dev.Latest.LastSeen; !got.Equal(want) {
				t.Errorf("LastSeen = %s, want %s (clamped=%v)", got.UTC(), want, tc.clamped)
			}
		})
	}
}
