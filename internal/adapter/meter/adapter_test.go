package meter

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// TestAdapter_FieldTypeDriftDegradesGracefully asserts that if the meter
// firmware changes one field's JSON type (here: a period counter becomes
// a non-numeric string), the rest of the message — cumulative + power —
// still ingests. The adapter must degrade per-field, not drop the whole
// reading.
func TestAdapter_FieldTypeDriftDegradesGracefully(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// day is a non-numeric string; cumulative + power are valid numbers.
	payload := `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","energy":{"import":{"cumulative":6252.217,"day":"n/a","week":90.226,"month":300.821}},"power":{"value":1.011}}}`
	a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(payload), false)

	dev, ok := store.Get("001122AABBCC")
	if !ok {
		t.Fatal("meter device not found: whole message dropped on one bad field")
	}
	if dev.Latest.EnergyKWh == nil || *dev.Latest.EnergyKWh != 6252.217 {
		t.Errorf("EnergyKWh=%v want 6252.217 (cumulative must survive bad day field)", dev.Latest.EnergyKWh)
	}
	if dev.Latest.PowerW == nil {
		t.Error("PowerW nil: power must survive a bad day field")
	}
	e := store.House().Electricity
	if e.TodayKWh != nil {
		t.Errorf("TodayKWh=%v want nil (unparseable), but week/month should still apply", e.TodayKWh)
	}
	if e.WeekKWh == nil || *e.WeekKWh != 90.226 {
		t.Errorf("WeekKWh=%v want 90.226 (other periods unaffected)", e.WeekKWh)
	}
}

// TestAdapter_NumericStringsTolerated asserts the realistic drift where a
// firmware update starts quoting numbers ("8.013") is parsed, not lost.
func TestAdapter_NumericStringsTolerated(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","energy":{"import":{"cumulative":"6252.217","day":"8.013"}},"power":{"value":"1.011"}}}`
	a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(payload), false)

	dev, ok := store.Get("001122AABBCC")
	if !ok {
		t.Fatal("meter device not found")
	}
	if dev.Latest.EnergyKWh == nil || *dev.Latest.EnergyKWh != 6252.217 {
		t.Errorf("EnergyKWh=%v want 6252.217 from quoted number", dev.Latest.EnergyKWh)
	}
	if e := store.House().Electricity; e.TodayKWh == nil || *e.TodayKWh != 8.013 {
		t.Errorf("TodayKWh=%v want 8.013 from quoted number", e.TodayKWh)
	}
}

func sensorCfg() config.Config {
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"energy_meter": {},
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

func TestAdapter_DrivesHouseElectricity(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(sampleMeter), false)

	e := store.House().Electricity
	if e.ComputedAt.IsZero() {
		t.Fatalf("ComputedAt zero after meter ingest")
	}
	if diff := e.GrossW - 1011.0; diff > 0.001 || diff < -0.001 {
		t.Errorf("Electricity.GrossW=%v want ~1011", e.GrossW)
	}
	if e.MonitoredW != 0 {
		t.Errorf("Electricity.MonitoredW=%v want 0 (no plugs)", e.MonitoredW)
	}
	if diff := e.UnmonitoredW - 1011.0; diff > 0.001 || diff < -0.001 {
		t.Errorf("Electricity.UnmonitoredW=%v want ~1011", e.UnmonitoredW)
	}
	// Authoritative meter period totals from import.day/week/month.
	if e.TodayKWh == nil || *e.TodayKWh != 32.715 {
		t.Errorf("Electricity.TodayKWh=%v want 32.715", e.TodayKWh)
	}
	if e.WeekKWh == nil || *e.WeekKWh != 90.226 {
		t.Errorf("Electricity.WeekKWh=%v want 90.226", e.WeekKWh)
	}
	if e.MonthKWh == nil || *e.MonthKWh != 300.821 {
		t.Errorf("Electricity.MonthKWh=%v want 300.821", e.MonthKWh)
	}
}

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

// TestAdapter_MalformedSerialIsRejected verifies that a very long random topic
// segment that fails the serial format check does not register any device.
func TestAdapter_MalformedSerialIsRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	// 100 chars of mixed-case letters — not valid hex.
	longSerial := "ZZZZZZZZZZzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	topic := "energy/" + longSerial + "/SENSOR/electricitymeter"
	a.HandleMessage(topic, []byte(sampleMeter), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("malformed serial must not register a device, got %d devices", n)
	}

	// Same check for glow sensor topic.
	glowTopic := "energy/001122AABBCC/SENSOR/glowsensorth1/" + longSerial
	a.HandleMessage(glowTopic, []byte(sampleGlowSensor), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("malformed glow sensor serial must not register a device, got %d devices", n)
	}
}

