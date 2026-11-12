package config

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/sweeney/common/devices"
	"github.com/sweeney/statehouse/internal/testutil"
)

// #83 decided that a probe does not inherit its appliance's room through part_of.
// devices.Check does not flag a probe with no room, because in the shared vocabulary
// that is legal: devices.PlaceOf fills it from the parent. So a namespace written for
// PlaceOf-reading consumers is valid there and silently roomless here. Statehouse
// therefore reports that case itself, as advisory, the same way it reports
// devices.Check problems: logged, never refused.

// noTelemetry is the `telemetry: false` an appliance entry (a cylinder, a fridge)
// carries: it sends nothing itself, so it has no identity.
var noTelemetry = testutil.Ptr(false)

// probe is an appliance_probe with an identity and the given part_of. Placement is
// set by the caller, so a row reads as "what this record declares".
func probe(primary, partOf string) DeviceConfig {
	return DeviceConfig{Scheme: "tanktemp", Primary: primary, Class: "appliance_probe", PartOf: partOf}
}

func withRoom(d DeviceConfig, room, floor string) DeviceConfig {
	d.Room, d.Floor = room, floor
	return d
}

func withLocation(d DeviceConfig, location string) DeviceConfig {
	d.Location = location
	return d
}

func appliance(room, floor string) DeviceConfig {
	return DeviceConfig{Class: "hot_water_cylinder", Room: room, Floor: floor, Telemetry: noTelemetry}
}

// Every row is its own document, so a case cannot lean on another's devices and a
// failure names the scenario. A new corner case is one row. want lists the problems
// expected, in order, with their exact messages: the message is the operator's whole
// instruction, so it is part of the contract.
func TestPartOfRoomNotInheritedProblems(t *testing.T) {
	type want struct{ id, message string }
	cases := []struct {
		name string
		devs map[string]DeviceConfig
		want []want
	}{
		{
			// The case the check exists for: PlaceOf would give the probe a room and
			// statehouse will not. The message names the room AND floor to add,
			// since the docs ask a probe to declare both.
			name: "probe has no room, parent has room and floor",
			devs: map[string]DeviceConfig{
				"cyl": appliance("first.cupboard", "first"),
				"top": probe("cyl.top", "cyl"),
			},
			want: []want{{"top", `declares no room; its part_of "cyl" is in "first.cupboard", but statehouse does not inherit room through part_of: add "room": "first.cupboard", "floor": "first"`}},
		},
		{
			// Floor is optional in the namespace. Suggest only what the parent has,
			// never an empty "floor": "".
			name: "parent has room but no floor",
			devs: map[string]DeviceConfig{
				"cyl": appliance("first.cupboard", ""),
				"top": probe("cyl.top", "cyl"),
			},
			want: []want{{"top", `declares no room; its part_of "cyl" is in "first.cupboard", but statehouse does not inherit room through part_of: add "room": "first.cupboard"`}},
		},
		{
			// PlaceOf falls back to the parent's deprecated location, so the answers
			// still differ and it is still reported. But a free-text location is not
			// a "<floor>.<room>" id, so suggesting it as a room would trade this
			// warning for device.room_malformed. The fix is to migrate the parent.
			name: "parent has only a legacy location",
			devs: map[string]DeviceConfig{
				"fridge": withLocation(DeviceConfig{Class: "fridge", Telemetry: noTelemetry}, "kitchen"),
				"probe":  probe("fridge", "fridge"),
			},
			want: []want{{"probe", `declares no room; its part_of "fridge" has only the deprecated location "kitchen", which is not a room id and is not inherited: give "fridge" a room, then declare the same room here`}},
		},
		{
			// location: house is a coverage scope, not a place: Place() is "", so
			// PlaceOf gives "" too. The answers agree; nothing to report.
			name: "parent location is house",
			devs: map[string]DeviceConfig{
				"meter": withLocation(DeviceConfig{Class: "energy_meter", Telemetry: noTelemetry}, CoverageHouse),
				"probe": probe("m", "meter"),
			},
		},
		{
			name: "probe declares its own room",
			devs: map[string]DeviceConfig{
				"cyl": appliance("first.cupboard", "first"),
				"top": withRoom(probe("cyl.top", "cyl"), "first.cupboard", "first"),
			},
		},
		{
			// The legacy location still counts as a declared place (Place()).
			name: "probe declares a legacy location",
			devs: map[string]DeviceConfig{
				"cyl": appliance("first.cupboard", "first"),
				"top": withLocation(probe("cyl.top", "cyl"), "cupboard"),
			},
		},
		{
			// The parent has no place either, so PlaceOf gives "" and the answers
			// agree. Nothing would be inherited.
			name: "parent has no place",
			devs: map[string]DeviceConfig{
				"fridge": {Class: "fridge", Telemetry: noTelemetry},
				"probe":  probe("f", "fridge"),
			},
		},
		{
			// devices.Check reports part_of_unresolved; a second line is noise.
			name: "part_of names no device",
			devs: map[string]DeviceConfig{"probe": probe("p", "nowhere")},
		},
		{
			// devices.Check reports part_of_chain. Covered by the chain test below,
			// since the parent is the device itself and it is part_of something.
			name: "part_of names itself",
			devs: map[string]DeviceConfig{"loop": probe("l", "loop")},
		},
		{
			// a -> b -> c with b in a room. PlaceOf is one level and gives a "" (b
			// is itself part_of), and devices.Check reports part_of_chain. Nothing
			// from here, or the chain is reported twice.
			name: "part_of chain",
			devs: map[string]DeviceConfig{
				"c": appliance("ground.kitchen", "ground"),
				"b": withRoom(probe("b", "c"), "ground.kitchen", "ground"),
				"a": probe("a", "b"),
			},
		},
		{
			name: "no part_of and no room is ordinary unknown placement",
			devs: map[string]DeviceConfig{"plain": {Scheme: "zigbee", Primary: "0x01", Class: "cycle_power_device"}},
		},
		{
			// Device-id order, like devices.Check's, so the log is stable.
			name: "several probes come back in id order",
			devs: map[string]DeviceConfig{
				"fridge": appliance("ground.kitchen", ""),
				"z":      probe("z", "fridge"),
				"a":      probe("a", "fridge"),
				"m":      probe("m", "fridge"),
			},
			want: []want{
				{"a", `declares no room; its part_of "fridge" is in "ground.kitchen", but statehouse does not inherit room through part_of: add "room": "ground.kitchen"`},
				{"m", `declares no room; its part_of "fridge" is in "ground.kitchen", but statehouse does not inherit room through part_of: add "room": "ground.kitchen"`},
				{"z", `declares no room; its part_of "fridge" is in "ground.kitchen", but statehouse does not inherit room through part_of: add "room": "ground.kitchen"`},
			},
		},
		{name: "nil map", devs: nil},
		{name: "empty map", devs: map[string]DeviceConfig{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PartOfRoomNotInheritedProblems(tc.devs)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d problems, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				p := got[i]
				if p.ID != w.id || p.Code != CodePartOfRoomNotInherited || p.Field != "room" {
					t.Errorf("problem %d = %s/%s/%s, want %s/%s/room", i, p.ID, p.Code, p.Field, w.id, CodePartOfRoomNotInherited)
				}
				if p.Message != w.message {
					t.Errorf("problem %d message\n got: %s\nwant: %s", i, p.Message, w.message)
				}
			}
		})
	}
}

