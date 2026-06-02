package state

import (
	"sort"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// HouseDeviceID is the synthetic device id used on canonical events
// produced by the whole-house electricity aggregator. The leading
// underscore is reserved: no resolver path (configured override,
// name-hint, or deriveID fallback) produces ids starting with "_", so
// a real device cannot collide with this synthetic. It is never
// registered in the store; consumers that group canonical events by
// device id should treat it as a virtual device.
const HouseDeviceID = "_house"

// HouseElectricityCapability is the canonical capability name for the
// emitted aggregate. Attribute names match the field names on
// ElectricitySummary.
const HouseElectricityCapability = "house_electricity"

// meterScheme is the scheme name assigned by the Glow/SMETS2 meter
// adapter. Recognising the meter by scheme avoids depending on a
// user-supplied name-hint to classify what is plainly a meter device.
const meterScheme = "meter"

// isMeterDevice reports whether a device should supply the gross
// electricity reading. We accept either explicit configuration
// (ClassEnergyMeter via the user's YAML) or scheme=meter (the adapter
// is the de facto source of meter readings) — but only when the
// device is carrying a PowerW reading, which excludes Glow TH sensors
// that ride on the same scheme.
func isMeterDevice(d model.Device) bool {
	if d.Latest.PowerW == nil {
		return false
	}
	if d.Class == device.ClassEnergyMeter {
		return true
	}
	if d.Identity.Scheme == meterScheme {
		return true
	}
	return false
}

// isPowerMonitored reports whether a device's class contributes its
// PowerW to the monitored sum. Passive sensors and binary-state devices
// don't carry power readings; the meter itself supplies gross, not
// monitored, and is excluded.
func isPowerMonitored(class string) bool {
	switch class {
	case device.ClassShortBurst, device.ClassCyclePower,
		device.ClassContinuous, device.ClassMedia:
		return true
	}
	return false
}

// ElectricityAggregate is the raw output of the aggregator before
// kWh integration. The engine layer combines it with three integrators
// to produce the full ElectricitySummary.
type ElectricityAggregate struct {
	GrossW       float64
	MonitoredW   float64
	UnmonitoredW float64
	GrossSeen    bool
	StaleIDs     []string
}

// isFreshDevice decides whether a power-reporting device's Latest is
// recent enough to participate in the monitored sum. Offline devices
// are always excluded; OfflinePending and Online defer to the
// class-based staleness threshold (so a brief offline hint within the
// debounce doesn't blank the metric).
func isFreshDevice(d model.Device, now time.Time, stalenessFor func(class string) *int) bool {
	if d.Availability == model.AvailabilityOffline {
		return false
	}
	if d.Latest.LastSeen.IsZero() {
		return false
	}
	threshold := device.StalenessSecondsForClass(d.Class, stalenessFor(d.Class))
	return now.Sub(d.Latest.LastSeen).Seconds() <= threshold
}

// AggregateElectricity walks the device map and produces the whole-house
// electricity aggregate at instant `now`. It does not touch integrators;
// the engine layer does that, so the aggregator can be unit-tested as a
// pure function.
//
// Gross is supplied by the lowest-id device whose class is
// ClassEnergyMeter or whose Identity.Scheme is "meter" (and which
// carries a non-nil PowerW). Selecting by sorted id gives a
// deterministic choice when multiple meter devices are present (rare
// — typically a single-supply home — but a two-meter misconfig
// surfaces as a stable wrong number rather than a flickering one).
// If no such device exists, GrossSeen is false and the engine treats
// the result as "no data yet, do nothing."
func AggregateElectricity(now time.Time, devices map[string]model.Device, stalenessFor func(class string) *int) ElectricityAggregate {
	var agg ElectricityAggregate
	var meterID string
	for _, d := range devices {
		if isMeterDevice(d) {
			if meterID == "" || d.ID < meterID {
				meterID = d.ID
				agg.GrossW = *d.Latest.PowerW
				agg.GrossSeen = true
			}
			continue
		}
		if !isPowerMonitored(d.Class) || d.Latest.PowerW == nil {
			continue
		}
		if !isFreshDevice(d, now, stalenessFor) {
			agg.StaleIDs = append(agg.StaleIDs, d.ID)
			continue
		}
		agg.MonitoredW += *d.Latest.PowerW
	}
	if agg.GrossSeen {
		agg.UnmonitoredW = agg.GrossW - agg.MonitoredW
	}
	sort.Strings(agg.StaleIDs)
	return agg
}
