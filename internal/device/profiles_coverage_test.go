package device

import (
	"testing"

	"github.com/sweeney/statehouse/internal/config"
)

// Coverage is resolved once, where config is turned into a Profile, so every reader
// downstream sees a Covers that is already correct instead of having to remember the
// legacy `location: house` spelling itself.
func TestProfileCarriesResolvedCoverage(t *testing.T) {
	for name, tc := range map[string]struct {
		in   config.DeviceConfig
		want string
	}{
		"legacy house location":     {config.DeviceConfig{Class: "energy_meter", Location: "house"}, "house"},
		"explicit covers":           {config.DeviceConfig{Class: "energy_meter", Room: "basement.hallway", Covers: "house"}, "house"},
		"room published first":      {config.DeviceConfig{Class: "energy_meter", Room: "basement.hallway", Location: "house"}, "house"},
		"ordinary device":           {config.DeviceConfig{Class: "continuous_power_device", Location: "kitchen"}, ""},
		"ordinary device with room": {config.DeviceConfig{Class: "continuous_power_device", Room: "groundfloor.kitchen"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			p := profileFromOverride(tc.in, nil)
			if p.Covers != tc.want {
				t.Errorf("Profile.Covers = %q, want %q", p.Covers, tc.want)
			}
		})
	}
}
