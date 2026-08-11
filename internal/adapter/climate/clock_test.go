package climate

import (
	"fmt"
	"testing"
	"time"
)

// These tests pin the adapter's notion of "now" to the engine it feeds.
//
// The adapter stamps every reading with a timestamp and sanitises the
// publisher's against a window around now. If that now comes from the wall
// clock rather than the engine, readings land in a different time base from
// the engine consuming them, and anything derived from reading time —
// staleness, cycle detection, lifetime extremes — is computed across a
// mixed epoch.
//
// replayEpoch is deliberately ancient. timeutil rejects timestamps more
// than 30 days behind now, so a wall-clock read collapses these payloads to
// the real current time and the assertions below fail on any date the suite
// is run. Asserting against a fixed epoch rather than time.Now() is what
// stops these tests rotting into the bug they exist to catch.
var replayEpoch = time.Date(2020, 3, 1, 9, 0, 0, 0, time.UTC)

func observationAt(ts time.Time, extra string) string {
	return fmt.Sprintf(`{"timestamp":%d,%s}`, ts.Unix(), extra)
}

// TestClock_ObservationTimestampComesFromEngineClock is the core case: a
// payload timestamp that agrees with the engine clock survives sanitisation
// and becomes the reading time.
func TestClock_ObservationTimestampComesFromEngineClock(t *testing.T) {
	a, store, _ := mkAdapterAt(t, replayEpoch)

	obsAt := replayEpoch.Add(-30 * time.Second)
	a.HandleMessage("climate/home/observation", []byte(observationAt(obsAt, `"temperature_c":7.69`)), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	if got := dev.Latest.LastSeen; !got.Equal(obsAt) {
		t.Errorf("LastSeen = %s, want payload timestamp %s (a wall-clock read would put this near %s)",
			got.UTC(), obsAt, time.Now().UTC())
	}
}

// TestClock_MissingTimestampFallsBackToEngineClock covers the fallback: a
// payload with no usable timestamp must be stamped from the engine clock.
func TestClock_MissingTimestampFallsBackToEngineClock(t *testing.T) {
	a, store, _ := mkAdapterAt(t, replayEpoch)

	a.HandleMessage("climate/home/observation", []byte(`{"temperature_c":7.69}`), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	if got := dev.Latest.LastSeen; !got.Equal(replayEpoch) {
		t.Errorf("LastSeen = %s, want engine clock now %s", got.UTC(), replayEpoch)
	}
}

// TestClock_SanitisationIsRelativeToEngineClock guards the timestamp
// poisoning defence: out-of-window values are still rejected, but the
// window is measured from the engine clock, and the substituted value is
// the engine's now — exactly, not "approximately wall-clock now".
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

			a.HandleMessage("climate/home/observation", []byte(observationAt(tc.at, `"temperature_c":7.69`)), false)

			dev, ok := store.Get("home")
			if !ok {
				t.Fatal("climate/home device not found in store")
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

// TestClock_DeviceStatusTimestampComesFromEngineClock covers the second
// handler, which has its own now.
func TestClock_DeviceStatusTimestampComesFromEngineClock(t *testing.T) {
	a, store, _ := mkAdapterAt(t, replayEpoch)

	statusAt := replayEpoch.Add(-15 * time.Second)
	a.HandleMessage("climate/home/device/status", []byte(observationAt(statusAt, `"rssi_dbm":-60`)), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found after device/status")
	}
	if got := dev.Latest.LastSeen; !got.Equal(statusAt) {
		t.Errorf("LastSeen = %s, want payload timestamp %s", got.UTC(), statusAt)
	}
}
