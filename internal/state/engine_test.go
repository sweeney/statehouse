package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
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
				IdleBelowW:           ptr(5.0),
				ActiveAboveW:         ptr(20.0),
				ActiveSustainedFor:   ptr(1 * time.Second),
				InactiveSustainedFor: ptr(1 * time.Second),
			},
			EnergyStrategy: "counter",
		},
		"short_burst_power_device": {
			DefaultThresholds: config.Thresholds{
				IdleBelowW:   ptr(5.0),
				ActiveAboveW: ptr(50.0),
			},
			EnergyStrategy: "integration",
		},
		"continuous_power_device": {
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           ptr(5.0),
				CompressorAboveW:     ptr(25.0),
				ActiveSustainedFor:   ptr(1 * time.Second),
				InactiveSustainedFor: ptr(1 * time.Second),
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
		"chestfreezer": {
			Scheme:      "zigbee",
			Primary:     "0x00158d0000000020",
			Class:       "continuous_power_device",
			DisplayName: "Chest freezer",
			Location:    "utility",
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
	// Counter-primary device (kitchen_dishwasher, cycle_power_device) with a
	// large counter/integration gap must fire a divergence warning — the counter
	// is the trusted source and a large gap from integration is a data-quality alert.
	engine, _, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000001", "kitchen_dishwasher")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0, PowerW: ptr(50.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(50.0), EnergyKWh: ptr(100.0)})
	// Counter jumps 1 kWh but integration is only ~42 Wh — ~99.9% divergence.
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(3 * time.Second), PowerW: ptr(1.0), EnergyKWh: ptr(101.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(5 * time.Second), PowerW: ptr(1.0), EnergyKWh: ptr(101.0)})
	if _, ok := col.findDerived(model.EvtCycleFinished); !ok {
		t.Fatalf("expected cycle_finished event, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyDivergenceWarning); !ok {
		t.Fatalf("expected energy_divergence_warning for counter-primary device, derived=%v", col.derived)
	}
}

func TestEngine_NoDivergenceForIntegrationPrimary(t *testing.T) {
	// Integration-primary device (kitchen_kettle, short_burst_power_device) with
	// a large counter/integration mismatch must NOT fire a divergence warning —
	// the counter is declared untrustworthy by the strategy choice, so any
	// counter/integration gap is expected noise (e.g. coarse 100 Wh counter ticks).
	engine, _, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000009", "kitchen_kettle")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: t0, PowerW: ptr(2000.0), EnergyKWh: ptr(100.0)})
	// Counter jumps 1 kWh; integration ≈ 2000W×1s ≈ 5.6e-4 kWh → ~99.9% divergence.
	// Because kitchen_kettle is integration-primary the warning must be suppressed.
	engine.IngestReading(id, "zigbee2mqtt/kitchen_kettle",
		model.Reading{Timestamp: t0.Add(time.Second), PowerW: ptr(0.0), EnergyKWh: ptr(101.0)})
	if _, ok := col.findDerived(model.EvtShortBurstDetected); !ok {
		t.Fatalf("expected short_burst_detected event, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyDivergenceWarning); ok {
		t.Fatal("must NOT emit divergence warning for integration-primary device")
	}
}

func TestEngine_LifetimeExtremes(t *testing.T) {
	engine, store, _, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000009", "kitchen_kettle")
	topic := "zigbee2mqtt/kitchen_kettle"

	// No lifetime block before any tracked measurement arrives.
	engine.IngestReading(id, topic, model.Reading{Timestamp: t0, VoltageV: ptr(240.0)})
	if d, _ := store.Get("kitchen_kettle"); d.Lifetime != nil {
		t.Fatalf("expected no lifetime block before any tracked measurement, got %+v", d.Lifetime)
	}

	// Power ratchets up to the peak and holds when it later drops.
	peakAt := t0.Add(2 * time.Second)
	engine.IngestReading(id, topic, model.Reading{Timestamp: t0.Add(1 * time.Second), PowerW: ptr(1200.0)})
	engine.IngestReading(id, topic, model.Reading{Timestamp: peakAt, PowerW: ptr(2400.0)})
	engine.IngestReading(id, topic, model.Reading{Timestamp: t0.Add(3 * time.Second), PowerW: ptr(800.0)})

	d, _ := store.Get("kitchen_kettle")
	if d.Lifetime == nil || d.Lifetime.MaxPower == nil {
		t.Fatalf("expected max power tracked, got %+v", d.Lifetime)
	}
	if d.Lifetime.MaxPower.Value != 2400.0 {
		t.Fatalf("expected max power 2400, got %v", d.Lifetime.MaxPower.Value)
	}
	if !d.Lifetime.MaxPower.At.Equal(peakAt) {
		t.Fatalf("expected max power at %v, got %v", peakAt, d.Lifetime.MaxPower.At)
	}

	// Temperature and humidity track both ends across readings.
	coldAt := t0.Add(4 * time.Second)
	hotAt := t0.Add(5 * time.Second)
	engine.IngestReading(id, topic, model.Reading{Timestamp: coldAt, TemperatureC: ptr(18.0), HumidityPct: ptr(55.0)})
	engine.IngestReading(id, topic, model.Reading{Timestamp: hotAt, TemperatureC: ptr(22.0), HumidityPct: ptr(40.0)})

	d, _ = store.Get("kitchen_kettle")
	if d.Lifetime.MinTemperature.Value != 18.0 || !d.Lifetime.MinTemperature.At.Equal(coldAt) {
		t.Fatalf("expected min temp 18 at %v, got %+v", coldAt, d.Lifetime.MinTemperature)
	}
	if d.Lifetime.MaxTemperature.Value != 22.0 || !d.Lifetime.MaxTemperature.At.Equal(hotAt) {
		t.Fatalf("expected max temp 22 at %v, got %+v", hotAt, d.Lifetime.MaxTemperature)
	}
	if d.Lifetime.MinHumidity.Value != 40.0 || d.Lifetime.MaxHumidity.Value != 55.0 {
		t.Fatalf("expected humidity range 40–55, got min=%+v max=%+v", d.Lifetime.MinHumidity, d.Lifetime.MaxHumidity)
	}
}

func TestEngine_StaleCounter(t *testing.T) {
	// Counter-primary device finishes a 32-second cycle with no EnergyKWh
	// readings — counter is absent/stuck. Must fire stale_counter_warning
	// and NOT fire divergence_warning.
	engine, store, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000001", "kitchen_dishwasher")
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0, PowerW: ptr(50.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(50.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(33 * time.Second), PowerW: ptr(1.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(34 * time.Second), PowerW: ptr(1.0)})
	if _, ok := col.findDerived(model.EvtCycleFinished); !ok {
		t.Fatalf("expected cycle_finished event, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyStaleCounterWarning); !ok {
		t.Fatalf("expected energy_stale_counter_warning, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyDivergenceWarning); ok {
		t.Fatal("must NOT emit divergence_warning alongside stale_counter")
	}
	d, _ := store.Get("kitchen_dishwasher")
	if d.Cycle == nil || !d.Cycle.Energy.StaleCounter {
		t.Fatal("expected StaleCounter=true on device cycle")
	}
}

func TestEngine_NoDivergenceWhenNoCounterData(t *testing.T) {
	// Devices like chestfreezer/wine-fridge have no energy counter — they
	// never report EnergyKWh. ReportedKWhDelta stays 0 and the divergence
	// check must NOT fire: there is no counter to compare integration against.
	engine, _, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000001", "kitchen_dishwasher")
	// Drive a complete cycle with power readings only — no EnergyKWh.
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0, PowerW: ptr(50.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(50.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(3 * time.Second), PowerW: ptr(1.0)})
	engine.IngestReading(id, "zigbee2mqtt/kitchen_dishwasher",
		model.Reading{Timestamp: t0.Add(5 * time.Second), PowerW: ptr(1.0)})
	if _, ok := col.findDerived(model.EvtCycleFinished); !ok {
		t.Fatalf("expected cycle_finished event, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyDivergenceWarning); ok {
		t.Fatalf("must NOT emit divergence warning when device has no energy counter")
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
	if h.Occupancy.State != model.OccupancyOccupied {
		t.Fatalf("expected house occupied while kettle running, got %q", h.Occupancy.State)
	}
}

// TestEngine_SensorPopulatesBatteryAndReportingActivity verifies
// the environmental_sensor class: a climate sensor reading populates
// Latest temp/humidity/battery, the device's activity flips
// unknown→reporting, and the engine emits canonical events for each
// measurement (including battery) so downstream sinks can record them.
func TestEngine_SensorPopulatesBatteryAndReportingActivity(t *testing.T) {
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"environmental_sensor": {
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
	if d.Class != "environmental_sensor" {
		t.Fatalf("expected environmental_sensor class, got %q", d.Class)
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
		"environmental_sensor": {NameHints: []string{"climate"}},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)

	engine.IngestReading(zid("0xaa", "bedroom_climate"), "zigbee2mqtt/bedroom_climate",
		model.Reading{Timestamp: clock.Now(), TemperatureC: ptr(21.4)})
	h := store.House()
	if h.Occupancy.State == model.OccupancyOccupied && h.Activity.State == model.HouseActivityBusy {
		t.Fatalf("sensor reading must not make house busy/active, got occupancy=%q activity=%q", h.Occupancy.State, h.Activity.State)
	}
}

// TestEngine_EnvironmentFieldsEmitCanonicalEvents verifies that a Reading
// with all 9 new environment/ups/radio fields produces canonical events
// with the matching attribute names and correct values so downstream
// sinks (e.g. influx.Writer) can store them.
func TestEngine_EnvironmentFieldsEmitCanonicalEvents(t *testing.T) {
	engine, _, col, clock := mkEngine()
	now := clock.Now()
	id := zid("0xbb", "weather_station")
	onBattery := true
	rssi := -72
	engine.IngestReading(id, "tasmota/weather_station/SENSOR",
		model.Reading{
			Timestamp:          now,
			PressureHPa:        ptr(1013.25),
			WindSpeedMS:        ptr(5.2),
			WindDirDeg:         ptr(270.0),
			RainfallMM:         ptr(3.4),
			IlluminanceLux:     ptr(800.0),
			UVIndex:            ptr(4.5),
			BatteryRuntimeMins: ptr(42.0),
			OnBattery:          &onBattery,
			RSSI:               &rssi,
		})

	attrs := map[string]any{}
	for _, ce := range col.canonical {
		attrs[ce.Attribute] = ce.Value
	}

	wantAttrs := []string{
		"pressure_hpa", "wind_speed_ms", "wind_dir_deg", "rainfall_mm",
		"illuminance_lux", "uv_index", "battery_runtime_mins", "on_battery", "rssi_dbm",
	}
	for _, want := range wantAttrs {
		if _, ok := attrs[want]; !ok {
			t.Errorf("expected canonical event with attribute=%q, not found in %v", want, col.canonical)
		}
	}

	// Verify the values are correct.
	type wantVal struct {
		attr string
		val  any
	}
	checks := []wantVal{
		{"pressure_hpa", float64(1013.25)},
		{"wind_speed_ms", float64(5.2)},
		{"wind_dir_deg", float64(270.0)},
		{"rainfall_mm", float64(3.4)},
		{"illuminance_lux", float64(800.0)},
		{"uv_index", float64(4.5)},
		{"battery_runtime_mins", float64(42.0)},
		{"on_battery", true},
		{"rssi_dbm", -72},
	}
	for _, c := range checks {
		if got, ok := attrs[c.attr]; !ok {
			t.Errorf("%s: attribute not found", c.attr)
		} else if got != c.val {
			t.Errorf("%s: expected %v, got %v", c.attr, c.val, got)
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

func TestEngine_ContinuousDevice_NoStaleCounterOnSmallCycle(t *testing.T) {
	// Compressor cycles are typically too short to accumulate a full counter
	// tick (~10 Wh resolution). With integration strategy the counter staying
	// at zero is expected and must NOT fire a stale_counter warning.
	engine, store, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000020", "chestfreezer")
	// t0: standby draw, device known.
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0, PowerW: ptr(2.0)})
	// t0+2s: compressor kicks in above threshold — cycle candidate starts.
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(60.0)})
	// t0+3s: sustained → compressor cycle starts.
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(3 * time.Second), PowerW: ptr(60.0)})
	// t0+35s: compressor stops — no counter reading ever arrives.
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(35 * time.Second), PowerW: ptr(2.0)})
	// t0+36s: sustained low → cycle finishes.
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(36 * time.Second), PowerW: ptr(2.0)})
	if _, ok := col.findDerived(model.EvtContinuousCycleFinished); !ok {
		t.Fatalf("expected continuous_cycle_finished, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyStaleCounterWarning); ok {
		t.Fatal("must NOT emit stale_counter_warning for integration-strategy continuous device")
	}
	d, _ := store.Get("chestfreezer")
	if d.Cycle == nil {
		t.Fatal("expected cycle on device")
	}
	if d.Cycle.Energy.StaleCounter {
		t.Fatal("expected StaleCounter=false for integration-strategy device")
	}
	if d.Cycle.Energy.PrimarySource != "integration" {
		t.Fatalf("expected primary_source=integration, got %q", d.Cycle.Energy.PrimarySource)
	}
}

func TestEngine_ContinuousDevice_NoDivergenceForIntegrationPrimary(t *testing.T) {
	// Continuous device (chestfreezer, integration strategy): even when the
	// counter ticks once during a short compressor cycle (coarse 100 Wh
	// resolution), the divergence warning must NOT fire. The counter is
	// untrustworthy by class design — the mismatch is expected noise.
	engine, _, col, clock := mkEngine()
	t0 := clock.Now()
	id := zid("0x00158d0000000020", "chestfreezer")
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0, PowerW: ptr(2.0), EnergyKWh: ptr(10.0)})
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(2 * time.Second), PowerW: ptr(60.0), EnergyKWh: ptr(10.0)})
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(3 * time.Second), PowerW: ptr(60.0), EnergyKWh: ptr(10.0)})
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(35 * time.Second), PowerW: ptr(2.0), EnergyKWh: ptr(11.0)})
	// Counter delta=1 kWh; integration≈60W×32s≈5.3e-4 kWh → ~99.9% nominal divergence —
	// but must be suppressed because chestfreezer is integration-primary.
	engine.IngestReading(id, "zigbee2mqtt/chestfreezer",
		model.Reading{Timestamp: t0.Add(36 * time.Second), PowerW: ptr(2.0), EnergyKWh: ptr(11.0)})
	if _, ok := col.findDerived(model.EvtContinuousCycleFinished); !ok {
		t.Fatalf("expected continuous_cycle_finished, derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyDivergenceWarning); ok {
		t.Fatal("must NOT emit divergence_warning for integration-primary continuous device")
	}
	if _, ok := col.findDerived(model.EvtEnergyStaleCounterWarning); ok {
		t.Fatal("must NOT emit stale_counter_warning for integration-strategy device")
	}
}

