package config

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// #83 decided that a probe does not inherit its appliance's room through part_of.
// devices.Check does not flag a probe with no room, because in the shared vocabulary
// that is legal: devices.PlaceOf fills it from the parent. So a namespace written for
// PlaceOf-reading consumers is valid there and silently roomless here. Statehouse
// therefore reports that case itself, as advisory, the same way it reports
// devices.Check problems: logged, never refused.

func off() *bool { b := false; return &b }

func TestPartOfRoomNotInheritedProblems(t *testing.T) {
	devs := map[string]DeviceConfig{
		"hot-water-cylinder": {Class: "hot_water_cylinder", Room: "first.cupboard", Telemetry: off()},
		// Flagged: part_of names a parent with a room, and the probe declares none. It
		// is the one case where PlaceOf and statehouse would answer differently.
		"cylinder-top": {Scheme: "tanktemp", Primary: "c.top", Class: "appliance_probe", PartOf: "hot-water-cylinder"},
		// Fine: declares its own room, as the decision on #83 asks.
		"cylinder-bottom": {Scheme: "tanktemp", Primary: "c.bottom", Class: "appliance_probe",
			Room: "first.cupboard", PartOf: "hot-water-cylinder"},
		// Fine: the legacy location still counts as a declared place (Place()).
		"cylinder-mid": {Scheme: "tanktemp", Primary: "c.mid", Class: "appliance_probe",
			Location: "cupboard", PartOf: "hot-water-cylinder"},
		// Not ours to report: the parent has no room, so PlaceOf gives "" too and the
		// answers agree. Nothing would be inherited.
		"roomless":       {Class: "fridge", Telemetry: off()},
		"roomless-probe": {Scheme: "climate", Primary: "f", Class: "appliance_probe", PartOf: "roomless"},
		// Not ours to report: an unresolved part_of is devices.Check's
		// part_of_unresolved, and reporting it twice would be noise.
		"orphan": {Scheme: "climate", Primary: "o", Class: "appliance_probe", PartOf: "nowhere"},
		// No part_of, no room: an ordinary unknown placement, not this problem.
		"plain": {Scheme: "zigbee", Primary: "0x01", Class: "cycle_power_device"},
	}

	got := PartOfRoomNotInheritedProblems(devs)
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.ID != "cylinder-top" || p.Code != CodePartOfRoomNotInherited || p.Field != "room" {
		t.Errorf("problem = %+v, want cylinder-top / %s / room", p, CodePartOfRoomNotInherited)
	}
	// The message must name the room the operator probably expected, so the fix is
	// obvious from the log line alone.
	if !strings.Contains(p.Message, "first.cupboard") || !strings.Contains(p.Message, "hot-water-cylinder") {
		t.Errorf("message %q should name the parent and its room", p.Message)
	}
}

// Problems come back in device-id order, like devices.Check's, so the log is stable
// from one reload to the next.
func TestPartOfRoomNotInheritedProblemsAreOrdered(t *testing.T) {
	devs := map[string]DeviceConfig{
		"fridge": {Class: "fridge", Room: "ground.kitchen", Telemetry: off()},
		"z":      {Scheme: "climate", Primary: "z", PartOf: "fridge"},
		"a":      {Scheme: "climate", Primary: "a", PartOf: "fridge"},
		"m":      {Scheme: "climate", Primary: "m", PartOf: "fridge"},
	}
	got := PartOfRoomNotInheritedProblems(devs)
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "a,m,z" {
		t.Errorf("ids = %v, want a,m,z", ids)
	}
}

// Wired in: the fetch logs it next to devices.Check's problems, and still applies the
// document. Advisory means it never costs the device.
func TestApplyDevicesWarnsWhenAProbeReliesOnInheritedRoom(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "devices_test", map[string]any{
		"hot-water-cylinder": map[string]any{
			"class": "hot_water_cylinder", "room": "first.cupboard", "floor": "first", "telemetry": false,
		},
		"hot-water-cylinder-top": map[string]any{
			"class": "appliance_probe", "scheme": "tanktemp",
			"primary": "hot-water-cylinder.top", "part_of": "hot-water-cylinder",
		},
	})
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})
	var logs bytes.Buffer
	f := newTestFetcher(t, mux)
	f.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	if err := f.ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote: %v", err)
	}

	if _, ok := cfg.Devices["hot-water-cylinder-top"]; !ok {
		t.Fatal("the probe was dropped; the problem is advisory")
	}
	if cfg.Devices["hot-water-cylinder-top"].Room != "" {
		t.Errorf("room = %q; applying config must not fill it in", cfg.Devices["hot-water-cylinder-top"].Room)
	}
	if !strings.Contains(logs.String(), CodePartOfRoomNotInherited) {
		t.Errorf("no %s warning logged; got:\n%s", CodePartOfRoomNotInherited, logs.String())
	}
}
