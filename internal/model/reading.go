package model

import "time"

// Reading carries the optional fields decoded from a Zigbee2MQTT device
// payload. Every value-bearing field is a pointer so callers can
// distinguish "field absent" from "field present with zero value".
// A first message from a device may carry only state + linkquality;
// power, voltage and energy can arrive later.
type Reading struct {
	Timestamp   time.Time
	SourceTopic string

	State       *string  // typically "ON"/"OFF" for plugs
	PowerW      *float64 // instantaneous power
	VoltageV    *float64
	CurrentA    *float64
	EnergyKWh   *float64 // monotonic counter reported by the plug
	TemperatureC *float64
	HumidityPct *float64
	LinkQuality *int
	Battery     *float64
}

// HasAnyMeasurement reports whether the reading carries at least one
// measurable field. Useful to decide whether the reading should
// participate in activity detection or energy calculation.
func (r Reading) HasAnyMeasurement() bool {
	return r.PowerW != nil || r.VoltageV != nil || r.CurrentA != nil ||
		r.EnergyKWh != nil || r.TemperatureC != nil || r.HumidityPct != nil ||
		r.State != nil
}
