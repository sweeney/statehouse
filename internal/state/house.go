package state

import (
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// isOccupancyRelevant reports whether a device class contributes to
// occupancy signals. Measurement-only sensors (ClassSensor) do not.
func isOccupancyRelevant(class string) bool {
	switch class {
	case device.ClassShortBurst, device.ClassCyclePower, device.ClassContinuous,
		device.ClassMedia, device.ClassBinaryState:
		return true
	}
	return false
}

// isActiveDeviceState reports whether an activity state counts as
// "currently active" for occupancy and activity dimension purposes.
func isActiveDeviceState(s model.DeviceActivityState) bool {
	switch s {
	case model.ActivityActive, model.ActivityRunning, model.ActivityStarting,
		model.ActivityFinishing, model.ActivityFinishedRecently, model.ActivityActiveCycle:
		return true
	}
	return false
}

// isIdleDeviceState reports whether an activity state is a resting /
// measurement-only state that does NOT indicate occupancy by itself.
func isIdleDeviceState(s model.DeviceActivityState) bool {
	switch s {
	case model.ActivityIdle, model.ActivityNormalIdle,
		model.ActivityUnknown, model.ActivityReporting:
		return true
	}
	return false
}

// DeriveHouseState computes three independent semantic dimensions —
// occupancy, activity, and mode — from the current device state.
//
// V1 deliberately avoids any person-level or room-level inference.
//
// TODO: future semantic overlays to layer on top of the mode dimension:
// morning_routine, evening_wind_down, meal_preparation, entertainment,
// laundry_day, overnight_quiet, guest_activity, vacation_pattern,
// workday_pattern, unusual_activity, departure_transition,
// arrival_transition.
func DeriveHouseState(now time.Time, cfg config.HouseConfig, devices map[string]model.Device) model.House {
	// ---------------------------------------------------------------
	// Pass 1: gather signals for all three dimensions in one scan.
	// ---------------------------------------------------------------
	var (
		mostRecentActivity time.Time // last Activity.LastChanged among occupancy-relevant devices
		anyCurrentlyActive bool      // at least one occupancy-relevant device is active right now
		activeCount        int       // number of devices in an "active" state (any class)
	)

	for _, d := range devices {
		// Activity count for house activity dimension (all classes).
		if isActiveDeviceState(d.Activity.State) {
			activeCount++
		}

		// Occupancy signals: only relevant device classes.
		if !isOccupancyRelevant(d.Class) {
			continue
		}
		if isActiveDeviceState(d.Activity.State) {
			anyCurrentlyActive = true
		}
		if !isIdleDeviceState(d.Activity.State) {
			// device is in some interesting state; track LastChanged
			if !d.Activity.LastChanged.IsZero() && d.Activity.LastChanged.After(mostRecentActivity) {
				mostRecentActivity = d.Activity.LastChanged
			}
		} else if !d.Activity.LastChanged.IsZero() && d.Activity.LastChanged.After(mostRecentActivity) {
			// idle/unknown/reporting: the transition-to-idle time is still
			// the last moment we know activity occurred.
			mostRecentActivity = d.Activity.LastChanged
		}
	}

	// ---------------------------------------------------------------
	// Occupancy dimension
	// ---------------------------------------------------------------
	var occ model.OccupancyDimension
	if len(devices) == 0 {
		occ = model.OccupancyDimension{State: model.OccupancyUnknown, Confidence: 0}
	} else if anyCurrentlyActive {
		// At least one relevant device is active right now.
		occ = model.OccupancyDimension{State: model.OccupancyOccupied, Confidence: 0.9}
	} else if mostRecentActivity.IsZero() {
		// Devices exist but none have ever produced an occupancy signal.
		occ = model.OccupancyDimension{State: model.OccupancyUnknown, Confidence: 0}
	} else {
		since := now.Sub(mostRecentActivity)
		switch {
		case cfg.QuietAfter > 0 && since < cfg.QuietAfter:
			// Recently active, now idle — still occupied.
			occ = model.OccupancyDimension{State: model.OccupancyOccupied, Confidence: 0.7}
		case cfg.EmptyAfter > 0 && since < cfg.EmptyAfter:
			// Between QuietAfter and EmptyAfter — uncertain.
			occ = model.OccupancyDimension{State: model.OccupancyUnknown, Confidence: 0.5}
		default:
			// Beyond EmptyAfter with no activity.
			occ = model.OccupancyDimension{State: model.OccupancyEmpty, Confidence: 0.85}
		}
	}

	// ---------------------------------------------------------------
	// House activity dimension
	// ---------------------------------------------------------------
	var act model.HouseActivityDimension
	if len(devices) == 0 {
		act = model.HouseActivityDimension{State: model.HouseActivityUnknown, Confidence: 0}
	} else {
		switch {
		case activeCount == 0:
			act = model.HouseActivityDimension{State: model.HouseActivityIdle, Confidence: 0.8}
		case activeCount == 1:
			act = model.HouseActivityDimension{State: model.HouseActivityQuiet, Confidence: 0.75}
		case activeCount <= 3:
			act = model.HouseActivityDimension{State: model.HouseActivityActive, Confidence: 0.8}
		default:
			act = model.HouseActivityDimension{State: model.HouseActivityBusy, Confidence: 0.85}
		}
	}

	// ---------------------------------------------------------------
	// Mode dimension
	//
	// Mode is inferred primarily from occupancy + activity + sustained
	// quiet duration. Time of day is a secondary confidence modifier.
	// ---------------------------------------------------------------
	quietDuration := time.Duration(0)
	if !mostRecentActivity.IsZero() {
		quietDuration = now.Sub(mostRecentActivity)
	}
	nightHour := now.Hour() >= 22 || now.Hour() < 7
	dayHour := now.Hour() >= 7 && now.Hour() < 22

	var mode model.ModeDimension
	switch {
	case occ.State == model.OccupancyEmpty && cfg.EmptyAfter > 0 && quietDuration > cfg.EmptyAfter:
		// House is confidently empty for longer than EmptyAfter → Away.
		mode = model.ModeDimension{State: model.ModeAway, Confidence: occ.Confidence}

	case (occ.State == model.OccupancyOccupied || occ.State == model.OccupancyUnknown) &&
		(act.State == model.HouseActivityIdle || act.State == model.HouseActivityQuiet) &&
		cfg.SleepingAfter > 0 && quietDuration > cfg.SleepingAfter:
		// Occupied (or unknown, giving benefit of doubt), activity low,
		// and sustained quiet longer than SleepingAfter → Sleeping.
		conf := 0.7
		if nightHour {
			conf += 0.15
		}
		if conf > 0.92 {
			conf = 0.92
		}
		mode = model.ModeDimension{State: model.ModeSleeping, Confidence: conf}

	case occ.State == model.OccupancyOccupied &&
		(act.State == model.HouseActivityActive || act.State == model.HouseActivityBusy):
		// Occupied and active → Day mode.
		ratio := float64(activeCount)
		if ratio > 3 {
			ratio = 3
		}
		conf := 0.7 + 0.1*(ratio/3)
		if dayHour {
			conf += 0.1
		}
		if conf > 1.0 {
			conf = 1.0
		}
		mode = model.ModeDimension{State: model.ModeDay, Confidence: conf}

	case occ.State == model.OccupancyOccupied &&
		act.State == model.HouseActivityIdle &&
		(cfg.SleepingAfter == 0 || quietDuration < cfg.SleepingAfter):
		// Occupied and idle, not long enough for sleeping → time-based.
		if nightHour {
			mode = model.ModeDimension{State: model.ModeNight, Confidence: 0.65}
		} else {
			mode = model.ModeDimension{State: model.ModeDay, Confidence: 0.6}
		}

	default:
		mode = model.ModeDimension{State: model.ModeUnknown, Confidence: 0}
	}

	return model.House{
		Occupancy: occ,
		Activity:  act,
		Mode:      mode,
	}
}
