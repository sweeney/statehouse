package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// makeBinaryDevice builds a minimal Device with ClassBinaryState. The
// activity LastChanged is set to lastChanged and the activity state is
// set to activeState.
func makeBinaryDevice(id string, activeState model.DeviceActivityState, lastChanged time.Time) model.Device {
	return model.Device{
		ID:    id,
		Class: device.ClassBinaryState,
		Activity: model.Activity{
			State:       activeState,
			LastChanged: lastChanged,
			Confidence:  0.9,
		},
		Latest: model.Latest{
			LastSeen: lastChanged,
		},
	}
}

// defaultCfg returns a HouseConfig with typical test values.
func defaultCfg() config.HouseConfig {
	return config.HouseConfig{
		QuietAfter:    30 * time.Minute,
		EmptyAfter:    2 * time.Hour,
		SleepingAfter: 2 * time.Hour,
	}
}

// TestDeriveHouseState_BinaryStateIdleWithinQuietAfter verifies that a
// binary-state device (e.g. boiler relay) that went idle 5 minutes ago
// keeps the house occupancy as OccupancyOccupied while it is still
// within the QuietAfter window. This is the regression test for issue #32.
func TestDeriveHouseState_BinaryStateIdleWithinQuietAfter(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	// Device went idle 5 minutes ago — well inside the 30-min QuietAfter window.
	idledAt := now.Add(-5 * time.Minute)
	devices := map[string]model.Device{
		"boiler": makeBinaryDevice("boiler", model.ActivityIdle, idledAt),
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied while within QuietAfter window, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_BinaryStateCurrentlyActive verifies that a
// binary-state device that is currently active drives OccupancyOccupied
// (with high confidence) and HouseActivityQuiet (one active device).
func TestDeriveHouseState_BinaryStateCurrentlyActive(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	// Device is still on — it went active 5 minutes ago and has not yet
	// gone idle.
	activeSince := now.Add(-5 * time.Minute)
	devices := map[string]model.Device{
		"boiler": makeBinaryDevice("boiler", model.ActivityActive, activeSince),
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied while binary device is active, got %q", h.Occupancy.State)
	}
	if h.Activity.State != model.HouseActivityQuiet {
		t.Errorf("expected HouseActivityQuiet for one active device, got %q", h.Activity.State)
	}
}

// TestDeriveHouseState_NoDevices verifies that an empty device map
// produces all-unknown dimensions.
func TestDeriveHouseState_NoDevices(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()

	h := DeriveHouseState(now, cfg, map[string]model.Device{}, nil, time.Time{})

	if h.Occupancy.State != model.OccupancyUnknown {
		t.Errorf("expected OccupancyUnknown for empty device map, got %q", h.Occupancy.State)
	}
	if h.Activity.State != model.HouseActivityUnknown {
		t.Errorf("expected HouseActivityUnknown for empty device map, got %q", h.Activity.State)
	}
	if h.Mode.State != model.ModeUnknown {
		t.Errorf("expected ModeUnknown for empty device map, got %q", h.Mode.State)
	}
}

// TestDeriveHouseState_OccupancyOccupiedWhenDeviceActive verifies that a
// currently-active short-burst device produces OccupancyOccupied.
func TestDeriveHouseState_OccupancyOccupiedWhenDeviceActive(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	activeSince := now.Add(-2 * time.Minute)
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityActive,
				LastChanged: activeSince,
				Confidence:  0.9,
			},
			Latest: model.Latest{LastSeen: activeSince},
		},
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied for active device, got %q", h.Occupancy.State)
	}
	if h.Occupancy.Confidence < 0.89 {
		t.Errorf("expected high confidence (>=0.9) for active device, got %.2f", h.Occupancy.Confidence)
	}
}

