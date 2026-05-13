package ups

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
	return New(engine, "ups", nil), store, clock
}

func TestAdapter_Name(t *testing.T) {
	a, _, _ := mkAdapter(t)
	if a.Name() != "ups" {
		t.Errorf("Name() = %q, want ups", a.Name())
	}
}

func TestAdapter_Subscriptions(t *testing.T) {
	a, _, _ := mkAdapter(t)
	subs := a.Subscriptions()
	if len(subs) != 1 || subs[0] != "ups/+/state" {
		t.Errorf("Subscriptions() = %v, want [ups/+/state]", subs)
	}
}

const sampleState = `{"timestamp":"2026-05-13T21:57:06Z","ups_name":"cyberpower","variables":{"battery.charge":"100","input.voltage":"244"},"computed":{"load_watts":72,"battery_runtime_mins":74.5,"on_battery":false,"low_battery":false}}`

func TestAdapter_ParsesStatePayload(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("ups/cyberpower/state", []byte(sampleState), false)

	dev, ok := store.Get("cyberpower")
	if !ok {
		t.Fatal("device cyberpower not found in store")
	}
	l := dev.Latest
	if l.PowerW == nil || *l.PowerW != 72 {
		t.Errorf("PowerW = %v, want 72", l.PowerW)
	}
	if l.BatteryRuntimeMins == nil || *l.BatteryRuntimeMins != 74.5 {
		t.Errorf("BatteryRuntimeMins = %v, want 74.5", l.BatteryRuntimeMins)
	}
	if l.OnBattery == nil || *l.OnBattery != false {
		t.Errorf("OnBattery = %v, want false", l.OnBattery)
	}
	if l.BatteryPct == nil || *l.BatteryPct != 100 {
		t.Errorf("BatteryPct = %v, want 100", l.BatteryPct)
	}
	if l.VoltageV == nil || *l.VoltageV != 244 {
		t.Errorf("VoltageV = %v, want 244", l.VoltageV)
	}
}

func TestAdapter_IgnoresUnrelatedTopics(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("ups/cyberpower/battery/charge", []byte(`100`), false)
	a.HandleMessage("zigbee2mqtt/something", []byte(`{}`), false)
	a.HandleMessage("ups/cyberpower/state", []byte(`not json`), false)
	if n := len(store.Devices()); n != 0 {
		t.Errorf("expected 0 devices after invalid input, got %d", n)
	}
}

func TestAdapter_MultipleUPS(t *testing.T) {
	a, store, _ := mkAdapter(t)
	a.HandleMessage("ups/cyberpower/state", []byte(sampleState), false)
	payload2 := `{"ups_name":"apc","variables":{},"computed":{"load_watts":50,"battery_runtime_mins":30,"on_battery":true}}`
	a.HandleMessage("ups/apc/state", []byte(payload2), false)
	if n := len(store.Devices()); n != 2 {
		t.Errorf("expected 2 UPS devices, got %d", n)
	}
}

// TestAdapter_PayloadCannotOverrideTopicName verifies that a payload-supplied
// ups_name cannot override the device identity derived from the MQTT topic.
// This guards against the spoofing / identity-confusion attack described in
// issue #5: an attacker publishing to ups/attacker/state with ups_name="victim"
// must not be able to inject readings into the "victim" device.
func TestAdapter_PayloadCannotOverrideTopicName(t *testing.T) {
	a, store, _ := mkAdapter(t)

	// Payload claims to be "victim", but the topic says "attacker".
	payload := `{"ups_name":"victim","variables":{},"computed":{"load_watts":99,"battery_runtime_mins":1,"on_battery":true}}`
	a.HandleMessage("ups/attacker/state", []byte(payload), false)

	// "attacker" must be in the store — the topic-derived name is the truth.
	if _, ok := store.Get("attacker"); !ok {
		t.Error("expected device 'attacker' in store (topic-derived name)")
	}

	// "victim" must NOT be in the store — the payload name must be ignored.
	if _, ok := store.Get("victim"); ok {
		t.Error("device 'victim' found in store: payload ups_name must not override topic identity")
	}
}

// TestAdapter_NoComputedBlock verifies that when the "computed" block is
// entirely absent from a UPS payload, PowerW and BatteryRuntimeMins are nil
// rather than false-zero readings.
func TestAdapter_NoComputedBlock(t *testing.T) {
	a, store, _ := mkAdapter(t)
	payload := `{"timestamp":"2026-05-13T21:57:06Z","ups_name":"cyberpower","variables":{"battery.charge":"80","input.voltage":"240"}}`
	a.HandleMessage("ups/cyberpower/state", []byte(payload), false)

	dev, ok := store.Get("cyberpower")
	if !ok {
		t.Fatal("device cyberpower not found in store")
	}
	l := dev.Latest
	// Without a computed block, derived fields must be absent (nil).
	if l.PowerW != nil {
		t.Errorf("PowerW should be nil when computed block is absent, got %v", *l.PowerW)
	}
	if l.BatteryRuntimeMins != nil {
		t.Errorf("BatteryRuntimeMins should be nil when computed block is absent, got %v", *l.BatteryRuntimeMins)
	}
	if l.OnBattery != nil {
		t.Errorf("OnBattery should be nil when computed block is absent, got %v", *l.OnBattery)
	}
	// Variables-derived fields must still be present.
	if l.BatteryPct == nil || *l.BatteryPct != 80 {
		t.Errorf("BatteryPct = %v, want 80", l.BatteryPct)
	}
	if l.VoltageV == nil || *l.VoltageV != 240 {
		t.Errorf("VoltageV = %v, want 240", l.VoltageV)
	}
}

// TestAdapter_FutureTimestampRejected verifies that a UPS state payload with a
// timestamp 50 years in the future is rejected and the reading timestamp falls
// back to approximately now.
func TestAdapter_FutureTimestampRejected(t *testing.T) {
	a, store, _ := mkAdapter(t)
	future := time.Now().Add(50 * 365 * 24 * time.Hour).Format(time.RFC3339)
	payload := fmt.Sprintf(`{"timestamp":%q,"ups_name":"cyberpower","variables":{},"computed":{"load_watts":72,"battery_runtime_mins":74.5,"on_battery":false}}`, future)
	before := time.Now()
	a.HandleMessage("ups/cyberpower/state", []byte(payload), false)
	after := time.Now()

	dev, ok := store.Get("cyberpower")
	if !ok {
		t.Fatal("device cyberpower not found in store")
	}
	ts := dev.Latest.LastSeen
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("future timestamp not sanitised: got %v, want close to now (%v..%v)", ts, before, after)
	}
}

func TestAdapter_FixtureReplay(t *testing.T) {
	a, store, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "ups_readings.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}
	dev, ok := store.Get("cyberpower")
	if !ok {
		t.Fatal("cyberpower not found after fixture replay")
	}
	if dev.Latest.PowerW == nil {
		t.Error("expected PowerW to be set after fixture replay")
	}
	if dev.Latest.BatteryRuntimeMins == nil {
		t.Error("expected BatteryRuntimeMins to be set after fixture replay")
	}
}
