package device

import (
	"testing"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/energy"
	"github.com/sweeney/statehouse/internal/model"
)

// TestClassifyByHints_LongerHintWins verifies that when two classes both
// match the same device name, the one with the longer hint wins
// deterministically regardless of map iteration order.
func TestClassifyByHints_LongerHintWins(t *testing.T) {
	cfg := config.Config{
		DeviceClasses: map[string]config.DeviceClassConfig{
			"tv_class": {
				NameHints: []string{"tv"},
			},
			"kettle_class": {
				NameHints: []string{"kettle"},
			},
		},
	}
	r := NewResolver(cfg)

	identity := model.DeviceIdentity{
		Scheme:  "zigbee",
		Primary: "0xaabbccdd",
		Display: "office_tv_kettle",
	}

	// Run 50 times to catch any non-determinism from map iteration.
	const runs = 50
	for i := 0; i < runs; i++ {
		p := r.Resolve(identity)
		if p.Class != "kettle_class" {
			t.Fatalf("run %d: expected 'kettle_class' (longer hint wins), got %q", i+1, p.Class)
		}
	}
}

// TestFireAlarmIsPassiveSensor verifies the fire_alarm class is treated
// as a measurement-only passive sensor — the thin-alias contract that
// keeps it off the activity/occupancy/staleness machinery and aligned
// with environmental_sensor. binary_state_device must stay non-passive.
func TestFireAlarmIsPassiveSensor(t *testing.T) {
	if !IsPassiveSensor(ClassFireAlarm) {
		t.Fatalf("fire_alarm must be a passive sensor")
	}
	if IsPassiveSensor(ClassBinaryState) {
		t.Fatalf("binary_state_device must not be passive (it feeds occupancy)")
	}
}

// TestResolve_PerDeviceEnergyStrategyOverride verifies that a device
// with an explicit energy_strategy field wins over its class default.
// This exists for devices whose hardware counters tick at too coarse a
// resolution (e.g. 100 Wh) for their typical cycle size (20–30 Wh),
// causing a stale_counter warning every cycle. Setting energy_strategy:
// integration on the specific device fixes it without changing the class.
func TestResolve_PerDeviceEnergyStrategyOverride(t *testing.T) {
	cfg := config.Config{
		DeviceClasses: map[string]config.DeviceClassConfig{
			"cycle_power_device": {
				EnergyStrategy: "counter",
			},
		},
		Devices: map[string]config.DeviceConfig{
			"officeheater": {
				Scheme:         "zigbee",
				Primary:        "0xaabbccdd",
				Class:          "cycle_power_device",
				EnergyStrategy: "integration",
			},
		},
	}
	r := NewResolver(cfg)
	p := r.Resolve(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xaabbccdd"})
	if p.Strategy != energy.StrategyIntegration {
		t.Fatalf("expected StrategyIntegration (per-device override), got %v", p.Strategy)
	}
}

// TestResolve_ClassStrategyUsedWhenNoDeviceOverride verifies that
// without a per-device energy_strategy the class default is used.
func TestResolve_ClassStrategyUsedWhenNoDeviceOverride(t *testing.T) {
	cfg := config.Config{
		DeviceClasses: map[string]config.DeviceClassConfig{
			"cycle_power_device": {
				EnergyStrategy: "counter",
			},
		},
		Devices: map[string]config.DeviceConfig{
			"dishwasher": {
				Scheme:  "zigbee",
				Primary: "0x11223344",
				Class:   "cycle_power_device",
			},
		},
	}
	r := NewResolver(cfg)
	p := r.Resolve(model.DeviceIdentity{Scheme: "zigbee", Primary: "0x11223344"})
	if p.Strategy != energy.StrategyCounter {
		t.Fatalf("expected StrategyCounter (class default), got %v", p.Strategy)
	}
}

// TestMergeThresholds_ZeroOverrideHonoured verifies that an explicit
// zero in the override is applied rather than falling back to the base.
func TestMergeThresholds_ZeroOverrideHonoured(t *testing.T) {
	base := config.Thresholds{
		IdleBelowW: ptrF(5.0),
	}
	override := config.Thresholds{
		IdleBelowW: ptrF(0.0),
	}
	merged := mergeThresholds(base, override)
	if merged.IdleBelowW == nil || *merged.IdleBelowW != 0.0 {
		t.Fatalf("expected merged IdleBelowW=0.0, got %v", merged.IdleBelowW)
	}
}
