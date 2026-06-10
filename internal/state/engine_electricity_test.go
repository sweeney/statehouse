package state

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// countingSink is a minimal thread-safe sink used by concurrent tests
// where the standard `collector` would itself race.
type countingSink struct {
	mu        sync.Mutex
	canonical int
}

func (c *countingSink) OnCanonicalEvent(_ model.CanonicalEvent) {
	c.mu.Lock()
	c.canonical++
	c.mu.Unlock()
}

func (c *countingSink) OnDerivedEvent(_ model.DerivedEvent) {}

// mkElectricityEngine builds an engine that knows about a meter and a
// pair of plugs (one short_burst, one cycle_power). Short staleness
// windows keep stale tests fast.
func mkElectricityEngine(t *testing.T) (*Engine, *Store, *collector, *testutil.FakeClock) {
	t.Helper()
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
		"energy_meter": {},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"house_meter": {
			Scheme:      "meter",
			Primary:     "abcd1234",
			Class:       "energy_meter",
			DisplayName: "House meter",
		},
		"kitchen_kettle": {
			Scheme:      "zigbee",
			Primary:     "0x00158d0000000009",
			Class:       "short_burst_power_device",
			DisplayName: "Kitchen kettle",
		},
		"kitchen_dishwasher": {
			Scheme:      "zigbee",
			Primary:     "0x00158d0000000001",
			Class:       "cycle_power_device",
			DisplayName: "Kitchen dishwasher",
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

func meterID() model.DeviceIdentity {
	return model.DeviceIdentity{Scheme: "meter", Primary: "abcd1234", Display: "abcd1234"}
}

func ingestMeter(e *Engine, at time.Time, powerW float64) {
	p := powerW
	e.IngestReading(meterID(), "energy/abcd1234/SENSOR/electricitymeter",
		model.Reading{Timestamp: at, PowerW: &p})
}

func ingestPlug(e *Engine, primary, display string, at time.Time, powerW float64) {
	p := powerW
	e.IngestReading(
		model.DeviceIdentity{Scheme: "zigbee", Primary: primary, Display: display},
		"zigbee2mqtt/"+display,
		model.Reading{Timestamp: at, PowerW: &p},
	)
}

func countHouseCanonical(c *collector, attr string) int {
	n := 0
	for _, ev := range c.canonical {
		if ev.DeviceID == HouseDeviceID && (attr == "" || ev.Attribute == attr) {
			n++
		}
	}
	return n
}

func lastHouseValue(t *testing.T, c *collector, attr string) float64 {
	t.Helper()
	for i := len(c.canonical) - 1; i >= 0; i-- {
		ev := c.canonical[i]
		if ev.DeviceID == HouseDeviceID && ev.Attribute == attr {
			v, ok := ev.Value.(float64)
			if !ok {
				t.Fatalf("attr %q value %v not float64", attr, ev.Value)
			}
			return v
		}
	}
	t.Fatalf("no canonical event for attr %q", attr)
	return 0
}

func TestEngineElectricity_MeterTickEmitsCanonicalSet(t *testing.T) {
	engine, _, col, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1500)

	wantAttrs := []string{
		"gross_w", "monitored_w", "unmonitored_w", "coverage",
		"session_gross_kwh", "session_monitored_kwh", "session_unmonitored_kwh",
		"stale_device_count",
	}
	for _, a := range wantAttrs {
		if countHouseCanonical(col, a) != 1 {
			t.Errorf("attr %q: got %d emits want 1", a, countHouseCanonical(col, a))
		}
	}
	for _, ev := range col.canonical {
		if ev.DeviceID == HouseDeviceID {
			if ev.Capability != HouseElectricityCapability {
				t.Errorf("capability=%q want %q", ev.Capability, HouseElectricityCapability)
			}
			if !ev.Timestamp.Equal(clock.Now()) {
				t.Errorf("timestamp=%v want %v", ev.Timestamp, clock.Now())
			}
		}
	}
}

// ingestMeterPeriods sends a meter reading carrying the authoritative
// day/week/month period totals alongside instantaneous power.
func ingestMeterPeriods(e *Engine, at time.Time, powerW, day, week, month float64) {
	p, d, w, m := powerW, day, week, month
	e.IngestReading(meterID(), "energy/abcd1234/SENSOR/electricitymeter",
		model.Reading{Timestamp: at, PowerW: &p,
			MeterTodayKWh: &d, MeterWeekKWh: &w, MeterMonthKWh: &m})
}

func TestEngineElectricity_MeterPeriodsSurfacedAndPersist(t *testing.T) {
	engine, store, col, clock := mkElectricityEngine(t)

	ingestMeterPeriods(engine, clock.Now(), 1000, 6.83, 47.65, 250.87)

	e := store.House().Electricity
	if e.TodayKWh == nil || *e.TodayKWh != 6.83 {
		t.Fatalf("TodayKWh=%v want 6.83", e.TodayKWh)
	}
	if e.WeekKWh == nil || *e.WeekKWh != 47.65 {
		t.Fatalf("WeekKWh=%v want 47.65", e.WeekKWh)
	}
	if e.MonthKWh == nil || *e.MonthKWh != 250.87 {
		t.Fatalf("MonthKWh=%v want 250.87", e.MonthKWh)
	}
	// Session.Since is the engine start, not zero.
	if e.Session.Since.IsZero() {
		t.Fatalf("Session.Since is zero; want engine start time")
	}
	// The authoritative period totals are emitted to the canonical stream.
	for _, attr := range []string{"today_kwh", "week_kwh", "month_kwh"} {
		if countHouseCanonical(col, attr) != 1 {
			t.Errorf("attr %q: got %d emits want 1", attr, countHouseCanonical(col, attr))
		}
	}

	// A later plug-triggered recompute must NOT drop the meter totals:
	// they persist until the meter reports again.
	clock.Advance(10 * time.Second)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 100)
	e = store.House().Electricity
	if e.TodayKWh == nil || *e.TodayKWh != 6.83 {
		t.Fatalf("TodayKWh dropped after plug recompute: %v", e.TodayKWh)
	}
}

