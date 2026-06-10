package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

var noOverride = func(class string) *int { return nil }

var aggTestNow = time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)

func mkPlug(id, class string, power float64, lastSeen time.Time, avail model.Availability) model.Device {
	p := power
	return model.Device{
		ID:           id,
		Class:        class,
		Availability: avail,
		Latest:       model.Latest{PowerW: &p, LastSeen: lastSeen},
	}
}

func mkMeter(id string, power float64, lastSeen time.Time) model.Device {
	p := power
	return model.Device{
		ID:           id,
		Class:        device.ClassEnergyMeter,
		Availability: model.AvailabilityOnline,
		Latest:       model.Latest{PowerW: &p, LastSeen: lastSeen},
	}
}

func TestAggregate_NoMeter_GrossUnseen(t *testing.T) {
	devs := map[string]model.Device{
		"plug": mkPlug("plug", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.GrossSeen {
		t.Fatalf("GrossSeen=true with no meter")
	}
	if agg.GrossW != 0 {
		t.Fatalf("GrossW=%v want 0", agg.GrossW)
	}
	if agg.MonitoredW != 100 {
		t.Fatalf("MonitoredW=%v want 100 (sums regardless of meter)", agg.MonitoredW)
	}
	if agg.UnmonitoredW != 0 {
		t.Fatalf("UnmonitoredW=%v want 0 (not computed without gross)", agg.UnmonitoredW)
	}
}

func TestAggregate_EmptyMap(t *testing.T) {
	agg := AggregateElectricity(aggTestNow, map[string]model.Device{}, noOverride)
	if agg.GrossSeen {
		t.Fatalf("GrossSeen=true on empty map")
	}
}

func TestAggregate_MeterOnly(t *testing.T) {
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1234.5, aggTestNow),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if !agg.GrossSeen {
		t.Fatalf("GrossSeen=false with meter present")
	}
	if agg.GrossW != 1234.5 {
		t.Fatalf("GrossW=%v want 1234.5", agg.GrossW)
	}
	if agg.MonitoredW != 0 {
		t.Fatalf("MonitoredW=%v want 0", agg.MonitoredW)
	}
	if agg.UnmonitoredW != 1234.5 {
		t.Fatalf("UnmonitoredW=%v want 1234.5", agg.UnmonitoredW)
	}
}

func TestAggregate_SumsPowerClasses(t *testing.T) {
	devs := map[string]model.Device{
		"meter":  mkMeter("meter", 2000, aggTestNow),
		"kettle": mkPlug("kettle", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOnline),
		"dish":   mkPlug("dish", device.ClassCyclePower, 200, aggTestNow, model.AvailabilityOnline),
		"fridge": mkPlug("fridge", device.ClassContinuous, 50, aggTestNow, model.AvailabilityOnline),
		"tv":     mkPlug("tv", device.ClassMedia, 80, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.MonitoredW != 100+200+50+80 {
		t.Fatalf("MonitoredW=%v want 430", agg.MonitoredW)
	}
	if agg.UnmonitoredW != 2000-430 {
		t.Fatalf("UnmonitoredW=%v want 1570", agg.UnmonitoredW)
	}
}

func TestAggregate_ExcludesPassiveAndBinary(t *testing.T) {
	// Environmental sensors, binary-state, and unclassified devices never
	// contribute power even if a stray PowerW is present. (UPS is power-
	// bearing and intentionally counted — see TestAggregate_IncludesUPS.)
	devs := map[string]model.Device{
		"meter":   mkMeter("meter", 1000, aggTestNow),
		"plug":    mkPlug("plug", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOnline),
		"climate": mkPlug("climate", device.ClassEnvironmentalSensor, 999, aggTestNow, model.AvailabilityOnline),
		"contact": mkPlug("contact", device.ClassBinaryState, 999, aggTestNow, model.AvailabilityOnline),
		"other":   mkPlug("other", device.ClassUnclassified, 999, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.MonitoredW != 100 {
		t.Fatalf("MonitoredW=%v want 100 (only plug counted)", agg.MonitoredW)
	}
}

func TestAggregate_IncludesUPS(t *testing.T) {
	// A UPS reports its output load (LoadWatts) as PowerW — real, otherwise
	// unmonitored draw — so it contributes to the monitored sum.
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOnline),
		"ups":   mkPlug("ups", device.ClassUPSSensor, 60, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.MonitoredW != 160 {
		t.Fatalf("MonitoredW=%v want 160 (plug 100 + ups 60)", agg.MonitoredW)
	}
	if agg.UnmonitoredW != 1000-160 {
		t.Fatalf("UnmonitoredW=%v want 840", agg.UnmonitoredW)
	}
}

func TestAggregate_ExcludesMeterFromMonitored(t *testing.T) {
	devs := map[string]model.Device{
		"m1":   mkMeter("m1", 1500, aggTestNow),
		"plug": mkPlug("plug", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.GrossW != 1500 {
		t.Fatalf("GrossW=%v want 1500", agg.GrossW)
	}
	if agg.MonitoredW != 100 {
		t.Fatalf("MonitoredW=%v want 100 (meter must not be summed)", agg.MonitoredW)
	}
}

func TestAggregate_NilPowerSkipped(t *testing.T) {
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug": {
			ID:           "plug",
			Class:        device.ClassShortBurst,
			Availability: model.AvailabilityOnline,
			Latest:       model.Latest{LastSeen: aggTestNow},
		},
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.MonitoredW != 0 {
		t.Fatalf("MonitoredW=%v want 0 (plug had no PowerW)", agg.MonitoredW)
	}
	if len(agg.StaleIDs) != 0 {
		t.Fatalf("StaleIDs=%v; nil-power plug must not be marked stale", agg.StaleIDs)
	}
}

func TestAggregate_PowerClassFreshWithin900s(t *testing.T) {
	lastSeen := aggTestNow.Add(-850 * time.Second)
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, lastSeen, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if len(agg.StaleIDs) != 0 {
		t.Fatalf("plug 850s old should be fresh under 900s threshold: %+v", agg)
	}
	if agg.MonitoredW != 100 {
		t.Fatalf("MonitoredW=%v want 100", agg.MonitoredW)
	}
}

func TestAggregate_PowerClassStaleAfter900s(t *testing.T) {
	lastSeen := aggTestNow.Add(-901 * time.Second)
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, lastSeen, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if len(agg.StaleIDs) != 1 || agg.StaleIDs[0] != "plug" {
		t.Fatalf("plug 901s old should be stale: %+v", agg)
	}
	if agg.MonitoredW != 0 {
		t.Fatalf("MonitoredW=%v want 0 (stale)", agg.MonitoredW)
	}
}

func TestAggregate_StalenessOverrideRespected(t *testing.T) {
	override := 300
	customStaleness := func(class string) *int {
		if class == device.ClassShortBurst {
			return &override
		}
		return nil
	}
	lastSeen := aggTestNow.Add(-301 * time.Second)
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, lastSeen, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, customStaleness)
	if len(agg.StaleIDs) != 1 {
		t.Fatalf("override 300s: device at 301s should be stale: %+v", agg)
	}
}

func TestAggregate_IdleZeroWContributesZero(t *testing.T) {
	// Device at 0W last seen 500s ago (< 900s) — not stale, contributes 0W.
	// No more idle/active split: idle devices are no longer held to a different threshold.
	lastSeen := aggTestNow.Add(-500 * time.Second)
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 0, lastSeen, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if len(agg.StaleIDs) != 0 {
		t.Fatalf("0W plug at 500s is fresh under 900s threshold: %+v", agg)
	}
	if agg.MonitoredW != 0 {
		t.Fatalf("MonitoredW=%v want 0 (plug reports 0W)", agg.MonitoredW)
	}
}

func TestAggregate_ConsistentWithDeviceStalenessForClass(t *testing.T) {
	for _, class := range []string{
		device.ClassShortBurst, device.ClassCyclePower,
		device.ClassContinuous, device.ClassMedia,
	} {
		threshold := device.StalenessSecondsForClass(class, nil)
		freshAgo := time.Duration(threshold-1) * time.Second
		staleAgo := time.Duration(threshold+1) * time.Second

		devs := map[string]model.Device{
			"meter": mkMeter("meter", 1000, aggTestNow),
			"plug":  mkPlug("plug", class, 100, aggTestNow.Add(-freshAgo), model.AvailabilityOnline),
		}
		agg := AggregateElectricity(aggTestNow, devs, noOverride)
		if len(agg.StaleIDs) != 0 {
			t.Errorf("class %q fresh (-%v): expected fresh, got stale", class, freshAgo)
		}

		devs["plug"] = mkPlug("plug", class, 100, aggTestNow.Add(-staleAgo), model.AvailabilityOnline)
		agg = AggregateElectricity(aggTestNow, devs, noOverride)
		if len(agg.StaleIDs) != 1 {
			t.Errorf("class %q stale (-%v): expected stale, got fresh", class, staleAgo)
		}
	}
}

func TestAggregate_OfflineExcluded(t *testing.T) {
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOffline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if len(agg.StaleIDs) != 1 {
		t.Fatalf("offline plug must be stale regardless of LastSeen: %+v", agg)
	}
	if agg.MonitoredW != 0 {
		t.Fatalf("MonitoredW=%v want 0", agg.MonitoredW)
	}
}

func TestAggregate_OfflinePendingDefersToStaleness_Fresh(t *testing.T) {
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, aggTestNow, model.AvailabilityOfflinePending),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if len(agg.StaleIDs) != 0 {
		t.Fatalf("offline_pending with fresh LastSeen should count: %+v", agg)
	}
	if agg.MonitoredW != 100 {
		t.Fatalf("MonitoredW=%v want 100", agg.MonitoredW)
	}
}

func TestAggregate_OfflinePendingDefersToStaleness_Old(t *testing.T) {
	// 16 minutes = 960s > 900s threshold → stale
	old := aggTestNow.Add(-16 * time.Minute)
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 100, old, model.AvailabilityOfflinePending),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if len(agg.StaleIDs) != 1 {
		t.Fatalf("offline_pending with old LastSeen should be stale: %+v", agg)
	}
}

func TestAggregate_StaleIDsSorted(t *testing.T) {
	// 16 minutes = 960s > 900s threshold → stale
	old := aggTestNow.Add(-16 * time.Minute)
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 1000, aggTestNow),
		"zzz":   mkPlug("zzz", device.ClassShortBurst, 100, old, model.AvailabilityOnline),
		"aaa":   mkPlug("aaa", device.ClassShortBurst, 100, old, model.AvailabilityOnline),
		"mmm":   mkPlug("mmm", device.ClassShortBurst, 100, old, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	want := []string{"aaa", "mmm", "zzz"}
	if len(agg.StaleIDs) != 3 {
		t.Fatalf("StaleIDs len=%v want 3", len(agg.StaleIDs))
	}
	for i, id := range want {
		if agg.StaleIDs[i] != id {
			t.Fatalf("StaleIDs[%d]=%q want %q (must be sorted)", i, agg.StaleIDs[i], id)
		}
	}
}

func TestAggregate_NegativeUnmonitored_NoClamp(t *testing.T) {
	devs := map[string]model.Device{
		"meter":  mkMeter("meter", 1200, aggTestNow),
		"kettle": mkPlug("kettle", device.ClassShortBurst, 1000, aggTestNow, model.AvailabilityOnline),
		"dish":   mkPlug("dish", device.ClassCyclePower, 500, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.UnmonitoredW != 1200-1500 {
		t.Fatalf("UnmonitoredW=%v want -300 (not clamped)", agg.UnmonitoredW)
	}
}

// TestAggregate_DetectsMeterByScheme verifies the scheme-based meter
// detection fallback: an adapter-classified device with scheme="meter"
// and Latest.PowerW set drives gross even when ClassEnergyMeter is not
// configured via user YAML.
func TestAggregate_DetectsMeterByScheme(t *testing.T) {
	p := 1500.0
	devs := map[string]model.Device{
		"meter": {
			ID:           "meter",
			Class:        device.ClassUnclassified,
			Identity:     model.DeviceIdentity{Scheme: "meter", Primary: "abc", Display: "abc"},
			Availability: model.AvailabilityOnline,
			Latest:       model.Latest{PowerW: &p, LastSeen: aggTestNow},
		},
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if !agg.GrossSeen {
		t.Fatalf("expected scheme=meter to drive gross even when class is unclassified")
	}
	if agg.GrossW != 1500 {
		t.Fatalf("GrossW=%v want 1500", agg.GrossW)
	}
}

// TestAggregate_SchemeMeterWithoutPowerExcluded asserts that a Glow TH
// sensor (also scheme=meter but no PowerW) is not mistaken for an
// electricity meter, so HumidityPct/TemperatureC-only payloads cannot
// fabricate a zero-gross signal.
func TestAggregate_SchemeMeterWithoutPowerExcluded(t *testing.T) {
	temp := 21.5
	devs := map[string]model.Device{
		"th_sensor": {
			ID:           "th_sensor",
			Class:        device.ClassUnclassified,
			Identity:     model.DeviceIdentity{Scheme: "meter", Primary: "th_serial", Display: "th_serial"},
			Availability: model.AvailabilityOnline,
			Latest:       model.Latest{TemperatureC: &temp, LastSeen: aggTestNow},
		},
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.GrossSeen {
		t.Fatalf("scheme=meter without PowerW must not be treated as a meter")
	}
}

// TestAggregate_CoverageMayExceedOne is the explicit contract check
// that downstream consumers can rely on raw Coverage being passed
// through, even when monitored briefly outruns gross. Coverage is
// computed by the engine layer; here we assert UnmonitoredW reflects
// the raw, non-clamped arithmetic that drives that downstream value.
func TestAggregate_CoverageMayExceedOne(t *testing.T) {
	devs := map[string]model.Device{
		"meter":  mkMeter("meter", 1000, aggTestNow),
		"plug_a": mkPlug("plug_a", device.ClassShortBurst, 700, aggTestNow, model.AvailabilityOnline),
		"plug_b": mkPlug("plug_b", device.ClassShortBurst, 500, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if agg.MonitoredW != 1200 {
		t.Fatalf("MonitoredW=%v want 1200", agg.MonitoredW)
	}
	if agg.UnmonitoredW != -200 {
		t.Fatalf("UnmonitoredW=%v want -200 (raw)", agg.UnmonitoredW)
	}
}

// TestAggregate_MultipleMetersDeterministic asserts that when two
// meter devices are present (e.g. a misconfigured second energy_meter),
// the choice of gross is the lowest device id rather than whatever
// happens to be first in the map iteration. A two-meter misconfig
// should surface as a stable wrong number, not a flickering one.
func TestAggregate_MultipleMetersDeterministic(t *testing.T) {
	p1, p2 := 1000.0, 2000.0
	devs := map[string]model.Device{
		"zzz_meter": {
			ID:           "zzz_meter",
			Class:        device.ClassEnergyMeter,
			Availability: model.AvailabilityOnline,
			Latest:       model.Latest{PowerW: &p2, LastSeen: aggTestNow},
		},
		"aaa_meter": {
			ID:           "aaa_meter",
			Class:        device.ClassEnergyMeter,
			Availability: model.AvailabilityOnline,
			Latest:       model.Latest{PowerW: &p1, LastSeen: aggTestNow},
		},
	}
	// Run repeatedly so any nondeterminism from map iteration order
	// is caught.
	for i := 0; i < 50; i++ {
		agg := AggregateElectricity(aggTestNow, devs, noOverride)
		if agg.GrossW != 1000 {
			t.Fatalf("iter %d: GrossW=%v want 1000 (aaa_meter, lowest id)", i, agg.GrossW)
		}
	}
}

func TestAggregate_ZeroGross(t *testing.T) {
	devs := map[string]model.Device{
		"meter": mkMeter("meter", 0, aggTestNow),
		"plug":  mkPlug("plug", device.ClassShortBurst, 0, aggTestNow, model.AvailabilityOnline),
	}
	agg := AggregateElectricity(aggTestNow, devs, noOverride)
	if !agg.GrossSeen {
		t.Fatalf("GrossSeen=false but meter ingested 0W")
	}
	if agg.UnmonitoredW != 0 {
		t.Fatalf("UnmonitoredW=%v want 0", agg.UnmonitoredW)
	}
}
