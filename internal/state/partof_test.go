package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// partOfCfg is a hot water cylinder with one probe. The cylinder always declares its
// room; the probe's room and part_of are the arguments, empty meaning "the namespace
// record does not declare this key".
func partOfCfg(probeRoom, partOf string) config.Config {
	off := false
	cfg := config.Default()
	cfg.Devices = map[string]config.DeviceConfig{
		"hot-water-cylinder": {
			Class: "hot_water_cylinder", Room: "first.cupboard", Floor: "first",
			Telemetry: &off,
		},
		"hot-water-cylinder-top": {
			Scheme: "tanktemp", Primary: "hot-water-cylinder.top",
			Class: "appliance_probe", Room: probeRoom, PartOf: partOf,
		},
	}
	return cfg
}

var probeIdentity = model.DeviceIdentity{Scheme: "tanktemp", Primary: "hot-water-cylinder.top"}

// part_of has to reach the stored device record, because that record is what both
// /state and /config/devices serve from (see ProfiledDevice).
func TestDiscoveryCarriesPartOf(t *testing.T) {
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	eng := NewEngine(partOfCfg("first.cupboard", "hot-water-cylinder"), store, clock)

	id := eng.EnsureDiscovered(probeIdentity, "test")

	d, ok := store.Get(id)
	if !ok {
		t.Fatalf("device %q not stored", id)
	}
	if d.PartOf != "hot-water-cylinder" {
		t.Errorf("PartOf = %q, want hot-water-cylinder", d.PartOf)
	}
}

// part_of reaches a running service the way room does: the namespace is republished
// and SIGHUP reloads it. So it has to follow room through Upsert's merge, both ways —
// a newly declared value is picked up, and a record that stops declaring it keeps the
// last known value ("not republished", not "no longer part of anything"). That is the
// same rule as room, floor and covers, and the same rule keeps /state and
// /config/devices in agreement after a reload.
func TestReloadConfigPropagatesPartOfAndKeepsItWhenAbsent(t *testing.T) {
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	eng := NewEngine(partOfCfg("first.cupboard", ""), store, clock)
	id := eng.EnsureDiscovered(probeIdentity, "test")

	eng.ReloadConfig(partOfCfg("first.cupboard", "hot-water-cylinder"))
	if d, _ := store.Get(id); d.PartOf != "hot-water-cylinder" {
		t.Errorf("PartOf = %q after reload, want hot-water-cylinder", d.PartOf)
	}

	eng.ReloadConfig(partOfCfg("first.cupboard", ""))
	pd, ok := store.GetProfiled(id)
	if !ok {
		t.Fatalf("device %q has no paired record after reload", id)
	}
	if pd.Device.PartOf != "hot-water-cylinder" {
		t.Errorf("PartOf = %q, want the previous value to survive an absent one", pd.Device.PartOf)
	}
}

// The decision on #83, at the store: a probe that declares part_of but no room has no
// room in statehouse, even though its appliance declares one. Probes declare their own
// room. Nothing in discovery or reload may fill it from the parent.
func TestProbeWithoutARoomDoesNotInheritItsAppliancesRoom(t *testing.T) {
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	eng := NewEngine(partOfCfg("", "hot-water-cylinder"), store, clock)
	id := eng.EnsureDiscovered(probeIdentity, "test")
	// A reload runs EnsureDiscovered again for every known device: a second chance to
	// get this wrong, so assert after it as well.
	eng.ReloadConfig(partOfCfg("", "hot-water-cylinder"))

	d, ok := store.Get(id)
	if !ok {
		t.Fatalf("device %q not stored", id)
	}
	if d.PartOf != "hot-water-cylinder" {
		t.Fatalf("PartOf = %q; the probe did not resolve to its config entry", d.PartOf)
	}
	if d.Place() != "" || d.Floor != "" {
		t.Errorf("room = %q, floor = %q; want both empty: a probe does not inherit its appliance's placement",
			d.Place(), d.Floor)
	}
}
