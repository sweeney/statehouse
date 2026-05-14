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

// zid builds a zigbee-scheme identity for tests — keeps engine call
// sites readable without locking the engine to Z2M vocabulary.
func zid(primary, display string) model.DeviceIdentity {
	return model.DeviceIdentity{Scheme: "zigbee", Primary: primary, Display: display}
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
			Scheme:      "zigbee",
			Primary:     "0x00158d0000000001",
			Class:       "cycle_power_device",
			DisplayName: "Kitchen dishwasher",
			Location:    "kitchen",
		},
		"kitchen_kettle": {
			Scheme:      "zigbee",
			Primary:     "0x00158d0000000009",
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

func TestEngine_DiscoversByPrimary_KeepsStateOnDisplayRename(t *testing.T) {
	engine, store, col, clock := mkEngine()
	now := clock.Now()
	engine.IngestReading(zid("0x00158d0000000001", "kitchen_dishwasher"), "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: now, EnergyKWh: ptr(50.0)})
	d, ok := store.Get("kitchen_dishwasher")
	if !ok {
		t.Fatalf("expected configured id 'kitchen_dishwasher'")
	}
	if d.Class != "cycle_power_device" {
		t.Fatalf("expected configured class, got %q", d.Class)
	}
	// Same device, display renamed in Z2M. Primary (IEEE) is stable.
	engine.IngestReading(zid("0x00158d0000000001", "kitchen_dishwasher_new"), "zigbee2mqtt/kitchen_dishwasher_new",
		model.Reading{Timestamp: now.Add(time.Second), EnergyKWh: ptr(50.5)})
	d2, _ := store.Get("kitchen_dishwasher")
	if d2.Identity.Display != "kitchen_dishwasher_new" {
		t.Fatalf("expected display renamed, got %q", d2.Identity.Display)
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
	id := zid("0x00158d0000000001", "kitchen_dishwasher")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: now, EnergyKWh: ptr(50.0)})
	engine.SetAvailability(id, "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOffline)
	d, _ := store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOfflinePending {
		t.Fatalf("expected offline_pending, got %q", d.Availability)
	}
	engine.SetAvailability(id, "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOnline)
	d, _ = store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOnline {
		t.Fatalf("expected online after recover, got %q", d.Availability)
	}
	engine.SetAvailability(id, "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOffline)
	clock.Advance(31 * time.Second)
	engine.Tick()
	d, _ = store.Get("kitchen_dishwasher")
	if d.Availability != model.AvailabilityOffline {
		t.Fatalf("expected offline after debounce matured, got %q", d.Availability)
	}
}

func TestEngine_DivergenceWarning(t *testing.T) {
	engine, _, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000001", "kitchen_dishwasher")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0, EnergyKWh: ptr(100.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(50.0), EnergyKWh: ptr(100.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Hour), PowerW: ptr(50.0), EnergyKWh: ptr(101.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2*time.Hour + time.Second), PowerW: ptr(1.0), EnergyKWh: ptr(101.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
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
	id := zid("0x00158d0000000001", "kitchen_dishwasher")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: now, EnergyKWh: ptr(50.0)})
	engine.SetAvailability(id, "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOffline)
	clock.Advance(5 * time.Second)
	engine.SetAvailability(id, "zigbee2mqtt/kitchen_dishwasher/availability", model.AvailabilityOnline)
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
	id := zid("0x00158d0000000009", "kitchen_kettle")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: now, PowerW: ptr(2000.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_kettle",
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
	engine.IngestReading(zid("0x00158d0000000009", "kitchen_kettle"), "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: now, PowerW: ptr(2000.0)})
	h := store.House()
	if h.State != model.HouseActive {
		t.Fatalf("expected house active while kettle running, got %q", h.State)
	}
}

// TestEngine_SensorPopulatesBatteryAndReportingActivity verifies
// the new sensor_device class: a climate sensor reading populates
// Latest temp/humidity/battery, the device's activity flips
// unknown→reporting, and the engine emits canonical events for each
// measurement (including battery) so downstream sinks can record them.
func TestEngine_SensorPopulatesBatteryAndReportingActivity(t *testing.T) {
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"sensor_device": {
			NameHints: []string{"climate"},
		},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)
	col := &collector{}
	engine.AddDerivedSink(col)
	engine.AddCanonicalSink(col)

	engine.IngestReading(zid("0xaa", "bedroom_climate"), "zigbee2mqtt/bedroom_climate",
		model.Reading{
			Timestamp:    clock.Now(),
			TemperatureC: ptr(21.4),
			HumidityPct:  ptr(54.3),
			Battery:      ptr(87.0),
			LinkQuality:  ptr(156),
		})

	d, ok := store.Get("bedroom_climate")
	if !ok {
		t.Fatalf("expected device 'bedroom_climate' classified by name-hint")
	}
	if d.Class != "sensor_device" {
		t.Fatalf("expected sensor_device class, got %q", d.Class)
	}
	if d.Activity.State != model.ActivityReporting {
		t.Errorf("expected activity=reporting, got %q", d.Activity.State)
	}
	if d.Latest.TemperatureC == nil || *d.Latest.TemperatureC != 21.4 {
		t.Errorf("expected Latest.TemperatureC=21.4, got %v", d.Latest.TemperatureC)
	}
	if d.Latest.HumidityPct == nil || *d.Latest.HumidityPct != 54.3 {
		t.Errorf("expected Latest.HumidityPct=54.3, got %v", d.Latest.HumidityPct)
	}
	if d.Latest.BatteryPct == nil || *d.Latest.BatteryPct != 87.0 {
		t.Errorf("expected Latest.BatteryPct=87, got %v", d.Latest.BatteryPct)
	}
	if d.Cycle != nil {
		t.Errorf("sensor must not have a Cycle, got %+v", d.Cycle)
	}

	// Canonical events emitted: one each for temperature, humidity, battery.
	attrs := map[string]int{}
	for _, ce := range col.canonical {
		attrs[ce.Attribute]++
	}
	for _, want := range []string{"temperature_c", "humidity_pct", "battery_pct"} {
		if attrs[want] != 1 {
			t.Errorf("expected exactly 1 canonical event with attribute=%q, got %d", want, attrs[want])
		}
	}
}

func TestEngine_SensorDoesNotMakeHouseActive(t *testing.T) {
	// Sensors must not trip the house-state derivation into "active";
	// they're measurement-only and report continuously regardless of
	// presence.
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"sensor_device": {NameHints: []string{"climate"}},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)

	engine.IngestReading(zid("0xaa", "bedroom_climate"), "zigbee2mqtt/bedroom_climate",
		model.Reading{Timestamp: clock.Now(), TemperatureC: ptr(21.4)})
	h := store.House()
	if h.State == model.HouseActive {
		t.Fatalf("sensor reading must not make house active, got %q", h.State)
	}
}

// TestEngine_EnvironmentFieldsEmitCanonicalEvents verifies that a Reading
// with PressureHPa and WindSpeedMS set produces canonical events with
// the matching attribute names so downstream sinks (e.g. influx.Writer)
// can store them.
func TestEngine_EnvironmentFieldsEmitCanonicalEvents(t *testing.T) {
	engine, _, col, clock := mkEngine()
	now := clock.Now()
	id := zid("0xbb", "weather_station")
	engine.IngestReading(id, "tasmota/weather_station/SENSOR",
		model.Reading{
			Timestamp:   now,
			PressureHPa: ptr(1013.25),
			WindSpeedMS: ptr(5.2),
		})

	attrs := map[string]int{}
	for _, ce := range col.canonical {
		attrs[ce.Attribute]++
	}
	for _, want := range []string{"pressure_hpa", "wind_speed_ms"} {
		if attrs[want] != 1 {
			t.Errorf("expected exactly 1 canonical event with attribute=%q, got %d", want, attrs[want])
		}
	}
	// Verify the values are correct.
	for _, ce := range col.canonical {
		switch ce.Attribute {
		case "pressure_hpa":
			if v, ok := ce.Value.(float64); !ok || v != 1013.25 {
				t.Errorf("pressure_hpa: expected 1013.25, got %v", ce.Value)
			}
		case "wind_speed_ms":
			if v, ok := ce.Value.(float64); !ok || v != 5.2 {
				t.Errorf("wind_speed_ms: expected 5.2, got %v", ce.Value)
			}
		}
	}
}

// TestEngine_SchemeAgnostic verifies the engine treats DeviceIdentity
// generically — a synthetic "tasmota" scheme works identically to
// "zigbee", which is the whole point of the adapter abstraction.
func TestEngine_SchemeAgnostic(t *testing.T) {
	engine, store, col, clock := mkEngine()
	now := clock.Now()
	// Hint-classify by display so we don't need a configured device.
	id := model.DeviceIdentity{Scheme: "tasmota", Primary: "DVES_123ABC", Display: "kettle_tasmota"}
	engine.IngestReading(id, "tasmota/kettle_tasmota/SENSOR",
		model.Reading{Timestamp: now, PowerW: ptr(2000.0)})
	if _, ok := col.findDerived(model.EvtDeviceDiscovered); !ok {
		t.Fatalf("expected discovery for tasmota device, got %v", col.derived)
	}
	if _, ok := store.Get("kettle_tasmota"); !ok {
		t.Fatalf("expected store record for tasmota device")
	}
}