// TestDeriveHouseState_OccupancyEmptyAfterTimeout verifies that a device
// whose last activity exceeds EmptyAfter produces OccupancyEmpty.
func TestDeriveHouseState_OccupancyEmptyAfterTimeout(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	// Last active 3 hours ago — beyond the 2-hour EmptyAfter threshold.
	lastActive := now.Add(-3 * time.Hour)
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: lastActive,
				Confidence:  0.9,
			},
			Latest: model.Latest{LastSeen: lastActive},
		},
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyEmpty {
		t.Errorf("expected OccupancyEmpty after EmptyAfter timeout, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_ActivityCountsActiveDevices verifies that two
// active devices produce HouseActivityActive.
func TestDeriveHouseState_ActivityCountsActiveDevices(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	activeSince := now.Add(-10 * time.Minute)
	makeActive := func(id, class string) model.Device {
		return model.Device{
			ID:    id,
			Class: class,
			Activity: model.Activity{
				State:       model.ActivityActive,
				LastChanged: activeSince,
				Confidence:  0.9,
			},
			Latest: model.Latest{LastSeen: activeSince},
		}
	}
	devices := map[string]model.Device{
		"kettle":  makeActive("kettle", device.ClassShortBurst),
		"toaster": makeActive("toaster", device.ClassShortBurst),
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if h.Activity.State != model.HouseActivityActive {
		t.Errorf("expected HouseActivityActive for 2 active devices, got %q", h.Activity.State)
	}
}

// TestDeriveHouseState_ModeAway verifies that an empty house with no
// recent activity for longer than EmptyAfter yields ModeAway.
func TestDeriveHouseState_ModeAway(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	// Last active 4 hours ago — beyond both EmptyAfter (2h) and SleepingAfter (2h).
	lastActive := now.Add(-4 * time.Hour)
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: lastActive,
				Confidence:  0.9,
			},
			Latest: model.Latest{LastSeen: lastActive},
		},
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyEmpty {
		t.Errorf("expected OccupancyEmpty before checking ModeAway, got %q", h.Occupancy.State)
	}
	if h.Mode.State != model.ModeAway {
		t.Errorf("expected ModeAway for long-empty house, got %q", h.Mode.State)
	}
}

// TestDeriveHouseState_ModeSleeping verifies that an occupied house with
// low activity and sustained quiet longer than SleepingAfter yields
// ModeSleeping.
func TestDeriveHouseState_ModeSleeping(t *testing.T) {
	// Use a nighttime hour to also test confidence boost.
	now := time.Date(2026, 5, 14, 23, 0, 0, 0, time.UTC)
	cfg := defaultCfg() // SleepingAfter = 2h
	// Last active 3 hours ago — within EmptyAfter enough to remain
	// OccupancyOccupied... wait, 3h > EmptyAfter (2h). Use QuietAfter < elapsed < EmptyAfter
	// to get OccupancyUnknown, which also triggers Sleeping. But OccupancyOccupied with
	// elapsed > SleepingAfter also works. Let's use 30min < elapsed < 2h scenario:
	// elapsed = 90min → OccupancyUnknown (between QuietAfter=30min and EmptyAfter=2h),
	// SleepingAfter=2h, quietDuration=90min < SleepingAfter. That won't trigger sleeping.
	//
	// For sleeping: need occupied (or unknown) + idle + quietDuration > SleepingAfter(2h).
	// Use OccupancyOccupied: elapsed must be < QuietAfter = 30min, but then quietDuration
	// < SleepingAfter. Contradiction — can't have QuietAfter=30min and SleepingAfter=2h
	// and elapsed < QuietAfter AND elapsed > SleepingAfter simultaneously.
	//
	// Use a smaller SleepingAfter for this test.
	cfg.SleepingAfter = 45 * time.Minute // smaller than EmptyAfter
	// elapsed = 90 min: beyond QuietAfter(30min) → OccupancyUnknown;
	// beyond SleepingAfter(45min) but below EmptyAfter(2h) → sleeping triggers.
	lastActive := now.Add(-90 * time.Minute)
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: lastActive,
				Confidence:  0.9,
			},
			Latest: model.Latest{LastSeen: lastActive},
		},
	}

	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	// OccupancyUnknown gives sleeping "benefit of doubt".
	if h.Occupancy.State != model.OccupancyUnknown {
		t.Errorf("expected OccupancyUnknown at 90min with QuietAfter=30m/EmptyAfter=2h, got %q", h.Occupancy.State)
	}
	if h.Mode.State != model.ModeSleeping {
		t.Errorf("expected ModeSleeping for occupied+idle+quiet>SleepingAfter, got %q", h.Mode.State)
	}
	// Night hour should boost confidence above 0.7 base.
	if h.Mode.Confidence <= 0.7 {
		t.Errorf("expected confidence boosted above 0.7 for night hour, got %.2f", h.Mode.Confidence)
	}
}

func TestDeriveHouseState_ActiveDevicesList(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	devices := map[string]model.Device{
		"boiler": makeBinaryDevice("boiler", model.ActivityActive, now.Add(-1*time.Minute)),
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: now.Add(-1 * time.Minute),
				Confidence:  0.9,
			},
		},
	}
	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if len(h.ActiveDevices) != 1 || h.ActiveDevices[0] != "boiler" {
		t.Errorf("expected [boiler] in ActiveDevices, got %v", h.ActiveDevices)
	}
}

