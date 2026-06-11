package model

import "time"

// Reading carries the optional fields decoded from a device payload.
// Every value-bearing field is a pointer so callers can distinguish
// "field absent" from "field present with zero value".
type Reading struct {
	Timestamp   time.Time
	SourceTopic string

	// Power / energy (plugs, smart meter, UPS)
	State     *string  // "ON"/"OFF" for binary-state devices
	PowerW    *float64 // instantaneous power watts
	VoltageV  *float64
	CurrentA  *float64
	EnergyKWh *float64 // monotonic import counter kWh

	// Meter period counters (smart meter only). These are authoritative
	// totals the meter resets itself at local midnight / week / month —
	// not service-derived. Nil for any device that is not the meter.
	MeterTodayKWh *float64
	MeterWeekKWh  *float64
	MeterMonthKWh *float64

	// Environment (climate / weather station)
	TemperatureC   *float64
	HumidityPct    *float64
	PressureHPa    *float64 // atmospheric pressure hPa (= mb)
	WindSpeedMS    *float64 // average wind speed m/s
	WindDirDeg     *float64 // wind direction 0–360°
	RainfallMM     *float64 // rainfall mm in last measurement interval
	IlluminanceLux *float64
	UVIndex        *float64

	// UPS
	BatteryRuntimeMins *float64 // remaining battery runtime minutes
	OnBattery          *bool    // true when running on battery
	LowBattery         *bool    // true when battery is critically low

	// Safety alarm (smoke/heat detectors). These are latched binary
	// signals, not activity — a fire alarm is a passive sensor class, so
	// they flow as measurements rather than driving the activity machine.
	// Absence (nil) means "not reported in this payload", NOT "cleared":
	// these devices emit partial per-cluster payloads, so a battery-only
	// message must never be read as smoke=false.
	Smoke  *bool // true when the primary smoke/heat alarm is active
	Tamper *bool // true when the tamper switch is tripped

	// Device health (ancillary — not counted in HasAnyMeasurement)
	LinkQuality *int
	Battery     *float64 // battery charge percent
	RSSI        *int     // signal strength dBm
}

// HasAnyMeasurement reports whether the reading carries at least one
// measurable field. Used to decide whether the reading should
// participate in activity detection.
func (r Reading) HasAnyMeasurement() bool {
	return r.PowerW != nil || r.VoltageV != nil || r.CurrentA != nil ||
		r.EnergyKWh != nil || r.State != nil ||
		r.TemperatureC != nil || r.HumidityPct != nil ||
		r.PressureHPa != nil || r.WindSpeedMS != nil ||
		r.WindDirDeg != nil || r.RainfallMM != nil ||
		r.IlluminanceLux != nil || r.UVIndex != nil ||
		r.BatteryRuntimeMins != nil || r.OnBattery != nil || r.LowBattery != nil ||
		r.Smoke != nil || r.Tamper != nil
}
