package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// probeIdentity is the tanktemp probe every scenario below discovers.
var probeIdentity = model.DeviceIdentity{Scheme: "tanktemp", Primary: "hot-water-cylinder.top"}

// ns is one published devices namespace, reduced to what these scenarios vary. The
// probe entry is present unless noProbe; the parents are present when named in
// parents, each with its own room and floor.
type ns struct {
	probeRoom, probeFloor, partOf string
	noProbe                       bool
	parents                       map[string][2]string // id -> {room, floor}
}

func (n ns) config() config.Config {
	cfg := config.Default()
	cfg.Devices = map[string]config.DeviceConfig{}
	for id, place := range n.parents {
		cfg.Devices[id] = config.DeviceConfig{
			Class: "hot_water_cylinder", Room: place[0], Floor: place[1],
			Telemetry: testutil.Ptr(false),
		}
	}
	if !n.noProbe {
		cfg.Devices["hot-water-cylinder-top"] = config.DeviceConfig{
			Scheme: probeIdentity.Scheme, Primary: probeIdentity.Primary,
			Class: "appliance_probe", Room: n.probeRoom, Floor: n.probeFloor, PartOf: n.partOf,
		}
	}
	return cfg
}

var (
	cylinder = map[string][2]string{"hot-water-cylinder": {"first.cupboard", "first"}}
	twoTanks = map[string][2]string{
		"hot-water-cylinder": {"first.cupboard", "first"},
		"buffer-tank":        {"ground.utility", "ground"},
	}
)

// expect is what the stored device record must say after a step. It is what both
// /state and /config/devices serve (see ProfiledDevice).
type expect struct{ partOf, room, floor string }

// The device is discovered under the first namespace; each later one is applied with
// ReloadConfig, which is what SIGHUP does. After every step the stored record is
// checked. A new "weird production" scenario is one row.
//
// The rule being pinned: part_of is relayed and merged like room, floor and covers
// (a new value replaces, an absent one keeps the last), and room is never filled in
// from the parent at any step (#83).
func TestPartOfAcrossDiscoveryAndReloads(t *testing.T) {
	cases := []struct {
		name  string
		steps []ns
		want  []expect // one per step
	}{
		{
			name:  "declared at discovery",
			steps: []ns{{probeRoom: "first.cupboard", probeFloor: "first", partOf: "hot-water-cylinder", parents: cylinder}},
			want:  []expect{{"hot-water-cylinder", "first.cupboard", "first"}},
		},
		{
			// Most devices: nothing declared, nothing relayed.
			name:  "never declared",
			steps: []ns{{probeRoom: "first.cupboard", parents: cylinder}},
			want:  []expect{{"", "first.cupboard", ""}},
		},
		{
			// The namespace learns about the probe's appliance after the probe has
			// already been discovered and is reporting.
			name: "declared by a later reload",
			steps: []ns{
				{probeRoom: "first.cupboard", parents: cylinder},
				{probeRoom: "first.cupboard", partOf: "hot-water-cylinder", parents: cylinder},
			},
			want: []expect{{"", "first.cupboard", ""}, {"hot-water-cylinder", "first.cupboard", ""}},
		},
		{
			// The probe is reporting before its namespace record exists at all
			// (adapter enabled first, namespace published second).
			name: "probe discovered before its namespace record exists",
			steps: []ns{
				{noProbe: true, parents: cylinder},
				{probeRoom: "first.cupboard", probeFloor: "first", partOf: "hot-water-cylinder", parents: cylinder},
			},
			want: []expect{{"", "", ""}, {"hot-water-cylinder", "first.cupboard", "first"}},
		},
		{
			// The probe is moved to another appliance: the new value replaces.
			name: "moved from one appliance to another",
			steps: []ns{
				{probeRoom: "first.cupboard", partOf: "hot-water-cylinder", parents: twoTanks},
				{probeRoom: "ground.utility", partOf: "buffer-tank", parents: twoTanks},
			},
			want: []expect{{"hot-water-cylinder", "first.cupboard", ""}, {"buffer-tank", "ground.utility", ""}},
		},
		{
			// "Not republished", not "no longer part of anything": the sticky rule.
			name: "a republish that drops part_of keeps the last value",
			steps: []ns{
				{probeRoom: "first.cupboard", partOf: "hot-water-cylinder", parents: cylinder},
				{probeRoom: "first.cupboard", parents: cylinder},
			},
			want: []expect{{"hot-water-cylinder", "first.cupboard", ""}, {"hot-water-cylinder", "first.cupboard", ""}},
		},
		{
			// The parent is removed from the namespace. Statehouse relays part_of,
			// it does not resolve it, so the probe keeps relaying the id, now
			// dangling. devices.Check warns part_of_unresolved for exactly this
			// (asserted in internal/config); statehouse does not second-guess it.
			name: "parent removed from the namespace",
			steps: []ns{
				{probeRoom: "first.cupboard", partOf: "hot-water-cylinder", parents: cylinder},
				{probeRoom: "first.cupboard", partOf: "hot-water-cylinder"},
			},
			want: []expect{{"hot-water-cylinder", "first.cupboard", ""}, {"hot-water-cylinder", "first.cupboard", ""}},
		},
		{
			// The decision on #83. The parent has a room; the probe declares none.
			// devices.PlaceOf would give it first.cupboard; statehouse gives nothing,
			// at discovery and again at the re-resolution every reload runs.
			name: "probe without a room does not inherit its appliance's",
			steps: []ns{
				{partOf: "hot-water-cylinder", parents: cylinder},
				{partOf: "hot-water-cylinder", parents: cylinder},
			},
			want: []expect{{"hot-water-cylinder", "", ""}, {"hot-water-cylinder", "", ""}},
		},
		{
			// A republish that drops the probe's room keeps the old room (sticky),
			// even though the probe is part_of a roomed parent. That kept room is
			// the probe's own last declared one, not an inherited one: nothing
			// changes when the parent's room differs, as it does here.
			name: "dropped room keeps the probe's own last room, not the parent's",
			steps: []ns{
				{probeRoom: "first.landing", partOf: "hot-water-cylinder", parents: cylinder},
				{partOf: "hot-water-cylinder", parents: cylinder},
			},
			want: []expect{{"hot-water-cylinder", "first.landing", ""}, {"hot-water-cylinder", "first.landing", ""}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.want) != len(tc.steps) {
				t.Fatalf("bad row: %d steps, %d expectations", len(tc.steps), len(tc.want))
			}
			store := NewStore()
			clock := testutil.NewFakeClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
			eng := NewEngine(tc.steps[0].config(), store, clock)
			id := eng.EnsureDiscovered(probeIdentity, "test")

			for i, step := range tc.steps {
				if i > 0 {
					eng.ReloadConfig(step.config())
				}
				pd, ok := store.GetProfiled(id)
				if !ok {
					t.Fatalf("step %d: device %q has no paired record", i, id)
				}
				d, w := pd.Device, tc.want[i]
				if d.PartOf != w.partOf || d.Place() != w.room || d.Floor != w.floor {
					t.Errorf("step %d: part_of=%q room=%q floor=%q, want part_of=%q room=%q floor=%q",
						i, d.PartOf, d.Place(), d.Floor, w.partOf, w.room, w.floor)
				}
			}
		})
	}
}