func TestAdapter_MeterPartialPayloadNoPower(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","energy":{"import":{"cumulative":6252.217}}}}`
	a.HandleMessage("energy/AABBCCDDEEFF/SENSOR/electricitymeter", []byte(payload), false)
	dev, _ := store.Get("AABBCCDDEEFF")
	if dev.Latest.PowerW != nil {
		t.Errorf("PowerW should be nil when power.value absent, got %v", *dev.Latest.PowerW)
	}
	if dev.Latest.EnergyKWh == nil || *dev.Latest.EnergyKWh != 6252.217 {
		t.Errorf("EnergyKWh should be 6252.217, got %v", dev.Latest.EnergyKWh)
	}
}

func TestAdapter_MeterPartialPayloadNoCumulative(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","power":{"value":1.011}}}`
	a.HandleMessage("energy/AABBCCDDEEFF/SENSOR/electricitymeter", []byte(payload), false)
	dev, _ := store.Get("AABBCCDDEEFF")
	if dev.Latest.EnergyKWh != nil {
		t.Errorf("EnergyKWh should be nil when cumulative absent, got %v", *dev.Latest.EnergyKWh)
	}
}

// TestAdapter_OutOfRangeMeterWarnLogged verifies that when the electricity meter
// power value fails the bounds check, a Warn-level log message is emitted.
// The valid energy_kwh field must not generate a warning.
func TestAdapter_OutOfRangeMeterWarnLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 21, 57, 0, 0, time.UTC))
	engine := state.NewEngine(sensorCfg(), store, clock)
	a := New(engine, "energy", logger)

	// power.value=1e308 kW → overflows bounds; cumulative=100.0 is valid.
	payload := `{"electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","energy":{"import":{"cumulative":100.0}},"power":{"value":1e308}}}`
	a.HandleMessage("energy/AABBCCDDEEFF/SENSOR/electricitymeter", []byte(payload), false)

	logged := buf.String()
	if !strings.Contains(logged, "rejected out-of-range field") {
		t.Errorf("expected warn log for rejected field, got: %s", logged)
	}
	if !strings.Contains(logged, "power_w") {
		t.Errorf("expected warn log to mention power_w, got: %s", logged)
	}
	// Valid cumulative field must not produce a warning.
	if strings.Contains(logged, "energy_kwh") {
		t.Errorf("valid energy_kwh should not produce a warning, got: %s", logged)
	}
}

// TestAdapter_OutOfRangeGlowSensorWarnLogged verifies that when a glow sensor
// field fails the bounds check, a Warn-level log message is emitted.
func TestAdapter_OutOfRangeGlowSensorWarnLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 21, 57, 0, 0, time.UTC))
	engine := state.NewEngine(sensorCfg(), store, clock)
	a := New(engine, "energy", logger)

	// temperature=200 is out of [-50, 80]; humidity=46 is valid.
	payload := `{"glowsensorth1":{"040D00000000":{"timestamp":"2026-05-13T23:03:55Z","temperature":{"value":200},"humidity":{"value":46}}}}`
	a.HandleMessage("energy/001122AABBCC/SENSOR/glowsensorth1/040D00000000", []byte(payload), false)

	logged := buf.String()
	if !strings.Contains(logged, "rejected out-of-range field") {
		t.Errorf("expected warn log for rejected field, got: %s", logged)
	}
	if !strings.Contains(logged, "temperature_c") {
		t.Errorf("expected warn log to mention temperature_c, got: %s", logged)
	}
	// Valid humidity field must not produce a warning.
	if strings.Contains(logged, "humidity_pct") {
		t.Errorf("valid humidity_pct should not produce a warning, got: %s", logged)
	}
}