// TestEngine_DeviceStrategyOverrideAppliedAfterPhantomUpgrade verifies that
// when a device is first discovered by display name (triggering name-hint
// classification with the class-level energy strategy) and later upgraded to
// a full IEEE identity whose device config carries a different energy_strategy,
// the runtime profile is updated so that the per-device override wins.
//
// Regression for the kitchenkettle stale_counter false-positive: the class
// config had energy_strategy=counter but the device config overrode it to
// integration. The name-hint path set counter on the runtime profile; the
// phantom→real upgrade left the profile stale because only a class change
// triggered a runtime rebuild.
func TestEngine_DeviceStrategyOverrideAppliedAfterPhantomUpgrade(t *testing.T) {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.Energy.DivergenceWarningPct = 20
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"short_burst_power_device": {
			NameHints: []string{"kettle"},
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           ptr(5.0),
				ActiveAboveW:         ptr(50.0),
				ActiveSustainedFor:   ptr(time.Duration(0)),
				InactiveSustainedFor: ptr(1 * time.Second),
			},
			EnergyStrategy: "counter", // class default is counter
		},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"the_kettle": {
			Scheme:         "zigbee",
			Primary:        "0xaabbccddeeff0011",
			Class:          "short_burst_power_device",
			EnergyStrategy: "integration", // per-device override wins
		},
	}

	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)
	col := &collector{}
	engine.AddDerivedSink(col)

	// Phase 1: display-only discovery (Z2M friendly name seen before bridge/devices).
	// Resolver cannot find the device config (no primary, no matching display key)
	// so falls through to name hints → class=short_burst_power_device, strategy=counter.
	phantomID := model.DeviceIdentity{Scheme: "zigbee", Display: "kettle"}
	engine.EnsureDiscovered(phantomID, "zigbee2mqtt/kettle")

	// Phase 2: full identity arrives (IEEE address now known).
	// Device config carries energy_strategy=integration — must replace the counter
	// strategy that was set in phase 1.
	fullID := model.DeviceIdentity{Scheme: "zigbee", Primary: "0xaabbccddeeff0011", Display: "kettle"}
	engine.EnsureDiscovered(fullID, "zigbee2mqtt/kettle")

	// Phase 3: run a cycle. Counter does not tick (ReportedKWhDelta = 0).
	// Integration accumulates well above the stale-counter threshold (1 Wh).
	// If the runtime still has strategy=counter a stale_counter_warning fires.
	t0 := clock.Now()
	engine.IngestReading(fullID, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: t0, PowerW: ptr(2000.0), EnergyKWh: ptr(10.0)})
	engine.IngestReading(fullID, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: t0.Add(30 * time.Second), PowerW: ptr(2000.0), EnergyKWh: ptr(10.0)})
	// Power drops; cycle ends after InactiveSustainedFor (1s).
	engine.IngestReading(fullID, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: t0.Add(31 * time.Second), PowerW: ptr(0.0), EnergyKWh: ptr(10.0)})
	engine.IngestReading(fullID, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: t0.Add(33 * time.Second), PowerW: ptr(0.0), EnergyKWh: ptr(10.0)})

	if _, ok := col.findDerived(model.EvtShortBurstDetected); !ok {
		t.Fatalf("expected short_burst_detected, got derived=%v", col.derived)
	}
	if _, ok := col.findDerived(model.EvtEnergyStaleCounterWarning); ok {
		t.Error("stale_counter_warning must NOT fire when per-device energy_strategy=integration overrides class default of counter")
	}
}

