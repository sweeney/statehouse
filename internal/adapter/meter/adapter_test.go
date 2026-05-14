package meter

import (
	"fmt"
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
	return New(engine, "energy", nil), store, clock
}

func TestAdapter_Name(t *testing.T) {
	a, _, _ := mkAdapter(t)
	if a.Name() != "meter" {
		t.Errorf("Name() = %q, want meter", a.Name())
	}
}

func TestAdapter_Subscriptions(t *testing.T) {
	a, _, _ := mkAdapter(t)
	subs := a.Subscriptions()
	want := []string{
		"energy/+/SENSOR/electricitymeter",
		"energy/+/SENSOR/glowsensorth1/+",
	}
	if len(subs) != len(want) {
		t.Fatalf("Subscriptions() = %v, want %v", subs, want)
	}
	for i, w := range want {
		if subs[i] != w {
			t.Errorf("subs[%d] = %q, want %q", i, subs[i], w)
		}
	}
}

const sampleMeter = `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","energy":{"export":{"cumulative":0.000,"units":"kWh"},"import":{"cumulative":6252.217,"day":32.715,"week":90.226,"month":300.821,"units":"kWh","mpan":"not available","supplier":"Example Energy","price":{"unitrate":0.15000,"standingcharge":0.25000}}},"power":{"value":1.011,"units":"kW"}}}`

func TestAdapter_ParsesMeterPayload(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(sampleMeter), false)

	dev, ok := store.Get("001122AABBCC")
	if !ok {
		t.Fatal("meter device not found in store")
	}
	l := dev.Latest
	if l.EnergyKWh == nil || *l.EnergyKWh != 6252.217 {
		t.Errorf("EnergyKWh = %v, want 6252.217", l.EnergyKWh)
	}
	// power.value=1.011 kW → 1011 W
	if l.PowerW == nil {
		t.Fatal("PowerW is nil")
	}
	if diff := *l.PowerW - 1011.0; diff > 0.001 || diff < -0.001 {
		t.Errorf("PowerW = %v, want ~1011", l.PowerW)
	}
}

const sampleGlowSensor = `{"glowsensorth1":{"00CAFEBABE01":{"timestamp":"2026-05-13T23:03:55Z","temperature":{"value":19.073,"units":"°C"},"humidity":{"value":46,"units":"%"},"battery":{"value":55,"units":"%"},"rssi":{"value":-82,"units":"dBm"},"status":"connected","advname":"GlowSensorTH_TESTDEVICE","customname":""}}}`

func TestAdapter_ParsesGlowSensorPayload(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("energy/001122AABBCC/SENSOR/glowsensorth1/00CAFEBABE01", []byte(sampleGlowSensor), false)

	dev, ok := store.Get("00CAFEBABE01")
	if !ok {
		t.Fatal("glow sensor device not found in store")
	}
	l := dev.Latest
	if l.TemperatureC == nil || *l.TemperatureC != 19.073 {
		t.Errorf("TemperatureC = %v, want 19.073", l.TemperatureC)
	}
	if l.HumidityPct == nil || *l.HumidityPct != 46 {
		t.Errorf("HumidityPct = %v, want 46", l.HumidityPct)
	}
	if l.BatteryPct == nil || *l.BatteryPct != 55 {
		t.Errorf("BatteryPct = %v, want 55", l.BatteryPct)
	}
	if l.RSSI == nil || *l.RSSI != -82 {
		t.Errorf("RSSI = %v, want -82", l.RSSI)
	}
}

func TestAdapter_IgnoresUnrelatedTopics(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// boiler sensor topics — lowercase SENSOR, different shape
	a.HandleMessage("energy/boiler/sensor/events", []byte(`{}`), false)
	// STATE topic — not electricitymeter
	a.HandleMessage("energy/001122AABBCC/STATE", []byte(`{}`), false)
	// invalid JSON for electricitymeter
	a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(`not json`), false)
	// invalid JSON for glow sensor
	a.HandleMessage("energy/001122AABBCC/SENSOR/glowsensorth1/00CAFEBABE01", []byte(`not json`), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("expected 0 devices, got %d", n)
	}
}

func TestAdapter_SerialExtractedFromTopic(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("energy/AABBCCDDEEFF/SENSOR/electricitymeter", []byte(sampleMeter), false)
	_, ok := store.Get("AABBCCDDEEFF")
	if !ok {
		t.Error("serial should be extracted from topic, not payload")
	}
}

