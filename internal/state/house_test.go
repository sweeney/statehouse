package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// makeBinaryDevice builds a minimal Device with ClassBinaryState. The
// activity LastChanged is set to idledAgo before now, and the activity
// state is set to activeState.
func makeBinaryDevice(id string, activeState model.ActivityState, lastChanged time.Time) model.Device {
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

// TestDeriveHouseState_BinaryStateIdleWithinQuietAfter verifies that a
// binary-state device (e.g. boiler relay) that went idle 5 minutes ago
// keeps the house in HouseOccupied while it is still within the
// QuietAfter window. This is the regression test for issue #32.
func TestDeriveHouseState_BinaryStateIdleWithinQuietAfter(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := config.HouseConfig{
		QuietAfter: 30 * time.Minute,
		EmptyAfter: 2 * time.Hour,
	}
	// Device went idle 5 minutes ago — well inside the 30-min QuietAfter window.
	idledAt := now.Add(-5 * time.Minute)
	devices := map[string]model.Device{
		"boiler": makeBinaryDevice("boiler", model.ActivityIdle, idledAt),
	}

	h := DeriveHouseState(now, cfg, devices)
	if h.State != model.HouseOccupied {
		t.Errorf("expected HouseOccupied while within QuietAfter window, got %q", h.State)
	}
}

// TestDeriveHouseState_BinaryStateCurrentlyActive verifies that a
// binary-state device that is currently active drives HouseActive.
func TestDeriveHouseState_BinaryStateCurrentlyActive(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cfg := config.HouseConfig{
		QuietAfter: 30 * time.Minute,
		EmptyAfter: 2 * time.Hour,
	}
	// Device is still on — it went active 5 minutes ago and has not yet
	// gone idle.
	activeSince := now.Add(-5 * time.Minute)
	devices := map[string]model.Device{
		"boiler": makeBinaryDevice("boiler", model.ActivityActive, activeSince),
	}

	h := DeriveHouseState(now, cfg, devices)
	if h.State != model.HouseActive {
		t.Errorf("expected HouseActive while binary device is active, got %q", h.State)
	}
}