// The checks never change the document they are given: they are advisory.
func TestPartOfRoomNotInheritedProblemsDoesNotChangeTheMap(t *testing.T) {
	devs := map[string]DeviceConfig{
		"cyl": appliance("first.cupboard", "first"),
		"top": probe("cyl.top", "cyl"),
	}
	PartOfRoomNotInheritedProblems(devs)
	if devs["top"].Room != "" || devs["top"].Floor != "" {
		t.Errorf("top = %+v; the check filled in placement", devs["top"])
	}
}

// The default list is what production runs. Pinned so dropping either check is a
// visible change, not a quiet one.
func TestDefaultDeviceChecksRunBothTheSharedAndStatehouseChecks(t *testing.T) {
	devs := map[string]DeviceConfig{
		"cyl":    appliance("first.cupboard", "first"),
		"top":    probe("cyl.top", "cyl"), // statehouse: room not inherited
		"orphan": probe("o", "nowhere"),   // shared: part_of unresolved
	}
	codes := map[string]bool{}
	for _, check := range DefaultDeviceChecks {
		for _, p := range check(devs) {
			codes[p.Code] = true
		}
	}
	for _, c := range []string{CodePartOfRoomNotInherited, devices.CodePartOfUnresolved} {
		if !codes[c] {
			t.Errorf("default checks did not report %s; got %v", c, codes)
		}
	}
}

