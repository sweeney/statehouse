package device

import (
	"testing"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// part_of (github.com/sweeney/common/devices v0.2.0) names the appliance a probe is
// inside: a cylinder probe's part_of names the cylinder, a fridge probe's names the
// fridge. Statehouse relays it (#83), so it has to survive the first hop, where config
// is turned into a Profile.
func TestProfileCarriesPartOf(t *testing.T) {
	p := profileFromOverride(config.DeviceConfig{
		Class: ClassApplianceProbe, Room: "first.cupboard", PartOf: "hot-water-cylinder",
	}, nil)
	if p.PartOf != "hot-water-cylinder" {
		t.Errorf("Profile.PartOf = %q, want hot-water-cylinder", p.PartOf)
	}
}

// A device that is not part of anything has no part_of. Pinned so an empty value
// stays empty rather than being filled from somewhere else.
func TestProfileOmitsPartOfWhenNotDeclared(t *testing.T) {
	p := profileFromOverride(config.DeviceConfig{
		Class: "cycle_power_device", Room: "ground.kitchen",
	}, nil)
	if p.PartOf != "" {
		t.Errorf("Profile.PartOf = %q, want empty", p.PartOf)
	}
}

// The decision on #83: a probe does NOT inherit its appliance's room. A probe declares
// its own room or has none. devices.PlaceOf would hand this probe the cylinder's
// room; statehouse must not, because room is relayed and never derived (CLAUDE.md).
//
// This goes through the real Resolver with the parent in the same config. That is the
// only place inheritance could happen, since it is the only place that sees both
// records.
func TestResolveDoesNotInheritRoomThroughPartOf(t *testing.T) {
	cfg := config.Config{
		Devices: map[string]config.DeviceConfig{
			// The appliance. telemetry: false, no identity: it sends nothing itself.
			"hot-water-cylinder": {
				Class: "hot_water_cylinder", Room: "first.cupboard", Floor: "first",
				Telemetry: testutil.Ptr(false),
			},
			// The probe declares part_of but no room or floor.
			"hot-water-cylinder-top": {
				Class: ClassApplianceProbe, Scheme: "tanktemp",
				Primary: "hot-water-cylinder.top", PartOf: "hot-water-cylinder",
			},
		},
	}
	p := NewResolver(cfg).Resolve(model.DeviceIdentity{
		Scheme: "tanktemp", Primary: "hot-water-cylinder.top",
	})

	if p.PartOf != "hot-water-cylinder" {
		t.Fatalf("PartOf = %q; the probe did not resolve to its config entry", p.PartOf)
	}
	if p.Room != "" || p.Place() != "" {
		t.Errorf("Room = %q, Place() = %q; want both empty: a probe does not inherit its appliance's room",
			p.Room, p.Place())
	}
	if p.Floor != "" {
		t.Errorf("Floor = %q; want empty: floor is not inherited either", p.Floor)
	}
}