func TestEngineElectricity_NoMeterPeriodsWhenAbsent(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000) // no period counters
	e := store.House().Electricity
	if e.TodayKWh != nil || e.WeekKWh != nil || e.MonthKWh != nil {
		t.Fatalf("period totals set without meter reporting them: %+v", e)
	}
}

// TestEngineElectricity_StateBoundedOverManyTicks guards the leak-relevant
// invariant for the meter path: ingesting many readings replaces state in
// place rather than accumulating it. The device set stays at one meter and
// the summary reflects only the latest period totals.
func TestEngineElectricity_StateBoundedOverManyTicks(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	var lastToday float64
	for i := 0; i < 500; i++ {
		lastToday = 1.0 + float64(i)*0.001
		ingestMeterPeriods(engine, clock.Now(), 1000, lastToday, 10, 100)
		clock.Advance(10 * time.Second)
	}
	if n := len(store.Devices()); n != 1 {
		t.Fatalf("device count=%d after 500 meter ticks; want 1 (state must not accumulate)", n)
	}
	e := store.House().Electricity
	if e.TodayKWh == nil || *e.TodayKWh != lastToday {
		t.Fatalf("TodayKWh=%v want latest %v (summary must hold only the latest, not grow)", e.TodayKWh, lastToday)
	}
}

func TestEngineElectricity_NoMeterNoEmit(t *testing.T) {
	engine, store, col, clock := mkElectricityEngine(t)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 100)
	if countHouseCanonical(col, "") != 0 {
		t.Fatalf("plug-only ingest emitted %d house events; want 0", countHouseCanonical(col, ""))
	}
	if !store.House().Electricity.ComputedAt.IsZero() {
		t.Fatalf("ComputedAt set without any meter reading: %v", store.House().Electricity.ComputedAt)
	}
}

func TestEngineElectricity_PlugUpdateLive_NoEmit(t *testing.T) {
	engine, store, col, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	before := countHouseCanonical(col, "")
	if before != 8 {
		t.Fatalf("expected 8 canonical emits from first meter tick, got %d", before)
	}

	clock.Advance(2 * time.Second)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 500)

	if got := countHouseCanonical(col, ""); got != before {
		t.Fatalf("plug update emitted %d new canonical house events; want 0", got-before)
	}
	if got := store.House().Electricity.MonitoredW; got != 500 {
		t.Fatalf("MonitoredW=%v want 500 after plug update", got)
	}
	if got := store.House().Electricity.UnmonitoredW; got != 500 {
		t.Fatalf("UnmonitoredW=%v want 500 (gross 1000 - monitored 500)", got)
	}
}

