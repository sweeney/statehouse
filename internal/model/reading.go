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

	// Environment (climate / weather station)
	TemperatureC  *float64
	HumidityPct   *float64
	PressureHPa   *float64 // atmospheric pressure hPa (= mb)
	WindSpeedMS   *float64 // average wind speed m/s
	WindDirDeg    *float64 // wind direction 0–360°
	RainfallMM    *float64 // rainfall mm in last measurement interval
	IlluminanceLux *float64
	UVIndex       *float64

	// UPS
	BatteryRuntimeMins *float64 // remaining battery runtime minutes
	OnBattery          *bool    // true when running on battery

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
		r.BatteryRuntimeMins != nil || r.OnBattery != nil
}