func TestEngine_ReloadConfig_UpdatesDeviceProfile(t *testing.T) {
	cfg := config.Default()
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"short_burst_power_device": {
			NameHints:      []string{"toaster"},
			EnergyStrategy: "integration",
			DefaultThresholds: config.Thresholds{
				IdleBelowW:   ptr(5.0),
				ActiveAboveW: ptr(50.0),
			},
		},
		"cycle_power_device": {
			EnergyStrategy: "counter",
			DefaultThresholds: config.Thresholds{
				IdleBelowW:   ptr(5.0),
				ActiveAboveW: ptr(20.0),
			},
		},
	}
	// No cfg.Devices entry — device will be classified by name hint.
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)

	identity := model.DeviceIdentity{Scheme: "zigbee", Primary: "0xaabbccdd", Display: "kitchen_toaster"}
	engine.IngestReading(identity, "zigbee2mqtt/kitchen_toaster", model.Reading{Timestamp: clock.Now()})

	var gotResolution device.Resolution
	var gotClass string
	store.withEntry("kitchen_toaster", func(ent *deviceEntry) {
		gotResolution = ent.Runtime.Profile.Resolution
		gotClass = ent.Runtime.Profile.Class
	})
	if gotResolution != device.ResolutionNameHint {
		t.Fatalf("before reload: want resolution %q, got %q", device.ResolutionNameHint, gotResolution)
	}
	if gotClass != "short_burst_power_device" {
		t.Fatalf("before reload: want class short_burst_power_device, got %q", gotClass)
	}

	// Reload with an explicit device_config override promoting the toaster
	// to cycle_power_device. ReloadConfig does not exist yet — this test is RED.
	newCfg := cfg
	newCfg.Devices = map[string]config.DeviceConfig{
		"kitchen_toaster": {
			Scheme:      "zigbee",
			Primary:     "0xaabbccdd",
			Class:       "cycle_power_device",
			DisplayName: "Kitchen Toaster",
		},
	}
	engine.ReloadConfig(newCfg)

	store.withEntry("kitchen_toaster", func(ent *deviceEntry) {
		gotResolution = ent.Runtime.Profile.Resolution
		gotClass = ent.Runtime.Profile.Class
	})
	if gotResolution != device.ResolutionDeviceConfig {
		t.Fatalf("after reload: want resolution %q, got %q", device.ResolutionDeviceConfig, gotResolution)
	}
	if gotClass != "cycle_power_device" {
		t.Fatalf("after reload: want class cycle_power_device, got %q", gotClass)
	}
}
