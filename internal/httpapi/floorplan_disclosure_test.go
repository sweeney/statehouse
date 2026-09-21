package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// The devices namespace hands statehouse more than statehouse publishes. Every record
// carries a `coordinate` — the device's position inside the house, in metres, to three
// decimal places — and some carry a free-text `note`. Neither is relayed.
//
// That is a DELIBERATE limit, not an oversight, and it is pinned here because the
// natural next question after "why did you add floor?" is "why not the rest of the
// record?". Room and floor are the coarse placement a consumer needs to group and label
// readings. A coordinate is a different kind of fact: it locates a person's belongings
// to within a few centimetres of a specific spot in their home, and no consumer of this
// API groups or labels anything by it. The floorplan service owns it and renders it;
// statehouse has no reason to hold it, so it does not have a field for it and cannot
// leak it by a later DTO change.
//
// This is the data-minimisation argument in test form: the boundary is the struct, so
// adding the field is the thing a reviewer would have to consciously approve.
func TestNamespaceCoordinateAndNoteAreNotRelayed(t *testing.T) {
	// A record shaped exactly like the live document, coordinate and note included.
	const record = `{
		"class": "cycle_power_device",
		"display_name": "Dishwasher (Basement)",
		"room": "basement.kitchen",
		"floor": "basement",
		"coordinate": [8.241, 3.211],
		"note": "behind the counter"
	}`

	var d config.DeviceConfig
	if err := json.Unmarshal([]byte(record), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// What survived the config boundary is all statehouse can ever publish.
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]any
	if err := json.Unmarshal(b, &kept); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"coordinate", "note"} {
		if v, ok := kept[field]; ok {
			t.Errorf("%s survived the config boundary as %v; statehouse must not hold it", field, v)
		}
	}
	// The placement facts statehouse does publish must still be there, or this test
	// would pass for the wrong reason.
	if d.Room != "basement.kitchen" || d.Floor != "basement" {
		t.Fatalf("room/floor lost: room=%q floor=%q", d.Room, d.Floor)
	}
}

// The same guarantee at the response DTO rather than the config struct: whatever a
// future config struct grows, these keys must not appear in a device response. This
// calls the builders directly; TestStateAndConfigEndpointsAgreeOnPlacement asserts the
// same absence on bodies served through the mux.
func TestDeviceResponsesExposeNoCoordinate(t *testing.T) {
	d := model.Device{
		ID: "basement-dishwasher", Class: "cycle_power_device",
		Room: "basement.kitchen", Floor: "basement",
	}
	bodies := map[string][]byte{}

	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	bodies["/state/devices"] = b

	b, _ = json.Marshal(buildDeviceProfileResponse(profiled(device.Profile{Class: d.Class}, d)))
	bodies["/config/devices"] = b

	for endpoint, body := range bodies {
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"coordinate", "note"} {
			if v, ok := got[field]; ok {
				t.Errorf("%s exposed %s = %v", endpoint, field, v)
			}
		}
	}
}

// placementCfg builds a one-device config for the whole-house meter — the awkward
// case, since it sits in a room but describes the entire property. Empty arguments
// stand for a namespace record that does not declare that key.
func placementCfg(room, floor, covers string) config.Config {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"energy_meter": {EnergyStrategy: "integration"},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"electricity_meter": {
			Scheme: "meter", Primary: "7C9EBDF6E56C",
			Class: "energy_meter", DisplayName: "Electricity Meter",
			Room: room, Floor: floor, Covers: covers,
		},
	}
	return cfg
}

// getJSON serves path through the mux and decodes the body, so a test covers the
// handler and the encoder rather than only the DTO builder.
func getJSON(t *testing.T, mux http.Handler, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: want 200, got %d", path, w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return got
}

// The bug this change fixes was not a missing field so much as two endpoints of one
// service disagreeing about the same device: /state said room+covers while
// /config/devices said location only. A consumer joining them got different answers
// depending on which it asked.
//
// This pins the invariant rather than the fields, so it keeps holding if either DTO
// grows: for the same device, the placement facts must match across both endpoints.
// Both responses are served through the mux, so it covers the handlers and the
// encoder rather than only the DTO builders.
func TestStateAndConfigEndpointsAgreeOnPlacement(t *testing.T) {
	cfg := placementCfg("basement.hallway", "basement", "house")

	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	srv := New(":0", store, nil, nil, nil, nil, cfg.DeviceClasses)
	mux := newMux(srv)

	id := engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "meter", Primary: "7C9EBDF6E56C", Display: "Electricity Meter"},
		"meter/7C9EBDF6E56C",
	)

	stateDev := getJSON(t, mux, "/state/devices/"+id)
	configDev := getJSON(t, mux, "/config/devices/"+id)

	for _, field := range []string{"room", "floor", "covers", "location"} {
		if stateDev[field] != configDev[field] {
			t.Errorf("%s: /state says %v, /config/devices says %v",
				field, stateDev[field], configDev[field])
		}
	}
	// The data-minimisation guarantee, served through the mux rather than off a DTO
	// builder: these keys must be absent from real response bodies, covering the
	// handlers and the encoder as well as the structs.
	for _, body := range []map[string]any{stateDev, configDev} {
		for _, field := range []string{"coordinate", "note"} {
			if v, ok := body[field]; ok {
				t.Errorf("response exposed %s = %v", field, v)
			}
		}
	}
	// And the values are the ones the namespace declared, so an agreement on two
	// wrong answers cannot pass.
	if stateDev["room"] != "basement.hallway" || stateDev["floor"] != "basement" ||
		stateDev["covers"] != "house" {
		t.Errorf("unexpected placement: %v", stateDev)
	}
}

// The agreement above only proves the two endpoints match for a device discovered
// under the config it is being served from. A republished namespace reaches a running
// service through SIGHUP, and the two records behind the two endpoints are updated by
// opposite rules: model.Device is merged so an absent field does not overwrite, while
// Runtime.Profile is replaced wholesale by EnsureDiscovered. So a record that drops a
// placement key makes the endpoints disagree — the very defect this change exists to
// close, one SIGHUP later.
//
// Two realistic triggers: a mid-migration republish that carries some placement facts
// and not others (the window DeviceConfig.Coverage() already documents), and a device
// retired from the floorplan but still reporting, whose record vanishes entirely.
func TestPlacementStillAgreesAfterAReloadDropsTheNamespaceKeys(t *testing.T) {
	cfg := placementCfg("basement.hallway", "basement", "house")
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	srv := New(":0", store, nil, nil, nil, nil, cfg.DeviceClasses)
	mux := newMux(srv)

	id := engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "meter", Primary: "7C9EBDF6E56C", Display: "Electricity Meter"},
		"meter/7C9EBDF6E56C",
	)

	// The namespace is republished without any placement keys.
	engine.ReloadConfig(placementCfg("", "", ""))

	stateDev := getJSON(t, mux, "/state/devices/"+id)
	configDev := getJSON(t, mux, "/config/devices/"+id)

	for _, field := range []string{"room", "floor", "covers", "location"} {
		if stateDev[field] != configDev[field] {
			t.Errorf("%s: /state says %v, /config/devices says %v",
				field, stateDev[field], configDev[field])
		}
	}
	// Both must have kept the last known placement rather than forgetting it: an
	// absent key is "not republished", not "no longer in a room".
	if stateDev["room"] != "basement.hallway" {
		t.Errorf("/state room = %v, want the previous value to survive", stateDev["room"])
	}
}
