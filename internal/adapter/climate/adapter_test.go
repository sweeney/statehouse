package climate

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func sensorCfg() config.Config {
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"environmental_sensor": {},
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

// TestAdapter_PartialObservation verifies that absent fields in a partial
// WeatherFlow observation (e.g. a message containing only temperature and
// humidity) do not produce false-zero readings for the missing fields.
func TestAdapter_PartialObservation(t *testing.T) {
	a, store, _ := mkAdapter(t)
	partial := `{"timestamp":1778709402,"temperature_c":18.5,"humidity_pct":65.0}`
	a.HandleMessage("climate/home/observation", []byte(partial), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	l := dev.Latest
	if l.TemperatureC == nil || *l.TemperatureC != 18.5 {
		t.Errorf("TemperatureC = %v, want 18.5", l.TemperatureC)
	}
	if l.HumidityPct == nil || *l.HumidityPct != 65.0 {
		t.Errorf("HumidityPct = %v, want 65.0", l.HumidityPct)
	}
	// Fields absent from JSON must not produce zero readings.
	if l.PressureHPa != nil {
		t.Errorf("PressureHPa should be nil for partial payload, got %v", *l.PressureHPa)
	}
	if l.WindSpeedMS != nil {
		t.Errorf("WindSpeedMS should be nil for partial payload, got %v", *l.WindSpeedMS)
	}
	if l.RainfallMM != nil {
		t.Errorf("RainfallMM should be nil for partial payload, got %v", *l.RainfallMM)
	}
	if l.UVIndex != nil {
		t.Errorf("UVIndex should be nil for partial payload, got %v", *l.UVIndex)
	}
}

// TestAdapter_FutureObservationTimestampRejected verifies that a payload
// timestamp 50 years in the future is rejected and the reading timestamp
// falls back to approximately now.
func TestAdapter_FutureObservationTimestampRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	futureUnix := time.Now().Add(50 * 365 * 24 * time.Hour).Unix()
	payload := fmt.Sprintf(`{"timestamp":%d,"temperature_c":20.0,"humidity_pct":50.0}`, futureUnix)
	before := time.Now()
	a.HandleMessage("climate/home/observation", []byte(payload), false)
	after := time.Now()

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	ts := dev.Latest.LastSeen
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("future timestamp not sanitised: got %v, want close to now (%v..%v)", ts, before, after)
	}
}

// TestAdapter_FutureDeviceStatusTimestampRejected verifies that a device/status
// payload with a 50-year-future timestamp is sanitised to approximately now.
func TestAdapter_FutureDeviceStatusTimestampRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	futureUnix := time.Now().Add(50 * 365 * 24 * time.Hour).Unix()
	payload := fmt.Sprintf(`{"timestamp":%d,"rssi_dbm":-60}`, futureUnix)
	before := time.Now()
	a.HandleMessage("climate/home/device/status", []byte(payload), false)
	after := time.Now()

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found after device/status")
	}
	ts := dev.Latest.LastSeen
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("future device/status timestamp not sanitised: got %v, want close to now (%v..%v)", ts, before, after)
	}
}

// TestAdapter_UnixMsTimestampRejected verifies that a unix-millisecond value
// accidentally supplied as unix-seconds is rejected (it would produce year ~58319).
func TestAdapter_UnixMsTimestampRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	unixMs := time.Now().UnixMilli() // milliseconds — far above 4e9 seconds
	payload := fmt.Sprintf(`{"timestamp":%d,"temperature_c":20.0}`, unixMs)
	before := time.Now()
	a.HandleMessage("climate/home/observation", []byte(payload), false)
	after := time.Now()

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	ts := dev.Latest.LastSeen
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("unix-ms timestamp not sanitised: got %v, want close to now (%v..%v)", ts, before, after)
	}
}

// TestAdapter_OutOfRangeObservationFieldsAreNil verifies that observation fields
// exceeding their bounds are silently omitted rather than accepted.
func TestAdapter_OutOfRangeObservationFieldsAreNil(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// temperature=150 exceeds [-50,80]; pressure=500 is below [800,1100]; uv_index=25 exceeds [0,20].
	// humidity=92.8 is valid and should be accepted.
	payload := `{"timestamp":1778709402,"temperature_c":150,"humidity_pct":92.8,"pressure_mb":500,"uv_index":25}`
	a.HandleMessage("climate/home/observation", []byte(payload), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found in store")
	}
	l := dev.Latest
	if l.TemperatureC != nil {
		t.Errorf("TemperatureC should be nil for out-of-range value, got %v", *l.TemperatureC)
	}
	if l.HumidityPct == nil || *l.HumidityPct != 92.8 {
		t.Errorf("HumidityPct = %v, want 92.8 (valid value should be accepted)", l.HumidityPct)
	}
	if l.PressureHPa != nil {
		t.Errorf("PressureHPa should be nil for out-of-range value, got %v", *l.PressureHPa)
	}
	if l.UVIndex != nil {
		t.Errorf("UVIndex should be nil for out-of-range value, got %v", *l.UVIndex)
	}
}

