package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

type collector struct {
	derived   []model.DerivedEvent
	canonical []model.CanonicalEvent
}

func (c *collector) OnDerivedEvent(ev model.DerivedEvent)     { c.derived = append(c.derived, ev) }
func (c *collector) OnCanonicalEvent(ev model.CanonicalEvent) { c.canonical = append(c.canonical, ev) }

func (c *collector) findDerived(t model.DerivedEventType) (model.DerivedEvent, bool) {
	for i := len(c.derived) - 1; i >= 0; i-- {
		if c.derived[i].Type == t {
			return c.derived[i], true
		}
	}
	return model.DerivedEvent{}, false
}

func mkEngine() (*Engine, *Store, *collector, *testutil.FakeClock) {
	cfg := config.Default()
	cfg.Energy.DivergenceWarningPct = 20
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.Availability.OfflineDebounce = 30 * time.Second
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"cycle_power_device": {
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           5,
				ActiveAboveW:         20,
				ActiveSustainedFor:   1 * time.Second,
				InactiveSustainedFor: 1 * time.Second,
			},
			EnergyStrategy: "counter",
		},
		"short_burst_power_device": {
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           5,
				ActiveAboveW:         50,
				ActiveSustainedFor:   0,
				InactiveSustainedFor: 0,
			},
			EnergyStrategy: "integration",
		},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"kitchen_dishwasher": {
			IEEEAddress: "0x00158d0000000001",
			Class:       "cycle_power_device",
			DisplayName: "Kitchen dishwasher",
			Location:    "kitchen",
		},
		"kitchen_kettle": {
			IEEEAddress: "0x00158d0000000009",
			Class:       "short_burst_power_device",
			DisplayName: "Kitchen kettle",
			Location:    "kitchen",
		},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)
	col := &collector{}
	engine.AddDerivedSink(col)
	engine.AddCanonicalSink(col)
	return engine, store, col, clock
}

func ptr[T any](v T) *T { return &v }

func TestEngine_DiscoversByIEEE_KeepsStateOnRename(t *testing.T) {
	engine, store, col, clock := mkEngine()
	now := clock.Now()
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: now, EnergyKWh: ptr(50.0)})
	d, ok := store.Get("kitchen_dishwasher")
	if !ok {
		t.Fatalf("expected configured id 'kitchen_dishwasher'")
	}
	if d.Class != "cycle_power_device" {
		t.Fatalf("expected configured class, got %q", d.Class)
	}
	// Same device, renamed in Z2M.
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher_new", "zigbee2mqtt/kitchen_dishwasher_new",
		model.Reading{Timestamp: now.Add(time.Second), EnergyKWh: ptr(50.5)})
	d2, _ := store.Get("kitchen_dishwasher")
	if d2.Identity.FriendlyName != "kitchen_dishwasher_new" {
		t.Fatalf("expected friendly name renamed, got %q", d2.Identity.FriendlyName)
	}
	if d2.SourceTopic == d.SourceTopic {
		t.Fatalf("expected source topic updated, still %q", d2.SourceTopic)
	}
	// Discovered event should only fire once.
	discovered := 0
	for _, ev := range col.derived {
		if ev.Type == model.EvtDeviceDiscovered {
			discovered++
		}
	}
	if discovered != 1 {
		t.Fatalf("expected exactly 1 discovered event, got %d", discovered)
	}
}

func TestEngine_AvailabilityDebounce(t *testing.T) {
	engine, store, _, clock := mkEngine()
	now := clock.Now()
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: now, EnergyKWh: ptr(50.0)})
	// Flicker offline.
	engine.SetAvailability("", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOffline)
	d, _ := store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOfflinePending {
		t.Fatalf("expected offline_pending, got %q", d.Availability)
	}
	// Comes back quickly - should clear pending without ever going to offline.
	engine.SetAvailability("", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOnline)
	d, _ = store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOnline {
		t.Fatalf("expected online after recover, got %q", d.Availability)
	}
	// Offline again, then tick after debounce should mature.
	engine.SetAvailability("", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOffline)
	clock.Advance(31 * time.Second)
	engine.Tick()
	d, _ = store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOffline {
		t.Fatalf("expected offline after debounce matured, got %q", d.Availability)
	}
}

func TestEngine_DivergenceWarning(t *testing.T) {
	engine, _, col, clock := mkEngine()
	// Start with seed.
	t0 := clock.Now()
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0, EnergyKWh: ptr(100.0)})
	// Begin cycle.
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(50.0), EnergyKWh: ptr(100.0)})
	// Long quiet stretch where reporting is sparse: power reported as ~50W
	// for only a single early sample, but the counter advances 1.0kWh. The
	// integrated path will reflect ~50W * 2s and a clamped gap = effectively zero.
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Hour), PowerW: ptr(50.0), EnergyKWh: ptr(101.0)})
	// Now finish: drop to idle, sustain.
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2*time.Hour + time.Second), PowerW: ptr(1.0), EnergyKWh: ptr(101.0)})
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2*time.Hour + 30*time.Second), PowerW: ptr(1.0), EnergyKWh: ptr(101.0)})
	if _, ok := col.findDerived(model.EvtCycleFinished); !ok {
		t.Fatalf("expected cycle_finished event, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyDivergenceWarning); !ok {
		t.Fatalf("expected energy_divergence_warning, derived=%v", col.derived)
	}
}

func TestEngine_BridgeRestartFlickerDoesNotAlarm(t *testing.T) {
	engine, _, col, clock := mkEngine()
	now := clock.Now()
	engine.IngestReading("0x00158d0000000001", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: now, EnergyKWh: ptr(50.0)})
	// Bridge restart: offline + online inside debounce window.
	engine.SetAvailability("", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOffline)
	clock.Advance(5 * time.Second)
	engine.SetAvailability("", "kitchen_dishwasher", "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOnline)
	// Count derived availability events: we expect pending + online but not offline.
	var seen []string
	for _, ev := range col.derived {
		if ev.Type == model.EvtDeviceAvailabilityChanged {
			seen = append(seen, ev.Evidence["availability"].(string))
		}
	}
	for _, s := range seen {
		if s == string(model.AvailabilityOffline) {
			t.Fatalf("flicker must not produce a confirmed offline event, got events %v", seen)
		}
	}
}

func TestEngine_ShortBurstCycle(t *testing.T) {
	engine, _, col, clock := mkEngine()
	now := clock.Now()
	engine.IngestReading("0x00158d0000000009", "kitchen_kettle", "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: now, PowerW: ptr(2000.0)})
	engine.IngestReading("0x00158d0000000009", "kitchen_kettle", "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: now.Add(45 * time.Second), PowerW: ptr(0.0)})
	if _, ok := col.findDerived(model.EvtCycleStarted); ok {
		t.Logf("note: short-burst class also produced cycle_started")
	}
	if _, ok := col.findDerived(model.EvtShortBurstDetected); !ok {
		t.Fatalf("expected short_burst_detected, derived=%v", col.derived)
	}
}

func TestEngine_HouseRecomputesOnActivity(t *testing.T) {
	engine, store, _, clock := mkEngine()
	now := clock.Now()
	engine.IngestReading("0x00158d0000000009", "kitchen_kettle", "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: now, PowerW: ptr(2000.0)})
	h := store.House()
	if h.State != model.HouseActive {
		t.Fatalf("expected house active while kettle running, got %q", h.State)
	}
}