// TestAdapter_RealFixtureReplay replays a redacted capture of real meter
// traffic (serial/supplier/price scrubbed) and asserts the period counters
// and cumulative parse and progress as captured — including a duplicate
// timestamp the meter genuinely re-published. Assertions are on parsed
// field values, so they do not depend on wall-clock timing.
func TestAdapter_RealFixtureReplay(t *testing.T) {
	a, store, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "meter_readings_real.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 fixture events, got %d", len(events))
	}
	// today_kwh as captured, in order (note the repeated 8.010 from the
	// duplicate-timestamp message).
	wantToday := []float64{8.007, 8.008, 8.010, 8.010, 8.013}
	for i, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
		got := store.House().Electricity.TodayKWh
		if got == nil || *got != wantToday[i] {
			t.Fatalf("after msg %d: TodayKWh=%v want %v (message dropped or misparsed)", i, got, wantToday[i])
		}
	}

	e := store.House().Electricity
	if e.TodayKWh == nil || *e.TodayKWh != 8.013 {
		t.Errorf("final TodayKWh=%v want 8.013", e.TodayKWh)
	}
	if e.WeekKWh == nil || *e.WeekKWh != 48.837 {
		t.Errorf("final WeekKWh=%v want 48.837", e.WeekKWh)
	}
	if e.MonthKWh == nil || *e.MonthKWh != 252.054 {
		t.Errorf("final MonthKWh=%v want 252.054", e.MonthKWh)
	}
	dev, ok := store.Get("A1B2C3D4E5F6")
	if !ok {
		t.Fatal("meter device not found after real fixture replay")
	}
	if dev.Latest.EnergyKWh == nil || *dev.Latest.EnergyKWh != 6931.574 {
		t.Errorf("final cumulative EnergyKWh=%v want 6931.574", dev.Latest.EnergyKWh)
	}
	// power.value 0.668 kW → 668 W
	if dev.Latest.PowerW == nil || *dev.Latest.PowerW < 667 || *dev.Latest.PowerW > 669 {
		t.Errorf("final PowerW=%v want ~668", dev.Latest.PowerW)
	}
}

// TestAdapter_UnknownFieldsIgnored locks forward-compatibility: a firmware
// update that adds new keys (here a nested "tariff" object and a top-level
// "version") must not disturb parsing of the known fields.
func TestAdapter_UnknownFieldsIgnored(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"version":"2.0","electricitymeter":{"timestamp":"2026-05-13T21:57:19Z","newthing":{"x":1},"energy":{"import":{"cumulative":6252.217,"day":8.013,"tariff":{"name":"flex"}}},"power":{"value":1.011,"units":"kW"}}}`
	a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(payload), false)

	dev, ok := store.Get("001122AABBCC")
	if !ok {
		t.Fatal("meter device not found with unknown extra fields present")
	}
	if dev.Latest.EnergyKWh == nil || *dev.Latest.EnergyKWh != 6252.217 {
		t.Errorf("EnergyKWh=%v want 6252.217", dev.Latest.EnergyKWh)
	}
	if e := store.House().Electricity; e.TodayKWh == nil || *e.TodayKWh != 8.013 {
		t.Errorf("TodayKWh=%v want 8.013", e.TodayKWh)
	}
}

// TestAdapter_MalformedJSONNoPanic asserts a range of broken/unexpected
// payloads are dropped without panicking and without registering bad data.
func TestAdapter_MalformedJSONNoPanic(t *testing.T) {
	cases := []string{
		``,                                     // empty
		`{`,                                    // truncated
		`[1,2,3]`,                              // wrong top-level type
		`"just a string"`,                      // scalar
		`null`,                                 // null doc
		`{"electricitymeter":"not an object"}`, // wrong nested type
		`{"electricitymeter":{"energy":{"import":[1,2]}}}`,             // import is array
		`{"electricitymeter":{"power":{"value":{"nested":true}}}}`,     // value is object
		`{"electricitymeter":{"energy":{"import":{"cumulative":[]}}}}`, // cumulative is array
	}
	for _, c := range cases {
		a, store, _ := mkAdapter(t) // fresh per case
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on payload %q: %v", c, r)
				}
			}()
			a.HandleMessage("energy/001122AABBCC/SENSOR/electricitymeter", []byte(c), false)
		}()
		// No usable electricity data should have been recorded.
		if !store.House().Electricity.ComputedAt.IsZero() {
			t.Errorf("payload %q produced an electricity recompute; want none", c)
		}
	}
}

func TestAdapter_FixtureReplay(t *testing.T) {
	a, store, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "meter_readings.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	// Independently track the peak power seen by sampling Latest after each
	// message, to cross-check Lifetime.MaxPower without hardcoding the fixture's
	// peak value.
	var wantMaxP *float64
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
		if dev, ok := store.Get("001122AABBCC"); ok && dev.Latest.PowerW != nil {
			if v := *dev.Latest.PowerW; wantMaxP == nil || v > *wantMaxP {
				wantMaxP = &v
			}
		}
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
	// Lifetime peak power must match the independently computed maximum.
	if wantMaxP == nil {
		t.Fatal("expected fixture to contain at least one power reading")
	}
	if dev.Lifetime == nil || dev.Lifetime.MaxPower == nil {
		t.Fatalf("expected lifetime max power after fixture replay, got %+v", dev.Lifetime)
	}
	if dev.Lifetime.MaxPower.Value != *wantMaxP {
		t.Errorf("max power: expected %v, got %v", *wantMaxP, dev.Lifetime.MaxPower.Value)
	}
	if dev.Lifetime.MaxPower.At.IsZero() {
		t.Error("max power: expected a recorded timestamp, got zero")
	}
}
