package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// #83: statehouse relays part_of on /state/devices and /config/devices, next to room,
// floor and covers. It tells a consumer which appliance a probe is inside, so a
// cylinder probe can be told from a fridge probe even though both are
// class: appliance_probe. The class still says "this reading is not ambient";
// part_of says which appliance it describes.

func decodeBody(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDeviceResponseCarriesPartOf(t *testing.T) {
	got := decodeBody(t, buildDeviceResponse(model.Device{
		ID: "hot-water-cylinder-top", Class: "appliance_probe",
		Room: "first.cupboard", PartOf: "hot-water-cylinder",
	}, time.Time{}, nil, false))

	if got["part_of"] != "hot-water-cylinder" {
		t.Errorf("part_of = %v, want hot-water-cylinder", got["part_of"])
	}
}

func TestDeviceProfileResponseCarriesPartOf(t *testing.T) {
	got := decodeBody(t, buildDeviceProfileResponse(profiled(
		device.Profile{Class: "appliance_probe"},
		model.Device{Class: "appliance_probe", Room: "first.cupboard", PartOf: "hot-water-cylinder"},
	)))

	if got["part_of"] != "hot-water-cylinder" {
		t.Errorf("part_of = %v, want hot-water-cylinder", got["part_of"])
	}
}

// Most devices are part of nothing. The key is then absent, not "", on both endpoints:
// the same omitempty convention as covers, so a consumer can test for presence.
func TestPartOfIsAbsentWhenNotDeclared(t *testing.T) {
	d := model.Device{ID: "kitchen_dishwasher", Class: "cycle_power_device", Room: "ground.kitchen"}
	bodies := map[string]map[string]any{
		"/state/devices":  decodeBody(t, buildDeviceResponse(d, time.Time{}, nil, false)),
		"/config/devices": decodeBody(t, buildDeviceProfileResponse(profiled(device.Profile{Class: d.Class}, d))),
	}
	for endpoint, got := range bodies {
		if v, ok := got["part_of"]; ok {
			t.Errorf("%s: part_of = %v, want absent", endpoint, v)
		}
	}
}

// part_of must be read from the DEVICE record, not the profile, for the same reason
// room is: the profile is replaced wholesale on every re-resolution, while the device
// merges. A profile with no part_of next to a device that has one must still answer
// with the device's value, or /config/devices would drop it one SIGHUP after a
// republish that stopped declaring it, while /state kept it.
func TestDeviceProfileResponseReadsPartOfFromTheDeviceRecord(t *testing.T) {
	got := decodeBody(t, buildDeviceProfileResponse(profiled(
		device.Profile{Class: "appliance_probe", PartOf: ""},
		model.Device{Class: "appliance_probe", PartOf: "hot-water-cylinder"},
	)))
	if got["part_of"] != "hot-water-cylinder" {
		t.Errorf("part_of = %v, want the device record's hot-water-cylinder", got["part_of"])
	}
}

// probeCfg is a cylinder and a probe that declares part_of but NO room: the case the
// second half of #83 decided. The cylinder declares a room, so devices.PlaceOf would
// give the probe one. Statehouse must not.
func probeCfg() config.Config {
	off := false
	cfg := config.Default()
	cfg.Devices = map[string]config.DeviceConfig{
		"hot-water-cylinder": {
			Class: "hot_water_cylinder", Room: "first.cupboard", Floor: "first",
			Telemetry: &off,
		},
		"hot-water-cylinder-top": {
			Scheme: "tanktemp", Primary: "hot-water-cylinder.top",
			Class: "appliance_probe", PartOf: "hot-water-cylinder",
		},
	}
	return cfg
}

// End to end through the mux: both endpoints relay part_of, agree on it, and neither
// invents a room for a probe that declared none.
func TestProbeRelaysPartOfWithoutInheritingARoom(t *testing.T) {
	cfg := probeCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	mux := newMux(New(":0", store, nil, nil, nil, nil, cfg.DeviceClasses))

	id := engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "tanktemp", Primary: "hot-water-cylinder.top"},
		"energy/hot_water/hot-water-cylinder/state",
	)

	for _, path := range []string{"/state/devices/" + id, "/config/devices/" + id} {
		body := getJSON(t, mux, path)
		if body["part_of"] != "hot-water-cylinder" {
			t.Errorf("%s: part_of = %v, want hot-water-cylinder", path, body["part_of"])
		}
		for _, field := range []string{"room", "floor", "location"} {
			if v, ok := body[field]; ok {
				t.Errorf("%s: %s = %v, want absent: a probe does not inherit its appliance's placement",
					path, field, v)
			}
		}
	}
}

// part_of is on MQTT too, not only HTTP. cmd/statehouse builds the retained
// house/state/devices/{id} payload with BuildDeviceResponse and house/state/snapshot
// with BuildSnapshot, the exported builders. Pinned here so the MQTT contract cannot
// lose the field while the HTTP tests above stay green.
func TestMQTTPayloadBuildersCarryPartOf(t *testing.T) {
	d := model.Device{
		ID: "hot-water-cylinder-top", Class: "appliance_probe",
		Room: "first.cupboard", PartOf: "hot-water-cylinder",
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	perDevice := decodeBody(t, BuildDeviceResponse(d, now, nil))
	if perDevice["part_of"] != "hot-water-cylinder" {
		t.Errorf("house/state/devices/{id}: part_of = %v, want hot-water-cylinder", perDevice["part_of"])
	}

	snap := decodeBody(t, BuildSnapshot(model.Snapshot{
		GeneratedAt: now, Devices: map[string]model.Device{d.ID: d},
	}, nil, nil, now, nil, time.Time{}, "home"))
	devs, _ := snap["devices"].(map[string]any)
	inSnap, _ := devs[d.ID].(map[string]any)
	if inSnap["part_of"] != "hot-water-cylinder" {
		t.Errorf("house/state/snapshot: devices[%s].part_of = %v, want hot-water-cylinder", d.ID, inSnap["part_of"])
	}
}
