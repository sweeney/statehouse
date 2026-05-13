package climate

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func sensorCfg() config.Config {
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"sensor_device": {},
	}
	return cfg
}

func mkAdapter(t *testing.T) (*Adapter, *state.Store, *testutil.FakeClock) {
	t.Helper()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 21, 57, 0, 0, time.UTC))
	engine := state.NewEngine(sensorCfg(), store, clock)
	return New(engine, "climate", nil), store, clock
}

func TestAdapter_Name(t *testing.T) {
	a, _, _ := mkAdapter(t)
	if a.Name() != "climate" {
		t.Errorf("Name() = %q, want climate", a.Name())
	}
}

func TestAdapter_Subscriptions(t *testing.T) {
	a, _, _ := mkAdapter(t)
	subs := a.Subscriptions()
	if len(subs) != 1 || subs[0] != "climate/#" {
		t.Errorf("Subscriptions() = %v, want [climate/#]", subs)
	}
}

const sampleObservation = `{"timestamp":1778709402,"wind_lull_ms":0,"wind_avg_ms":1.2,"wind_gust_ms":2.5,"wind_direction_deg":270,"pressure_mb":998,"temperature_c":7.69,"humidity_pct":92.8,"illuminance_lux":1,"uv_index":0,"rain_1min_mm":0.2}`

func TestAdapter_ParsesObservation(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("climate/home/observation", []byte(sampleObservation), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	l := dev.Latest
	if l.TemperatureC == nil || *l.TemperatureC != 7.69 {
		t.Errorf("TemperatureC = %v, want 7.69", l.TemperatureC)
	}
	if l.HumidityPct == nil || *l.HumidityPct != 92.8 {
		t.Errorf("HumidityPct = %v, want 92.8", l.HumidityPct)
	}
	if l.PressureHPa == nil || *l.PressureHPa != 998 {
		t.Errorf("PressureHPa = %v, want 998", l.PressureHPa)
	}
	if l.WindSpeedMS == nil || *l.WindSpeedMS != 1.2 {
		t.Errorf("WindSpeedMS = %v, want 1.2", l.WindSpeedMS)
	}
	if l.WindDirDeg == nil || *l.WindDirDeg != 270 {
		t.Errorf("WindDirDeg = %v, want 270", l.WindDirDeg)
	}
	if l.RainfallMM == nil || *l.RainfallMM != 0.2 {
		t.Errorf("RainfallMM = %v, want 0.2", l.RainfallMM)
	}
	if l.IlluminanceLux == nil || *l.IlluminanceLux != 1 {
		t.Errorf("IlluminanceLux = %v, want 1", l.IlluminanceLux)
	}
	if l.UVIndex == nil || *l.UVIndex != 0 {
		t.Errorf("UVIndex = %v, want 0", l.UVIndex)
	}
}

const sampleDeviceStatus = `{"timestamp":1778709402,"rssi_dbm":-57,"battery_v":2.642,"sensor_ok":true}`

func TestAdapter_ParsesDeviceStatus(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("climate/home/device/status", []byte(sampleDeviceStatus), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found after device/status")
	}
	if dev.Latest.RSSI == nil || *dev.Latest.RSSI != -57 {
		t.Errorf("RSSI = %v, want -57", dev.Latest.RSSI)
	}
}

func TestAdapter_IgnoresWindRapidAndHubStatus(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("climate/home/wind/rapid", []byte(`{"speed_ms":0,"direction_deg":0}`), false)
	a.HandleMessage("climate/home/status", []byte(`{"rssi_dbm":-72}`), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("wind/rapid and status must not create devices, got %d", n)
	}
}

func TestAdapter_IgnoresInvalidPayload(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("climate/home/observation", []byte(`not json`), false)
	a.HandleMessage("climate/home/observation", []byte(``), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("invalid payloads must not create devices, got %d", n)
	}
}

func TestAdapter_MultipleLocations(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("climate/home/observation", []byte(sampleObservation), false)
	a.HandleMessage("climate/garden/observation", []byte(sampleObservation), false)
	if n := len(store.Devices()); n != 2 {
		t.Errorf("expected 2 location devices, got %d", n)
	}
}

func TestAdapter_FixtureReplay(t *testing.T) {
	a, store, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "climate_readings.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}
	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home not found after fixture replay")
	}
	if dev.Latest.TemperatureC == nil {
		t.Error("expected TemperatureC to be set after fixture replay")
	}
	if dev.Latest.PressureHPa == nil {
		t.Error("expected PressureHPa to be set after fixture replay")
	}
}