func TestEngineElectricity_ComputedAtTracksReading(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ts := clock.Now().Add(7 * time.Second)
	ingestMeter(engine, ts, 1000)
	if !store.House().Electricity.ComputedAt.Equal(ts) {
		t.Fatalf("ComputedAt=%v want %v (matches reading timestamp, not engine clock)",
			store.House().Electricity.ComputedAt, ts)
	}
}

func TestEngineElectricity_PerMeterTickEmitsOnce(t *testing.T) {
	engine, _, col, clock := mkElectricityEngine(t)
	for i := 0; i < 5; i++ {
		ingestMeter(engine, clock.Now(), 1000)
		clock.Advance(10 * time.Second)
	}
	if got := countHouseCanonical(col, "gross_w"); got != 5 {
		t.Fatalf("got %d gross_w emits across 5 meter ticks, want 5", got)
	}
}

func TestEngineElectricity_HouseStateUnaffectedByFloatWiggle(t *testing.T) {
	engine, _, col, clock := mkElectricityEngine(t)
	// First meter ingest causes the legitimate unknown -> idle transition;
	// the test cares about wiggles *after* the system has settled.
	ingestMeter(engine, clock.Now(), 900)
	baseline := 0
	for _, ev := range col.derived {
		if ev.Type == model.EvtHouseStateChanged {
			baseline++
		}
	}

	for i := 0; i < 50; i++ {
		clock.Advance(10 * time.Second)
		ingestMeter(engine, clock.Now(), float64(900+i*7))
	}
	got := 0
	for _, ev := range col.derived {
		if ev.Type == model.EvtHouseStateChanged {
			got++
		}
	}
	if got != baseline {
		t.Fatalf("EvtHouseStateChanged fired %d extra times from electricity wiggle", got-baseline)
	}
}

func TestEngineElectricity_StaleDevicesInSummary(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 200)
	ingestPlug(engine, "0x00158d0000000001", "kitchen_dishwasher", clock.Now(), 300)

	clock.Advance(901 * time.Second)
	ingestMeter(engine, clock.Now(), 1000)

	s := store.House().Electricity
	if s.StaleDeviceCount != 2 {
		t.Fatalf("StaleDeviceCount=%d want 2", s.StaleDeviceCount)
	}
	if s.MonitoredW != 0 {
		t.Fatalf("MonitoredW=%v want 0 (both stale)", s.MonitoredW)
	}
	if s.UnmonitoredW != 1000 {
		t.Fatalf("UnmonitoredW=%v want 1000", s.UnmonitoredW)
	}
}

func TestEngineElectricity_CoverageComputed(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 750)

	clock.Advance(5 * time.Second)
	ingestMeter(engine, clock.Now(), 1000)

	if got := store.House().Electricity.Coverage; got != 0.75 {
		t.Fatalf("Coverage=%v want 0.75", got)
	}
}

func TestEngineElectricity_CoverageZeroGross(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 0)
	if got := store.House().Electricity.Coverage; got != 0 {
		t.Fatalf("Coverage=%v want 0 (no NaN, no Inf)", got)
	}
	if math.IsNaN(store.House().Electricity.Coverage) {
		t.Fatalf("Coverage is NaN")
	}
}

func TestEngineElectricity_RepeatedTimestampNoDoubleCount(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ts := clock.Now()
	ingestMeter(engine, ts, 1000)
	ingestMeter(engine, ts, 1000) // same timestamp; integrator skips dt<=0
	if k := store.House().Electricity.Session.GrossKWh; k != 0 {
		t.Fatalf("GrossKWh=%v want 0 (no interval to accrue)", k)
	}
}

func TestEngineElectricity_AdditivityInvariant(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	for i := 0; i < 30; i++ {
		gross := 1000.0 + float64(i*5)
		ingestMeter(engine, clock.Now(), gross)
		ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 100+float64(i))
		ingestPlug(engine, "0x00158d0000000001", "kitchen_dishwasher", clock.Now(), 200+float64(i*2))
		clock.Advance(5 * time.Second)
	}
	clock.Advance(5 * time.Second)
	ingestMeter(engine, clock.Now(), 1500)

	e := store.House().Electricity
	diff := math.Abs(e.Session.GrossKWh - (e.Session.MonitoredKWh + e.Session.UnmonitoredKWh))
	if diff > 1e-9 {
		t.Fatalf("gross != monitored + unmonitored: gross=%v mon=%v un=%v diff=%v",
			e.Session.GrossKWh, e.Session.MonitoredKWh, e.Session.UnmonitoredKWh, diff)
	}
}