// TestDeriveHouseState_ModeNight_HonoursConfiguredTimezone verifies that
// the night-hour classification uses the operator's configured timezone
// rather than UTC. 23:00 local in San Francisco is 06:00 UTC the next day —
// without honouring the timezone the mode would resolve to Day.
func TestDeriveHouseState_ModeNight_HonoursConfiguredTimezone(t *testing.T) {
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tz database unavailable: %v", err)
	}
	cfg := config.HouseConfig{
		QuietAfter:    30 * time.Minute,
		EmptyAfter:    2 * time.Hour,
		SleepingAfter: 4 * time.Hour, // longer than elapsed → not sleeping
		Timezone:      "America/Los_Angeles",
	}

	// 23:00 Pacific is night-local but day-UTC.
	nowLocal := time.Date(2026, 5, 14, 23, 0, 0, 0, pacific)
	lastActive := nowLocal.Add(-5 * time.Minute)
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: lastActive,
				Confidence:  0.9,
			},
			Latest: model.Latest{LastSeen: lastActive},
		},
	}

	h := DeriveHouseState(nowLocal, cfg, devices, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Fatalf("expected OccupancyOccupied 5min after activity, got %q", h.Occupancy.State)
	}
	if h.Mode.State != model.ModeNight {
		t.Errorf("expected ModeNight at 23:00 Pacific (06:00 UTC), got %q", h.Mode.State)
	}
}

// TestHouseConfig_LocationFallback asserts that an invalid timezone string
// falls back to UTC rather than panicking.
func TestHouseConfig_LocationFallback(t *testing.T) {
	cfg := config.HouseConfig{Timezone: "Not/A/Real/Zone"}
	if loc := cfg.Location(); loc != time.UTC {
		t.Errorf("expected UTC fallback for invalid tz, got %v", loc)
	}
}

// TestDeviceActivityStates_AreExhaustivelyClassified asserts that every
// declared DeviceActivityState is bucketed by isActiveDeviceState or
// isIdleDeviceState. Adding a new state without classifying it breaks
// occupancy/activity inference; this test catches that at CI time.
func TestDeviceActivityStates_AreExhaustivelyClassified(t *testing.T) {
	allStates := []model.DeviceActivityState{
		model.ActivityUnknown, model.ActivityIdle, model.ActivityActive,
		model.ActivityStarting, model.ActivityRunning, model.ActivityFinishing,
		model.ActivityFinishedRecently, model.ActivityStandby,
		model.ActivityNormalIdle, model.ActivityActiveCycle, model.ActivityReporting,
	}
	for _, s := range allStates {
		active := isActiveDeviceState(s)
		idle := isIdleDeviceState(s)
		if !active && !idle {
			t.Errorf("state %q is in neither active nor idle bucket", s)
		}
		if active && idle {
			t.Errorf("state %q is in both active and idle buckets", s)
		}
	}
}

func TestDeriveHouseState_ActiveDevicesSortedAndStable(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	devices := map[string]model.Device{
		"zebra":  makeBinaryDevice("zebra", model.ActivityActive, now.Add(-1*time.Minute)),
		"alpha":  makeBinaryDevice("alpha", model.ActivityActive, now.Add(-1*time.Minute)),
		"monkey": makeBinaryDevice("monkey", model.ActivityActive, now.Add(-1*time.Minute)),
	}
	want := []string{"alpha", "monkey", "zebra"}
	for i := 0; i < 50; i++ {
		h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
		if len(h.ActiveDevices) != len(want) {
			t.Fatalf("iter %d: len=%d want %d (%v)", i, len(h.ActiveDevices), len(want), h.ActiveDevices)
		}
		for j := range want {
			if h.ActiveDevices[j] != want[j] {
				t.Fatalf("iter %d: got %v want %v", i, h.ActiveDevices, want)
			}
		}
	}
}

// TestDeriveHouseState_ContinuousDeviceExcludedFromActivity verifies that
// a continuous_power_device in active_cycle does not appear in ActiveDevices
// and does not inflate the house activity dimension.
func TestDeriveHouseState_ContinuousDeviceExcludedFromActivity(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	activeSince := now.Add(-5 * time.Minute)
	devices := map[string]model.Device{
		"bigfridge": {
			ID:    "bigfridge",
			Class: device.ClassContinuous,
			Activity: model.Activity{
				State:       model.ActivityActiveCycle,
				LastChanged: activeSince,
				Confidence:  0.9,
			},
		},
	}
	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if len(h.ActiveDevices) != 0 {
		t.Errorf("continuous device must not appear in ActiveDevices, got %v", h.ActiveDevices)
	}
	if h.Activity.State != model.HouseActivityIdle {
		t.Errorf("expected HouseActivityIdle with only continuous devices, got %q", h.Activity.State)
	}
}

