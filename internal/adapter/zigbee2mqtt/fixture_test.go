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

func summary(evs []model.DerivedEvent) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, string(ev.Type))
	}
	return out
}
