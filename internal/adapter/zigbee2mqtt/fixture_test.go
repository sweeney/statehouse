package zigbee2mqtt

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func fixtureCfg() config.Config {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.Energy.DivergenceWarningPct = 20
	cfg.Availability.OfflineDebounce = 30 * time.Second
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"cycle_power_device": {
			NameHints: []string{"dishwasher"},
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           testutil.PtrF64(5),
				ActiveAboveW:         testutil.PtrF64(20),
				ActiveSustainedFor:   testutil.PtrDur(10 * time.Second),
				InactiveSustainedFor: testutil.PtrDur(5 * time.Minute),
			},
			EnergyStrategy: "counter",
		},
		"short_burst_power_device": {
			NameHints: []string{"kettle"},
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           testutil.PtrF64(5),
				ActiveAboveW:         testutil.PtrF64(50),
				ActiveSustainedFor:   testutil.PtrDur(3 * time.Second),
				InactiveSustainedFor: testutil.PtrDur(10 * time.Second),
			},
			EnergyStrategy: "integration",
		},
	}
	return cfg
}

type capture struct {
	derived []model.DerivedEvent
}

func (c *capture) OnDerivedEvent(ev model.DerivedEvent)    { c.derived = append(c.derived, ev) }
func (c *capture) OnCanonicalEvent(_ model.CanonicalEvent) {}

// replay feeds a JSONL fixture through the Z2M Adapter the same way
// paho would in production. The fake clock is advanced to each
// message's timestamp so debounce/hysteresis fire deterministically.
func replay(t *testing.T, path string, clock *testutil.FakeClock, a *Adapter) {
	t.Helper()
	events, err := testutil.LoadFixture(path)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

func TestFixture_DishwasherCycleProducesCycleEvents(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "zigbee2mqtt", nil)

	replay(t, fixturePath("dishwasher_cycle.jsonl"), clock, a)

	var sawStart, sawFinish bool
	for _, ev := range cap.derived {
		if ev.Type == model.EvtCycleStarted {
			sawStart = true
		}
		if ev.Type == model.EvtCycleFinished {
			sawFinish = true
		}
	}
	if !sawStart || !sawFinish {
		t.Fatalf("expected both cycle_started and cycle_finished, got %v", summary(cap.derived))
	}
	d, ok := store.Get("kitchen_dishwasher")
	if !ok {
		t.Fatalf("expected device 'kitchen_dishwasher' in store")
	}
	if d.Cycle == nil {
		t.Fatalf("expected cycle record")
	}
	if d.Cycle.Energy.ReportedKWhDelta < 1.4 || d.Cycle.Energy.ReportedKWhDelta > 1.6 {
		t.Fatalf("expected counter delta around 1.5, got %v", d.Cycle.Energy.ReportedKWhDelta)
	}
}

func TestFixture_KettleShortBurst(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 7, 30, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "zigbee2mqtt", nil)

	replay(t, fixturePath("kettle_short_burst.jsonl"), clock, a)

	var sawBurst bool
	for _, ev := range cap.derived {
		if ev.Type == model.EvtShortBurstDetected {
			sawBurst = true
		}
	}
	if !sawBurst {
		t.Fatalf("expected short_burst_detected, got %v", summary(cap.derived))
	}
}

func TestFixture_BridgeRestartFlickerDoesNotProduceOffline(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "zigbee2mqtt", nil)

	replay(t, fixturePath("bridge_restart_flicker.jsonl"), clock, a)

	for _, ev := range cap.derived {
		if ev.Type == model.EvtDeviceAvailabilityChanged {
			if s, _ := ev.Evidence["availability"].(string); s == string(model.AvailabilityOffline) {
				t.Fatalf("must not emit confirmed offline for a flicker; got %v", summary(cap.derived))
			}
		}
	}
	d, _ := store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOnline {
		t.Fatalf("expected online after flicker recovery, got %q", d.Availability)
	}
}

func TestFixture_RenameKeepsState(t *testing.T) {
	cfg := fixtureCfg()
	cfg.DeviceClasses["short_burst_power_device"] = config.DeviceClassConfig{
		NameHints: []string{"plug"},
		DefaultThresholds: config.Thresholds{
			IdleBelowW:   testutil.PtrF64(5),
			ActiveAboveW: testutil.PtrF64(50),
		},
		EnergyStrategy: "integration",
	}
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 14, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "zigbee2mqtt", nil)

	replay(t, fixturePath("rename_friendlyname.jsonl"), clock, a)

	devs := store.Devices()
	if len(devs) != 1 {
		t.Fatalf("expected exactly one device after rename, got %d: %v", len(devs), devs)
	}
	for _, d := range devs {
		if d.Identity.Scheme != SchemeName {
			t.Errorf("expected scheme=%q, got %q", SchemeName, d.Identity.Scheme)
		}
		if d.Identity.Display != "new_plug" {
			t.Errorf("expected display 'new_plug' after rename, got %q", d.Identity.Display)
		}
		if d.Identity.Primary != "0x00158d0000000099" {
			t.Errorf("expected stable Primary IEEE, got %q", d.Identity.Primary)
		}
	}
}