// TestDeriveHouseState_ContinuousDoesNotContributeToOccupancy verifies that
// a continuous_power_device does not update mostRecentActivity and therefore
// cannot prevent the house from reaching OccupancyEmpty.
func TestDeriveHouseState_ContinuousDoesNotContributeToOccupancy(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg() // EmptyAfter = 2h
	// Fridge was last "active" 3 hours ago — but continuous devices are excluded.
	lastSeen := now.Add(-3 * time.Hour)
	devices := map[string]model.Device{
		"bigfridge": {
			ID:    "bigfridge",
			Class: device.ClassContinuous,
			Activity: model.Activity{
				State:       model.ActivityNormalIdle,
				LastChanged: lastSeen,
				Confidence:  0.9,
			},
		},
	}
	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	// With no occupancy-relevant devices, we expect unknown (not empty or occupied).
	if h.Occupancy.State != model.OccupancyUnknown {
		t.Errorf("continuous-only device map should yield OccupancyUnknown, got %q", h.Occupancy.State)
	}
}

// --- Signal tests ---

func makeCallSignal(id string, since time.Time) model.ActivitySignal {
	return model.ActivitySignal{
		ID:         id,
		Source:     "intercom",
		Type:       "call_active",
		Confidence: 0.9,
		Since:      since,
	}
}

// TestDeriveHouseState_SignalAloneProducesOccupied verifies that a
// single active signal with no devices drives OccupancyOccupied.
func TestDeriveHouseState_SignalAloneProducesOccupied(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	signals := []model.ActivitySignal{makeCallSignal("call-1", now.Add(-2*time.Minute))}

	h := DeriveHouseState(now, cfg, map[string]model.Device{}, signals, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied from active signal, got %q", h.Occupancy.State)
	}
	if h.Activity.State != model.HouseActivityQuiet {
		t.Errorf("expected HouseActivityQuiet for one signal, got %q", h.Activity.State)
	}
}

