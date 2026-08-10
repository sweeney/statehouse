package meter

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

const meterTopic = "energy/001122AABBCC/SENSOR/electricitymeter"
const glowTopic = "energy/001122AABBCC/SENSOR/glowsensorth1/00CAFEBABE01"

func meterAt(ts time.Time) string {
	return fmt.Sprintf(
		`{"electricitymeter":{"timestamp":"%s","energy":{"import":{"cumulative":6252.217,"day":32.715,`+
			`"week":90.226,"month":300.821,"units":"kWh"}},"power":{"value":1.011,"units":"kW"}}}`,
		ts.Format(time.RFC3339))
}

func glowAt(ts time.Time) string {
	return fmt.Sprintf(
		`{"glowsensorth1":{"00CAFEBABE01":{"timestamp":"%s","temperature":{"value":19.073,"units":"°C"},`+
			`"humidity":{"value":46,"units":"%%"},"battery":{"value":55,"units":"%%"},"status":"connected"}}}`,
		ts.Format(time.RFC3339))
}

// TestClock_MeterTimestampComesFromEngineClock is the core case for the
// electricity meter handler.
func TestClock_MeterTimestampComesFromEngineClock(t *testing.T) {
	a, store, _ := mkAdapterAt(t, replayEpoch)

	readingAt := replayEpoch.Add(-19 * time.Second)
	a.HandleMessage(meterTopic, []byte(meterAt(readingAt)), false)

	dev, ok := store.Get("001122AABBCC")
	if !ok {
		t.Fatal("meter device not found in store")
	}
	if got := dev.Latest.LastSeen; !got.Equal(readingAt) {
		t.Errorf("LastSeen = %s, want payload timestamp %s (a wall-clock read would put this near %s)",
			got.UTC(), readingAt, time.Now().UTC())
	}
}

// TestClock_GlowSensorTimestampComesFromEngineClock covers the second
// handler, which derives its own now.
func TestClock_GlowSensorTimestampComesFromEngineClock(t *testing.T) {
	a, store, _ := mkAdapterAt(t, replayEpoch)

	readingAt := replayEpoch.Add(-55 * time.Second)
	a.HandleMessage(glowTopic, []byte(glowAt(readingAt)), false)

	dev, ok := store.Get("00CAFEBABE01")
	if !ok {
		t.Fatal("glow sensor device not found in store")
	}
	if got := dev.Latest.LastSeen; !got.Equal(readingAt) {
		t.Errorf("LastSeen = %s, want payload timestamp %s", got.UTC(), readingAt)
	}
}

// TestClock_MissingTimestampFallsBackToEngineClock covers the fallback path
// on both handlers.
func TestClock_MissingTimestampFallsBackToEngineClock(t *testing.T) {
	t.Run("electricity meter", func(t *testing.T) {
		a, store, _ := mkAdapterAt(t, replayEpoch)
		payload := `{"electricitymeter":{"energy":{"import":{"cumulative":6252.217,"units":"kWh"}},"power":{"value":1.011,"units":"kW"}}}`
		a.HandleMessage(meterTopic, []byte(payload), false)

		dev, ok := store.Get("001122AABBCC")
		if !ok {
			t.Fatal("meter device not found in store")
		}
		if got := dev.Latest.LastSeen; !got.Equal(replayEpoch) {
			t.Errorf("LastSeen = %s, want engine clock now %s", got.UTC(), replayEpoch)
		}
	})

	t.Run("glow sensor", func(t *testing.T) {
		a, store, _ := mkAdapterAt(t, replayEpoch)
		payload := `{"glowsensorth1":{"00CAFEBABE01":{"temperature":{"value":19.073,"units":"°C"},"status":"connected"}}}`
		a.HandleMessage(glowTopic, []byte(payload), false)

		dev, ok := store.Get("00CAFEBABE01")
		if !ok {
			t.Fatal("glow sensor device not found in store")
		}
		if got := dev.Latest.LastSeen; !got.Equal(replayEpoch) {
			t.Errorf("LastSeen = %s, want engine clock now %s", got.UTC(), replayEpoch)
		}
	})
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

			a.HandleMessage(meterTopic, []byte(meterAt(tc.at)), false)

			dev, ok := store.Get("001122AABBCC")
			if !ok {
				t.Fatal("meter device not found in store")
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
