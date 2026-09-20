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

// The same guarantee at the edge rather than the boundary: whatever a future config
// struct grows, these keys must not appear in a device response.
func TestDeviceResponsesExposeNoCoordinate(t *testing.T) {
	d := model.Device{
		ID: "basement-dishwasher", Class: "cycle_power_device",
		Room: "basement.kitchen", Floor: "basement",
	}
	bodies := map[string][]byte{}

	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	bodies["/state/devices"] = b

	b, _ = json.Marshal(buildDeviceProfileResponse(device.Profile{
		Class: d.Class, Room: d.Room, Floor: d.Floor,
	}))
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

// The bug this change fixes was not a missing field so much as two endpoints of one
// service disagreeing about the same device: /state said room+covers while
// /config/devices said location only. A consumer joining them got different answers
// depending on which it asked.
//
// This pins the invariant rather than the fields, so it keeps holding if either DTO
// grows: for the same device, the placement facts must match across both endpoints.
func TestStateAndConfigEndpointsAgreeOnPlacement(t *testing.T) {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"energy_meter": {EnergyStrategy: "integration"},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"electricity_meter": {
			Scheme: "meter", Primary: "7C9EBDF6E56C",
			Class: "energy_meter", DisplayName: "Electricity Meter",
			// The awkward case: sits in a room, describes the whole property.
			Room: "basement.hallway", Floor: "basement", Covers: "house",
		},
	}

	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	srv := New(":0", store, nil, nil, nil, nil, cfg.DeviceClasses)
	mux := newMux(srv)

	id := engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "meter", Primary: "7C9EBDF6E56C", Display: "Electricity Meter"},
		"meter/7C9EBDF6E56C",
	)

	get := func(path string) map[string]any {
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

	stateDev := get("/state/devices/" + id)
	configDev := get("/config/devices/" + id)

	for _, field := range []string{"room", "floor", "covers", "location"} {
		if stateDev[field] != configDev[field] {
			t.Errorf("%s: /state says %v, /config/devices says %v",
				field, stateDev[field], configDev[field])
		}
	}
	// And the values are the ones the namespace declared, so an agreement on two
	// wrong answers cannot pass.
	if stateDev["room"] != "basement.hallway" || stateDev["floor"] != "basement" ||
		stateDev["covers"] != "house" {
		t.Errorf("unexpected placement: %v", stateDev)
	}
}