// TestDeriveHouseState_NoDevicesNoSignalsIsUnknown verifies that an
// empty device map and empty signal slice produces all-unknown dimensions.
func TestDeriveHouseState_NoDevicesNoSignalsIsUnknown(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	h := DeriveHouseState(now, defaultCfg(), map[string]model.Device{}, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyUnknown {
		t.Errorf("expected OccupancyUnknown with no sources, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_SignalAndIdleDeviceCombine verifies that an active
// signal overrides an otherwise-idle device map to produce OccupancyOccupied.
func TestDeriveHouseState_SignalAndIdleDeviceCombine(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	// Device went idle 3 hours ago — beyond EmptyAfter (2h).
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: "short_burst_power_device",
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: now.Add(-3 * time.Hour),
			},
		},
	}
	signals := []model.ActivitySignal{makeCallSignal("call-1", now.Add(-1*time.Minute))}

	h := DeriveHouseState(now, cfg, devices, signals, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied with active signal despite idle device, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_TwoSignalsProducesActive verifies that two active
// signals produce HouseActivityActive (activeCount==2).
func TestDeriveHouseState_TwoSignalsProducesActive(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	signals := []model.ActivitySignal{
		makeCallSignal("call-1", now.Add(-2*time.Minute)),
		makeCallSignal("call-2", now.Add(-1*time.Minute)),
	}
	h := DeriveHouseState(now, defaultCfg(), map[string]model.Device{}, signals, time.Time{})
	if h.Activity.State != model.HouseActivityActive {
		t.Errorf("expected HouseActivityActive for 2 signals, got %q", h.Activity.State)
	}
}

// TestDeriveHouseState_SignalContributesToMostRecentActivity verifies
// that the most recent signal Since time feeds into the quiet/empty timeout
// so that a house with only an old idle device but a recent signal does
// not reach OccupancyEmpty.
func TestDeriveHouseState_SignalContributesToMostRecentActivity(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg() // QuietAfter=30m, EmptyAfter=2h
	// No devices. Signal fired 10 minutes ago — within QuietAfter.
	signals := []model.ActivitySignal{makeCallSignal("call-1", now.Add(-10*time.Minute))}
	// Clear the signal so it is no longer active (caller passes empty slice).
	h := DeriveHouseState(now, cfg, map[string]model.Device{}, nil, time.Time{})
	if h.Occupancy.State != model.OccupancyUnknown {
		t.Errorf("pre-condition: expected OccupancyUnknown with no sources")
	}
	// But with the signal's Since feeding mostRecentActivity, result should be Occupied.
	h = DeriveHouseState(now, cfg, map[string]model.Device{}, signals, time.Time{})
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied while signal is active, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_LastSignalAtLingers verifies that a cleared signal
// still contributes to the QuietAfter window via lastSignalAt, so the house
// doesn't snap immediately to unknown/empty when a call ends.
func TestDeriveHouseState_LastSignalAtLingers(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg() // QuietAfter=30m
	// Call ended 5 minutes ago — within QuietAfter.
	hangupAt := now.Add(-5 * time.Minute)

	// No active signals, no devices, but lastSignalAt is recent.
	h := DeriveHouseState(now, cfg, map[string]model.Device{}, nil, hangupAt)
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected OccupancyOccupied within QuietAfter of last signal, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_LastSignalAtExpires verifies that once lastSignalAt
// is beyond EmptyAfter, the house reaches OccupancyEmpty (no other sources).
func TestDeriveHouseState_LastSignalAtExpires(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg() // EmptyAfter=2h
	// Call ended 3 hours ago — beyond EmptyAfter.
	hangupAt := now.Add(-3 * time.Hour)

	h := DeriveHouseState(now, cfg, map[string]model.Device{}, nil, hangupAt)
	if h.Occupancy.State != model.OccupancyEmpty {
		t.Errorf("expected OccupancyEmpty after EmptyAfter of last signal, got %q", h.Occupancy.State)
	}
}

// TestDeriveHouseState_ModeNotUnknown_WhenOccupancyUnknownAndIdle verifies
// that a house with unknown occupancy and idle activity produces a time-based
// mode (day or night) rather than mode=unknown. This is the "quiet limbo"
// zone: last activity is beyond QuietAfter but not yet beyond SleepingAfter,
// so occupancy is uncertain but mode should still be meaningful.
func TestDeriveHouseState_ModeNotUnknown_WhenOccupancyUnknownAndIdle(t *testing.T) {
	cfg := defaultCfg() // QuietAfter=30m, EmptyAfter=2h, SleepingAfter=2h
	// last activity 1 hour ago: beyond QuietAfter(30m) → occ=unknown,
	// but not beyond SleepingAfter(2h) → sleeping case won't fire.
	lastActive := func(now time.Time) time.Time { return now.Add(-1 * time.Hour) }
	makeIdleDevice := func(now time.Time) map[string]model.Device {
		return map[string]model.Device{
			"kettle": {
				ID:    "kettle",
				Class: device.ClassShortBurst,
				Activity: model.Activity{
					State:       model.ActivityIdle,
					LastChanged: lastActive(now),
					Confidence:  0.9,
				},
				Latest: model.Latest{LastSeen: lastActive(now)},
			},
		}
	}

	t.Run("daytime", func(t *testing.T) {
		now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC) // 10:00 UTC
		h := DeriveHouseState(now, cfg, makeIdleDevice(now), nil, time.Time{})
		if h.Occupancy.State != model.OccupancyUnknown {
			t.Fatalf("pre-condition: expected OccupancyUnknown, got %q", h.Occupancy.State)
		}
		if h.Mode.State != model.ModeDay {
			t.Errorf("expected ModeDay during daytime with unknown occupancy and idle activity, got %q", h.Mode.State)
		}
	})

	t.Run("nighttime", func(t *testing.T) {
		now := time.Date(2026, 5, 22, 23, 0, 0, 0, time.UTC) // 23:00 UTC
		h := DeriveHouseState(now, cfg, makeIdleDevice(now), nil, time.Time{})
		if h.Occupancy.State != model.OccupancyUnknown {
			t.Fatalf("pre-condition: expected OccupancyUnknown, got %q", h.Occupancy.State)
		}
		if h.Mode.State != model.ModeNight {
			t.Errorf("expected ModeNight during nighttime with unknown occupancy and idle activity, got %q", h.Mode.State)
		}
	})
}

func TestDeriveHouseState_ActiveDevicesEmptyWhenIdle(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := defaultCfg()
	devices := map[string]model.Device{
		"kettle": {
			ID:    "kettle",
			Class: device.ClassShortBurst,
			Activity: model.Activity{
				State:       model.ActivityIdle,
				LastChanged: now.Add(-1 * time.Minute),
				Confidence:  0.9,
			},
		},
	}
	h := DeriveHouseState(now, cfg, devices, nil, time.Time{})
	if len(h.ActiveDevices) != 0 {
		t.Errorf("expected empty ActiveDevices when all idle, got %v", h.ActiveDevices)
	}
}
