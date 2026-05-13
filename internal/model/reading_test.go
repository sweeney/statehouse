package model

import "testing"

func ptr[T any](v T) *T { return &v }

func TestHasAnyMeasurement_EmptyReading(t *testing.T) {
	if (Reading{}).HasAnyMeasurement() {
		t.Error("empty Reading must not report any measurement")
	}
}

func TestHasAnyMeasurement_AncillaryFieldsOnly(t *testing.T) {
	// LinkQuality, Battery, RSSI are health metadata, not measurements.
	r := Reading{
		LinkQuality: ptr(80),
		Battery:     ptr(95.0),
		RSSI:        ptr(-72),
	}
	if r.HasAnyMeasurement() {
		t.Error("ancillary-only Reading must not report any measurement")
	}
}

var measurementCases = []struct {
	name string
	r    Reading
}{
	{"State", Reading{State: ptr("ON")}},
	{"PowerW", Reading{PowerW: ptr(100.0)}},
	{"VoltageV", Reading{VoltageV: ptr(230.0)}},
	{"CurrentA", Reading{CurrentA: ptr(0.5)}},
	{"EnergyKWh", Reading{EnergyKWh: ptr(1.5)}},
	{"TemperatureC", Reading{TemperatureC: ptr(20.0)}},
	{"HumidityPct", Reading{HumidityPct: ptr(60.0)}},
	{"PressureHPa", Reading{PressureHPa: ptr(1013.0)}},
	{"WindSpeedMS", Reading{WindSpeedMS: ptr(3.5)}},
	{"WindDirDeg", Reading{WindDirDeg: ptr(180.0)}},
	{"RainfallMM", Reading{RainfallMM: ptr(0.2)}},
	{"IlluminanceLux", Reading{IlluminanceLux: ptr(500.0)}},
	{"UVIndex", Reading{UVIndex: ptr(2.0)}},
	{"BatteryRuntimeMins", Reading{BatteryRuntimeMins: ptr(74.5)}},
	{"OnBattery", Reading{OnBattery: ptr(false)}},
}

func TestHasAnyMeasurement_EachMeasurementField(t *testing.T) {
	for _, tc := range measurementCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !tc.r.HasAnyMeasurement() {
				t.Errorf("Reading with %s set must report HasAnyMeasurement", tc.name)
			}
		})
	}
}