// TestAdapter_RejectLongRandomTopicCreatesNoDevice verifies that a topic
// with a random 100-character friendly-name segment does not cause the
// engine to create a device. This closes the DoS vector reported in
// issue #33 where an attacker could flood the engine with crafted topics.
func TestAdapter_RejectLongRandomTopicCreatesNoDevice(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "zigbee2mqtt", nil)

	// 100-character random-looking string — well above the 64-char limit.
	longName := "aB3xQrKzPmLvNwOuTsYeHfGdCjIiJlRnVbUqWoAkEhSyXtDpFcMgZaBcDeFgHiJkLmNoPqRsTuVwXyZ012345"
	topic := "zigbee2mqtt/" + longName
	payload := []byte(`{"power":100}`)
	a.HandleMessage(topic, payload, false)

	devs := store.Devices()
	if len(devs) != 0 {
		t.Fatalf("expected no devices created for oversized random friendly name, got %d: %v", len(devs), devs)
	}
}

// TestAdapter_AcceptValidFriendlyNameCreatesDevice verifies that a valid
// friendly name (within the allow-list) causes the engine to create a
// device as expected.
func TestAdapter_AcceptValidFriendlyNameCreatesDevice(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "zigbee2mqtt", nil)

	topic := "zigbee2mqtt/kitchen_kettle"
	payload := []byte(`{"power":2000}`)
	a.HandleMessage(topic, payload, false)

	devs := store.Devices()
	if len(devs) != 1 {
		t.Fatalf("expected 1 device created for valid friendly name, got %d", len(devs))
	}
}

// TestAdapter_PhantomActivityResetOnIEEEMerge verifies that a
// display-only phantom device's Activity is reset to ActivityUnknown
// when the real IEEE address is learned via bridge/devices. This
// prevents pre-discovery state injection (from crafted topics) from
// persisting after the legitimate device is seen.
func TestAdapter_PhantomActivityResetOnIEEEMerge(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "zigbee2mqtt", nil)

	// Step 1: a device payload arrives before bridge/devices — the adapter
	// falls back to Primary=Display="kitchen_dishwasher".
	a.HandleMessage("zigbee2mqtt/kitchen_dishwasher", []byte(`{"power":2000}`), false)

	// The device should exist with a display-only phantom identity.
	devs := store.Devices()
	if len(devs) != 1 {
		t.Fatalf("expected phantom device after payload, got %d devices", len(devs))
	}

	// Step 2: bridge/devices arrives with the real IEEE address.
	bridgePayload := []byte(`[{"ieee_address":"0x00158d0000000001","friendly_name":"kitchen_dishwasher","type":"EndDevice"}]`)
	a.HandleMessage("zigbee2mqtt/bridge/devices", bridgePayload, true)

	// The phantom record should now have been upgraded and its Activity
	// reset to ActivityUnknown — no pre-discovery state leaks through.
	var found model.Device
	var foundID string
	for id, d := range store.Devices() {
		found = d
		foundID = id
		_ = foundID
	}
	if found.Identity.Primary != "0x00158d0000000001" {
		t.Errorf("expected IEEE address after bridge/devices merge, got %q", found.Identity.Primary)
	}
	if found.Activity.State != model.ActivityUnknown {
		t.Errorf("expected Activity reset to unknown after phantom merge, got %q", found.Activity.State)
	}
	if found.Cycle != nil {
		t.Errorf("expected Cycle cleared after phantom merge, got %+v", found.Cycle)
	}
}

// TestAdapter_BridgeDevicesEvictsRemovedFriendlyNames verifies the
// unbounded-growth fix from issue #49: when a friendly_name is renamed
// or a device is unpaired, the next bridge/devices snapshot omits it
// and the adapter's ieeeByFN cache must drop the absent entry.
func TestAdapter_BridgeDevicesEvictsRemovedFriendlyNames(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "zigbee2mqtt", nil)

	first := []byte(`[
		{"ieee_address":"0x00158d0000000001","friendly_name":"old_plug","type":"EndDevice"},
		{"ieee_address":"0x00158d0000000002","friendly_name":"stable_kettle","type":"EndDevice"}
	]`)
	a.HandleMessage("zigbee2mqtt/bridge/devices", first, true)

	a.mu.RLock()
	_, hadOld := a.ieeeByFN["old_plug"]
	a.mu.RUnlock()
	if !hadOld {
		t.Fatalf("expected old_plug to be cached after first snapshot")
	}

	// Second snapshot: old_plug has been renamed to new_plug (and a
	// device has been unpaired entirely — represented here by simply
	// omitting it). The cache must reflect the new snapshot exactly.
	second := []byte(`[
		{"ieee_address":"0x00158d0000000001","friendly_name":"new_plug","type":"EndDevice"},
		{"ieee_address":"0x00158d0000000002","friendly_name":"stable_kettle","type":"EndDevice"}
	]`)
	a.HandleMessage("zigbee2mqtt/bridge/devices", second, true)

	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, stillThere := a.ieeeByFN["old_plug"]; stillThere {
		t.Fatalf("expected old_plug evicted from cache after rename snapshot, got %v", a.ieeeByFN)
	}
	if got := a.ieeeByFN["new_plug"]; got != "0x00158d0000000001" {
		t.Errorf("expected new_plug -> ieee, got %q", got)
	}
	if got := a.ieeeByFN["stable_kettle"]; got != "0x00158d0000000002" {
		t.Errorf("expected stable_kettle retained, got %q", got)
	}
	if len(a.ieeeByFN) != 2 {
		t.Errorf("expected exactly 2 cache entries after rebuild, got %d: %v", len(a.ieeeByFN), a.ieeeByFN)
	}
}