func TestEngineElectricity_TrapezoidalSanity(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	// 360 samples × 10s = 3600s = 1h, all at 1000W → 1.000 kWh.
	for i := 0; i < 361; i++ {
		ingestMeter(engine, clock.Now(), 1000)
		clock.Advance(10 * time.Second)
	}
	got := store.House().Electricity.Session.GrossKWh
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("GrossKWh=%v want 1.0 (1000W * 1h)", got)
	}
}

func TestEngineElectricity_FirstReadingNoKWh(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	if k := store.House().Electricity.Session.GrossKWh; k != 0 {
		t.Fatalf("GrossKWh=%v want 0 on first reading", k)
	}
}

func TestEngineElectricity_GapClampSkipsAllThree(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 500)

	clock.Advance(45 * time.Minute) // > 30min cfg.Energy.MaxIntegrationGap

	ingestMeter(engine, clock.Now(), 1000)
	e := store.House().Electricity
	if e.Session.GrossKWh != 0 || e.Session.MonitoredKWh != 0 || e.Session.UnmonitoredKWh != 0 {
		t.Fatalf("expected all integrators clamped across gap; got %+v", e)
	}
}

func TestEngineElectricity_NegativeUnmonitoredExposed(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 800)
	ingestPlug(engine, "0x00158d0000000001", "kitchen_dishwasher", clock.Now(), 500)
	clock.Advance(1 * time.Second)
	ingestMeter(engine, clock.Now(), 1000)
	if got := store.House().Electricity.UnmonitoredW; got != -300 {
		t.Fatalf("UnmonitoredW=%v want -300 (raw, not clamped)", got)
	}
}

func TestEngineElectricity_OfflineMaturesToStale(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 200)

	engine.SetAvailability(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0x00158d0000000009", Display: "kitchen_kettle"},
		"zigbee2mqtt/kitchen_kettle/availability", model.AvailabilityOffline)
	clock.Advance(60 * time.Second)
	engine.SetAvailability(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0x00158d0000000009", Display: "kitchen_kettle"},
		"zigbee2mqtt/kitchen_kettle/availability", model.AvailabilityOffline)

	clock.Advance(1 * time.Second)
	ingestMeter(engine, clock.Now(), 1000)
	s := store.House().Electricity
	if s.StaleDeviceCount != 1 || s.MonitoredW != 0 {
		t.Fatalf("offline plug should be stale; got %+v", s)
	}
}

func TestEngineElectricity_CanonicalValueMatchesSummary(t *testing.T) {
	engine, store, col, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1234)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 234)

	clock.Advance(1 * time.Second)
	ingestMeter(engine, clock.Now(), 1234)

	s := store.House().Electricity
	if got := lastHouseValue(t, col, "gross_w"); got != s.GrossW {
		t.Errorf("gross_w canonical=%v summary=%v", got, s.GrossW)
	}
	if got := lastHouseValue(t, col, "monitored_w"); got != s.MonitoredW {
		t.Errorf("monitored_w canonical=%v summary=%v", got, s.MonitoredW)
	}
	if got := lastHouseValue(t, col, "unmonitored_w"); got != s.UnmonitoredW {
		t.Errorf("unmonitored_w canonical=%v summary=%v", got, s.UnmonitoredW)
	}
	if got := lastHouseValue(t, col, "coverage"); got != s.Coverage {
		t.Errorf("coverage canonical=%v summary=%v", got, s.Coverage)
	}
}

// TestEngineElectricity_NegativeGrossCoverageExposed asserts the
// design contract: when the meter reports export (negative gross —
// SMETS2 with solar PV), Coverage is computed raw rather than clamped.
// The negative ratio is meaningless as a "fraction of consumption"
// but the test pins that the engine doesn't clip the signal.
func TestEngineElectricity_NegativeGrossCoverageExposed(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), -500)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 200)
	clock.Advance(1 * time.Second)
	ingestMeter(engine, clock.Now(), -500)

	e := store.House().Electricity
	if e.Coverage != 200.0/-500.0 {
		t.Fatalf("Coverage=%v want %v (raw, not clamped)", e.Coverage, 200.0/-500.0)
	}
	if e.UnmonitoredW != -500-200 {
		t.Fatalf("UnmonitoredW=%v want -700 (raw)", e.UnmonitoredW)
	}
}

