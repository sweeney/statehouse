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

	h := DeriveHouseState(now, cfg, devices)
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

	h := DeriveHouseState(now, cfg, devices)
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

	h := DeriveHouseState(now, cfg, map[string]model.Device{})

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

	h := DeriveHouseState(now, cfg, devices)
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

	h := DeriveHouseState(now, cfg, devices)
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

	h := DeriveHouseState(now, cfg, devices)
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

	h := DeriveHouseState(now, cfg, devices)
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

	h := DeriveHouseState(now, cfg, devices)
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
	h := DeriveHouseState(now, cfg, devices)
	if len(h.ActiveDevices) != 1 || h.ActiveDevices[0] != "boiler" {
		t.Errorf("expected [boiler] in ActiveDevices, got %v", h.ActiveDevices)
	}
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
	h := DeriveHouseState(now, cfg, devices)
	if len(h.ActiveDevices) != 0 {
		t.Errorf("expected empty ActiveDevices when all idle, got %v", h.ActiveDevices)
	}
}
