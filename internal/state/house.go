package state

import (
	"sort"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// DeriveHouseState produces a conservative whole-house summary from
// the current device state. V1 deliberately avoids any person-level
// or room-level inference.
//
// Logic:
//   - Any "interesting" appliance (short burst / cycle / media)
//     currently active -> house is active.
//   - Continuous compressor activity is *not* on its own a strong
//     occupancy signal — fridges run unattended.
//   - Recent activity within cfg.QuietAfter keeps the house occupied
//     even after the appliance returns to idle.
//   - No activity for cfg.EmptyAfter -> house is empty.
//   - In between quiet_after and empty_after -> quiet.
//   - If nothing has ever been seen, state stays unknown.
func DeriveHouseState(now time.Time, cfg config.HouseConfig, devices map[string]model.Device) model.House {
	var (
		signals    []string
		anyActive  bool
		latestSeen time.Time
	)

	for id, d := range devices {
		if !d.Latest.LastSeen.IsZero() && d.Latest.LastSeen.After(latestSeen) {
			latestSeen = d.Latest.LastSeen
		}
		switch d.Class {
		case device.ClassShortBurst:
			if d.Activity.State == model.ActivityActive {
				anyActive = true
				signals = append(signals, id+"_active")
			}
		case device.ClassCyclePower:
			if d.Activity.State == model.ActivityRunning || d.Activity.State == model.ActivityStarting {
				anyActive = true
				signals = append(signals, id+"_running")
			}
		case device.ClassMedia:
			if d.Activity.State == model.ActivityActive {
				anyActive = true
				signals = append(signals, id+"_active")
			}
		case device.ClassBinaryState:
			if d.Activity.State == model.ActivityActive {
				anyActive = true
				signals = append(signals, id+"_on")
			}
		case device.ClassContinuous:
			// continuous devices do not on their own indicate occupancy
		}
	}
	sort.Strings(signals)

	h := model.House{State: model.HouseUnknown, Confidence: 0.5, Signals: signals}
	if latestSeen.IsZero() {
		return h
	}
	if anyActive {
		h.State = model.HouseActive
		h.Confidence = 0.85
		return h
	}
	// Use the most recent activity end as the "last activity" mark.
	mostRecentActivity := time.Time{}
	for _, d := range devices {
		switch d.Class {
		case device.ClassShortBurst, device.ClassCyclePower, device.ClassMedia, device.ClassBinaryState:
			if !d.Activity.LastChanged.IsZero() && d.Activity.LastChanged.After(mostRecentActivity) {
				mostRecentActivity = d.Activity.LastChanged
			}
		}
	}
	if mostRecentActivity.IsZero() {
		// Never observed any human-driven activity, but we have telemetry.
		h.State = model.HouseQuiet
		h.Confidence = 0.5
		return h
	}
	since := now.Sub(mostRecentActivity)
	switch {
	case cfg.QuietAfter > 0 && since < cfg.QuietAfter:
		h.State = model.HouseOccupied
		h.Confidence = 0.7
	case cfg.EmptyAfter > 0 && since >= cfg.EmptyAfter:
		h.State = model.HouseEmpty
		h.Confidence = 0.6
	default:
		h.State = model.HouseQuiet
		h.Confidence = 0.6
	}
	return h
}
