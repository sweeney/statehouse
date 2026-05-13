package mqtt

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
				IdleBelowW: 5, ActiveAboveW: 20,
				ActiveSustainedFor:   10 * time.Second,
				InactiveSustainedFor: 5 * time.Minute,
			},
			EnergyStrategy: "counter",
		},
		"short_burst_power_device": {
			NameHints: []string{"kettle"},
			DefaultThresholds: config.Thresholds{
				IdleBelowW: 5, ActiveAboveW: 50,
				ActiveSustainedFor:   3 * time.Second,
				InactiveSustainedFor: 10 * time.Second,
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

func replay(t *testing.T, path string, clock *testutil.FakeClock, sub *Z2MSubscriber) {
	t.Helper()
	events, err := testutil.LoadFixture(path)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		sub.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}
}

func TestFixture_DishwasherCycleProducesCycleEvents(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	sub := &Z2MSubscriber{Engine: engine, Base: "zigbee2mqtt"}

	path := filepath.Join("..", "testdata", "fixtures", "dishwasher_cycle.jsonl")
	replay(t, path, clock, sub)

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
	sub := &Z2MSubscriber{Engine: engine, Base: "zigbee2mqtt"}

	path := filepath.Join("..", "testdata", "fixtures", "kettle_short_burst.jsonl")
	replay(t, path, clock, sub)

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
	sub := &Z2MSubscriber{Engine: engine, Base: "zigbee2mqtt"}

	path := filepath.Join("..", "testdata", "fixtures", "bridge_restart_flicker.jsonl")
	replay(t, path, clock, sub)

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
	_ = store
}

func TestFixture_RenameKeepsState(t *testing.T) {
	cfg := fixtureCfg()
	// classify "plug" devices as short-burst so they get processed; we
	// only care about identity, not state-machine behaviour here.
	cfg.DeviceClasses["short_burst_power_device"] = config.DeviceClassConfig{
		NameHints: []string{"plug"},
		DefaultThresholds: config.Thresholds{
			IdleBelowW: 5, ActiveAboveW: 50,
			ActiveSustainedFor: 0, InactiveSustainedFor: 0,
		},
		EnergyStrategy: "integration",
	}
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 14, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	sub := &Z2MSubscriber{Engine: engine, Base: "zigbee2mqtt"}

	path := filepath.Join("..", "testdata", "fixtures", "rename_friendlyname.jsonl")
	replay(t, path, clock, sub)

	devs := store.Devices()
	if len(devs) != 1 {
		t.Fatalf("expected exactly one device after rename, got %d: %v", len(devs), devs)
	}
	for _, d := range devs {
		if d.Identity.FriendlyName != "new_plug" {
			t.Fatalf("expected friendly name 'new_plug' after rename, got %q", d.Identity.FriendlyName)
		}
		if d.Identity.IEEEAddress != "0x00158d0000000099" {
			t.Fatalf("expected stable IEEE, got %q", d.Identity.IEEEAddress)
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