// TestAdapter_TwoPhantomsMergeToOneDeviceOnBridgeDevices reproduces the
// "islaav duplicate" seen in production. The problematic message ordering:
//
//  1. During Z2M interview the device publishes under the IEEE address as
//     its topic (friendly_name == ieee before the user renames it).
//  2. An availability (or payload) for the user-given friendly name arrives
//     before bridge/devices updates the adapter's ieee→name cache, creating
//     a second phantom keyed by the friendly name.
//  3. bridge/devices arrives with the canonical mapping — must collapse both
//     phantoms into exactly one device with the correct identity.
func TestAdapter_TwoPhantomsMergeToOneDeviceOnBridgeDevices(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "zigbee2mqtt", nil)

	const ieee = "0x20a716fffea4634f"
	const fn = "islaav"

	// Step 1: payload arrives under the IEEE address as topic (Z2M
	// interview in progress — friendly name not yet assigned by user).
	a.HandleMessage("zigbee2mqtt/"+ieee, []byte(`{"power":0}`), false)

	if got := len(store.Devices()); got != 1 {
		t.Fatalf("after interview payload: expected 1 device, got %d", got)
	}

	// Step 2: availability for the user-given friendly name arrives before
	// bridge/devices has mapped ieee→fn. The adapter has no cache entry
	// yet so falls back to Primary=Display=fn, creating a second phantom.
	a.HandleMessage("zigbee2mqtt/"+fn+"/availability", []byte(`{"state":"online"}`), false)

	if got := len(store.Devices()); got != 2 {
		t.Fatalf("after availability race: expected 2 phantom devices, got %d", got)
	}

	// Step 3: bridge/devices arrives with the canonical ieee→fn mapping.
	// Both phantoms must collapse into exactly one device.
	bridgePayload := []byte(`[{"ieee_address":"` + ieee + `","friendly_name":"` + fn + `","type":"EndDevice"}]`)
	a.HandleMessage("zigbee2mqtt/bridge/devices", bridgePayload, true)

	devs := store.Devices()
	if len(devs) != 1 {
		t.Fatalf("after bridge/devices: expected exactly 1 device, got %d: %v", len(devs), devIDs(devs))
	}
	for _, d := range devs {
		if d.Identity.Primary != ieee {
			t.Errorf("Primary: got %q, want %q", d.Identity.Primary, ieee)
		}
		if d.Identity.Display != fn {
			t.Errorf("Display: got %q, want %q", d.Identity.Display, fn)
		}
	}
}

// TestAdapter_FriendlyNamePhantomUpgradedOnBridgeDevices covers the simpler
// ordering where only one phantom exists (no interview-under-IEEE-topic
// step), confirming that case still works correctly.
func TestAdapter_FriendlyNamePhantomUpgradedOnBridgeDevices(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	a := New(engine, "zigbee2mqtt", nil)

	const ieee = "0x20a716fffea4634f"
	const fn = "islaav"

	// Only a payload under the friendly name arrives — one phantom.
	a.HandleMessage("zigbee2mqtt/"+fn, []byte(`{"power":5}`), false)

	if got := len(store.Devices()); got != 1 {
		t.Fatalf("expected 1 phantom, got %d", got)
	}

	// bridge/devices arrives and must upgrade the phantom in-place.
	bridgePayload := []byte(`[{"ieee_address":"` + ieee + `","friendly_name":"` + fn + `","type":"EndDevice"}]`)
	a.HandleMessage("zigbee2mqtt/bridge/devices", bridgePayload, true)

	devs := store.Devices()
	if len(devs) != 1 {
		t.Fatalf("expected exactly 1 device after upgrade, got %d: %v", len(devs), devIDs(devs))
	}
	for _, d := range devs {
		if d.Identity.Primary != ieee {
			t.Errorf("Primary: got %q, want %q", d.Identity.Primary, ieee)
		}
		if d.Identity.Display != fn {
			t.Errorf("Display: got %q, want %q", d.Identity.Display, fn)
		}
	}
}

func devIDs(devs map[string]model.Device) []string {
	out := make([]string, 0, len(devs))
	for k := range devs {
		out = append(out, k)
	}
	return out
}

func summary(evs []model.DerivedEvent) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, string(ev.Type))
	}
	return out
}