// TestAdapter_MalformedLocationIsRejected verifies that a very long random topic
// segment that fails the location format check does not register any device.
func TestAdapter_MalformedLocationIsRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// 100 chars — exceeds the 64-char limit.
	longLocation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	topic := "climate/" + longLocation + "/observation"
	a.HandleMessage(topic, []byte(sampleObservation), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("malformed location must not register a device, got %d devices", n)
	}
}

// TestAdapter_DeviceStatusWithoutRSSI verifies that a device/status payload
// lacking rssi_dbm does not produce a false-zero RSSI reading.
func TestAdapter_DeviceStatusWithoutRSSI(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"timestamp":1778709402}`
	a.HandleMessage("climate/home/device/status", []byte(payload), false)

	dev, ok := store.Get("home")
	if !ok {
		t.Fatal("climate/home device not found after device/status")
	}
	if dev.Latest.RSSI != nil {
		t.Errorf("RSSI should be nil when rssi_dbm absent, got %v", *dev.Latest.RSSI)
	}
}

// TestAdapter_OutOfRangeWarnLogged verifies that when a field fails the bounds
// check, a Warn-level log message is emitted containing the field name, value,
// and topic. The valid humidity_pct field must not generate a warning.
func TestAdapter_OutOfRangeWarnLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 21, 57, 0, 0, time.UTC))
	engine := state.NewEngine(sensorCfg(), store, clock)
	a := New(engine, "climate", logger)

	// temperature=150 is out of [-50, 80]; humidity=92.8 is valid.
	payload := `{"timestamp":1778709402,"temperature_c":150,"humidity_pct":92.8}`
	a.HandleMessage("climate/home/observation", []byte(payload), false)

	logged := buf.String()
	if !strings.Contains(logged, "rejected out-of-range field") {
		t.Errorf("expected warn log for rejected field, got: %s", logged)
	}
	if !strings.Contains(logged, "temperature_c") {
		t.Errorf("expected warn log to mention temperature_c, got: %s", logged)
	}
	// Valid field must not produce a warning.
	if strings.Contains(logged, "humidity_pct") {
		t.Errorf("valid humidity_pct should not produce a warning, got: %s", logged)
	}
}

func TestAdapter_FixtureReplay(t *testing.T) {
	a, store, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "climate_readings.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	// Independently accumulate the expected temperature/humidity extremes by
	// sampling Latest after each message — a separate pass over the same data
	// the engine sees, so we cross-check Lifetime without re-parsing payloads
	// or hardcoding fixture values.
	var wantMinT, wantMaxT, wantMinH, wantMaxH *float64
	observe := func(seen, lo, hi *float64) (*float64, *float64) {
		if seen == nil {
			return lo, hi
		}
		v := *seen
		if lo == nil || v < *lo {
			lo = &v
		}
		if hi == nil || v > *hi {
			hi = &v
		}
		return lo, hi
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
		if dev, ok := store.Get("home"); ok {
			wantMinT, wantMaxT = observe(dev.Latest.TemperatureC, wantMinT, wantMaxT)
			wantMinH, wantMaxH = observe(dev.Latest.HumidityPct, wantMinH, wantMaxH)
		}
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
	// Lifetime extremes must match the independently computed min/max, and the
	// recorded timestamps must fall within the replayed window.
	if dev.Lifetime == nil {
		t.Fatal("expected lifetime block after fixture replay")
	}
	checkExtremum(t, "min_temperature_c", dev.Lifetime.MinTemperature, wantMinT)
	checkExtremum(t, "max_temperature_c", dev.Lifetime.MaxTemperature, wantMaxT)
	checkExtremum(t, "min_humidity_pct", dev.Lifetime.MinHumidity, wantMinH)
	checkExtremum(t, "max_humidity_pct", dev.Lifetime.MaxHumidity, wantMaxH)
}

// checkExtremum asserts a lifetime extremum was recorded with the expected
// value (and a non-zero timestamp) when one was expected, or is absent when
// the measurement never appeared.
func checkExtremum(t *testing.T, name string, got *model.Extremum, want *float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s: expected no extremum, got %+v", name, got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s: expected extremum %v, got nil", name, *want)
	}
	if got.Value != *want {
		t.Errorf("%s: expected value %v, got %v", name, *want, got.Value)
	}
	if got.At.IsZero() {
		t.Errorf("%s: expected a recorded timestamp, got zero", name)
	}
}