// recordingHandler keeps every slog record, so a test can assert on structured
// attributes rather than on a substring of formatted text.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// problemLines returns the attributes of every "devices problem" warning, in order.
func (h *recordingHandler) problemLines() []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]string
	for _, r := range h.records {
		if r.Message != "remote config: devices problem" {
			continue
		}
		attrs := map[string]string{"level": r.Level.String()}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// fetchDevices serves doc as devices_test (with empty classes and behaviour) and
// applies it through the real ApplyRemote with the given checks.
func fetchDevices(t *testing.T, doc map[string]any, checks []DeviceCheck) (Config, *recordingHandler) {
	t.Helper()
	mux := http.NewServeMux()
	serveNamespace(mux, "devices_test", doc)
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})

	rec := &recordingHandler{}
	f := newTestFetcher(t, mux)
	f.Logger = slog.New(rec)
	f.DeviceChecks = checks

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	if err := f.ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote: %v", err)
	}
	return cfg, rec
}

// The wiring, independent of what any one check means: every problem an injected
// check returns is logged once, as a warning, with its namespace, device, field and
// code, and the device it names is still applied. A fake check keeps this test about
// applyDevices rather than about PartOfRoomNotInheritedProblems.
func TestApplyDevicesLogsEveryProblemFromInjectedChecks(t *testing.T) {
	fake := func(m map[string]DeviceConfig) []devices.Problem {
		return []devices.Problem{{ID: "dev", Field: "room", Code: "test.fake", Message: "fake problem"}}
	}
	cfg, rec := fetchDevices(t, map[string]any{
		"dev": map[string]any{"class": "cycle_power_device", "scheme": "zigbee", "primary": "0x01"},
	}, []DeviceCheck{fake})

	if _, ok := cfg.Devices["dev"]; !ok {
		t.Fatal("dev was dropped; checks are advisory")
	}
	lines := rec.problemLines()
	if len(lines) != 1 {
		t.Fatalf("logged %d problem lines, want exactly 1: %v", len(lines), lines)
	}
	for k, want := range map[string]string{
		"level": "WARN", "namespace": "devices_test", "device": "dev",
		"field": "room", "code": "test.fake", "problem": "fake problem",
	} {
		if lines[0][k] != want {
			t.Errorf("%s = %q, want %q (line %v)", k, lines[0][k], want, lines[0])
		}
	}
}

// Problems from every check are sorted together, by device id then code, so one
// device's lines sit together and the log reads the same on every reload. Two fake
// checks return interleaved ids, out of order, to prove the merge sorts.
func TestApplyDevicesSortsProblemsAcrossChecks(t *testing.T) {
	first := func(map[string]DeviceConfig) []devices.Problem {
		return []devices.Problem{{ID: "b", Code: "x.2"}, {ID: "a", Code: "x.9"}}
	}
	second := func(map[string]DeviceConfig) []devices.Problem {
		return []devices.Problem{{ID: "b", Code: "x.1"}, {ID: "a", Code: "x.3"}}
	}
	_, rec := fetchDevices(t, map[string]any{}, []DeviceCheck{first, second})

	var got []string
	for _, l := range rec.problemLines() {
		got = append(got, l["device"]+"/"+l["code"])
	}
	want := []string{"a/x.3", "a/x.9", "b/x.1", "b/x.2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// nil DeviceChecks means the defaults, so production (which never sets the field)
// runs both checks. End to end with the real check: a probe relying on its
// cylinder's room is logged once, structured, and still applied with no room.
func TestApplyDevicesDefaultChecksWarnWhenAProbeReliesOnInheritedRoom(t *testing.T) {
	cfg, rec := fetchDevices(t, map[string]any{
		"hot-water-cylinder": map[string]any{
			"class": "hot_water_cylinder", "room": "first.cupboard", "floor": "first", "telemetry": false,
		},
		"hot-water-cylinder-top": map[string]any{
			"class": "appliance_probe", "scheme": "tanktemp",
			"primary": "hot-water-cylinder.top", "part_of": "hot-water-cylinder",
		},
	}, nil)

	top, ok := cfg.Devices["hot-water-cylinder-top"]
	if !ok {
		t.Fatal("the probe was dropped; the problem is advisory")
	}
	if top.Room != "" || top.Floor != "" {
		t.Errorf("room = %q, floor = %q; applying config must not fill them in", top.Room, top.Floor)
	}

	var ours []map[string]string
	for _, l := range rec.problemLines() {
		if l["code"] == CodePartOfRoomNotInherited {
			ours = append(ours, l)
		}
	}
	if len(ours) != 1 {
		t.Fatalf("logged %s %d times, want exactly once: %v", CodePartOfRoomNotInherited, len(ours), rec.problemLines())
	}
	for k, want := range map[string]string{
		"namespace": "devices_test", "device": "hot-water-cylinder-top", "field": "room",
	} {
		if ours[0][k] != want {
			t.Errorf("%s = %q, want %q", k, ours[0][k], want)
		}
	}
}
