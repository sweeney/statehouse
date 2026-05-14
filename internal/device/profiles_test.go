package device

import (
	"testing"

	"github.com/sweeney/statehouse/internal/config"
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