// TestAdapter_GlowSensorPartialPayload verifies that absent "battery" and
// "rssi" keys in a glow sensor entry do not produce false-zero readings.
func TestAdapter_GlowSensorPartialPayload(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// Payload has temperature and humidity but no battery or rssi fields.
	partial := `{"glowsensorth1":{"00CAFEBABE01":{"timestamp":"2026-05-13T23:03:55Z","temperature":{"value":19.073,"units":"°C"},"humidity":{"value":46,"units":"%"}}}}`
	a.HandleMessage("energy/001122AABBCC/SENSOR/glowsensorth1/00CAFEBABE01", []byte(partial), false)

	dev, ok := store.Get("00CAFEBABE01")
	if !ok {
		t.Fatal("glow sensor device not found in store")
	}
	l := dev.Latest
	if l.TemperatureC == nil || *l.TemperatureC != 19.073 {
		t.Errorf("TemperatureC = %v, want 19.073", l.TemperatureC)
	}
	if l.HumidityPct == nil || *l.HumidityPct != 46 {
		t.Errorf("HumidityPct = %v, want 46", l.HumidityPct)
	}
	// Absent fields must not produce zero readings.
	if l.BatteryPct != nil {
		t.Errorf("BatteryPct should be nil when battery is absent, got %v", *l.BatteryPct)
	}
	if l.RSSI != nil {
		t.Errorf("RSSI should be nil when rssi is absent, got %v", *l.RSSI)
	}
}

// TestAdapter_FutureMeterTimestampRejected verifies that an electricity meter
// payload with a timestamp 50 years in the future is sanitised to approximately
// now.
func TestAdapter_FutureMeterTimestampRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	future := time.Now().Add(50 * 365 * 24 * time.Hour).Format(time.RFC3339)
	payload := fmt.Sprintf(`{"electricitymeter":{"timestamp":%q,"energy":{"import":{"cumulative":100.0}},"power":{"value":1.0}}}`, future)
	before := time.Now()
	a.HandleMessage("energy/AABBCCDDEEFF/SENSOR/electricitymeter", []byte(payload), false)
	after := time.Now()

	dev, ok := store.Get("AABBCCDDEEFF")
	if !ok {
		t.Fatal("meter device not found in store")
	}
	ts := dev.Latest.LastSeen
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("future meter timestamp not sanitised: got %v, want close to now (%v..%v)", ts, before, after)
	}
}

// TestAdapter_FutureGlowSensorTimestampRejected verifies that a glow sensor
// payload with a timestamp 50 years in the future is sanitised to approximately
// now.
func TestAdapter_FutureGlowSensorTimestampRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	future := time.Now().Add(50 * 365 * 24 * time.Hour).Format(time.RFC3339)
	payload := fmt.Sprintf(`{"glowsensorth1":{"00CAFEBABE01":{"timestamp":%q,"temperature":{"value":20.0},"humidity":{"value":50.0}}}}`, future)
	before := time.Now()
	a.HandleMessage("energy/001122AABBCC/SENSOR/glowsensorth1/00CAFEBABE01", []byte(payload), false)
	after := time.Now()

	dev, ok := store.Get("00CAFEBABE01")
	if !ok {
		t.Fatal("glow sensor device not found in store")
	}
	ts := dev.Latest.LastSeen
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("future glow sensor timestamp not sanitised: got %v, want close to now (%v..%v)", ts, before, after)
	}
}

// TestAdapter_OutOfRangePowerIsNil verifies that an extreme power value (1e308 kW)
// is rejected by the bounds check and PowerW is left nil.
func TestAdapter_OutOfRangePowerIsNil(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","energy":{"import":{"cumulative":100.0}},"power":{"value":1e308}}}`
	a.HandleMessage("energy/AABBCCDDEEFF/SENSOR/electricitymeter", []byte(payload), false)

	dev, ok := store.Get("AABBCCDDEEFF")
	if !ok {
		t.Fatal("meter device not found in store")
	}
	if dev.Latest.PowerW != nil {
		t.Errorf("PowerW should be nil for out-of-range value, got %v", *dev.Latest.PowerW)
	}
}

// TestAdapter_OutOfRangeGlowSensorFieldsAreNil verifies that out-of-range glow
// sensor fields are silently omitted rather than accepted.
func TestAdapter_OutOfRangeGlowSensorFieldsAreNil(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// temperature=200 exceeds [-50,80], humidity=-5 is below [0,100].
	payload := `{"glowsensorth1":{"040D00000000":{"timestamp":"2026-05-13T23:03:55Z","temperature":{"value":200},"humidity":{"value":-5}}}}`
	a.HandleMessage("energy/001122AABBCC/SENSOR/glowsensorth1/040D00000000", []byte(payload), false)

	dev, ok := store.Get("040D00000000")
	if !ok {
		t.Fatal("glow sensor device not found in store")
	}
	if dev.Latest.TemperatureC != nil {
		t.Errorf("TemperatureC should be nil for out-of-range value, got %v", *dev.Latest.TemperatureC)
	}
	if dev.Latest.HumidityPct != nil {
		t.Errorf("HumidityPct should be nil for out-of-range value, got %v", *dev.Latest.HumidityPct)
	}
}

func TestAdapter_FixtureReplay(t *testing.T) {
	a, store, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "meter_readings.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}
	dev, ok := store.Get("001122AABBCC")
	if !ok {
		t.Fatalf("001122AABBCC not found after fixture replay; devices: %v", store.Devices())
	}
	if dev.Latest.EnergyKWh == nil {
		t.Error("expected EnergyKWh to be set after fixture replay")
	}
	if dev.Latest.PowerW == nil {
		t.Error("expected PowerW to be set after fixture replay")
	}
}