// TestEngineElectricity_MonotonicityGuard_RejectsOlderTimestamp
// verifies the guard in recomputeElectricity: a reading whose
// timestamp is older than the last applied recompute is dropped
// entirely (no integrator advance, no store update). Without this
// guard, the energy.Integrator's dt<=0 branch advances lastAt
// backwards, causing the next legitimate interval to double-count.
func TestEngineElectricity_MonotonicityGuard_RejectsOlderTimestamp(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	beforeTotal := store.House().Electricity.Session.GrossKWh
	beforeComputedAt := store.House().Electricity.ComputedAt

	// Older-than-current reading; should be skipped.
	older := clock.Now().Add(-30 * time.Second)
	ingestMeter(engine, older, 2000)

	after := store.House().Electricity
	if !after.ComputedAt.Equal(beforeComputedAt) {
		t.Fatalf("ComputedAt regressed from %v to %v", beforeComputedAt, after.ComputedAt)
	}
	if after.Session.GrossKWh != beforeTotal {
		t.Fatalf("GrossKWh changed from %v to %v on older reading", beforeTotal, after.Session.GrossKWh)
	}
	if after.GrossW != 1000 {
		t.Fatalf("GrossW=%v; older reading must not overwrite live value", after.GrossW)
	}
}

// TestEngineElectricity_MonotonicityGuard_AdvancesOnNewer is the
// positive case: a reading newer than the last applied recompute is
// integrated normally.
func TestEngineElectricity_MonotonicityGuard_AdvancesOnNewer(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	beforeAt := store.House().Electricity.ComputedAt

	clock.Advance(10 * time.Second)
	ingestMeter(engine, clock.Now(), 1000)

	after := store.House().Electricity
	if !after.ComputedAt.After(beforeAt) {
		t.Fatalf("ComputedAt did not advance: %v -> %v", beforeAt, after.ComputedAt)
	}
	if after.Session.GrossKWh <= 0 {
		t.Fatalf("GrossKWh=%v; expected positive after 10s interval", after.Session.GrossKWh)
	}
}

func TestEngineElectricity_HouseNotInSnapshotDevices(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	if _, ok := store.Snapshot().Devices[HouseDeviceID]; ok {
		t.Fatalf("synthetic %q leaked into Snapshot().Devices", HouseDeviceID)
	}
}

func TestEngineElectricity_HousePowerNotInStore(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	if _, ok := store.Get(HouseDeviceID); ok {
		t.Fatalf("synthetic %q must not be registered in the store", HouseDeviceID)
	}
}

func TestEngineElectricity_ConcurrentIngest(t *testing.T) {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"short_burst_power_device": {
			DefaultThresholds: config.Thresholds{
				IdleBelowW:   ptr(5.0),
				ActiveAboveW: ptr(50.0),
			},
		},
		"energy_meter": {},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"house_meter": {Scheme: "meter", Primary: "abcd1234", Class: "energy_meter"},
		"kitchen_kettle": {
			Scheme: "zigbee", Primary: "0x00158d0000000009",
			Class: "short_burst_power_device",
		},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)
	engine.AddCanonicalSink(&countingSink{})

	start := clock.Now()
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			ingestMeter(engine, start.Add(time.Duration(i)*time.Millisecond), 1000+float64(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle",
				start.Add(time.Duration(i)*time.Millisecond), 50+float64(i%50))
		}
	}()
	wg.Wait()

	s := store.House().Electricity
	if s.Session.GrossKWh < 0 {
		t.Fatalf("GrossKWh negative: %v", s.Session.GrossKWh)
	}
	if s.ComputedAt.IsZero() {
		t.Fatalf("ComputedAt not set after concurrent ingest")
	}
}

func TestEngineElectricity_SnapshotCarriesSummary(t *testing.T) {
	engine, store, _, clock := mkElectricityEngine(t)
	ingestMeter(engine, clock.Now(), 1000)
	ingestPlug(engine, "0x00158d0000000009", "kitchen_kettle", clock.Now(), 200)
	clock.Advance(1 * time.Second)
	ingestMeter(engine, clock.Now(), 1000)

	snap := store.Snapshot()
	if snap.House.Electricity.GrossW != 1000 {
		t.Fatalf("snapshot House.Electricity.GrossW=%v want 1000", snap.House.Electricity.GrossW)
	}
	if snap.House.Electricity.MonitoredW != 200 {
		t.Fatalf("snapshot MonitoredW=%v want 200", snap.House.Electricity.MonitoredW)
	}
}
