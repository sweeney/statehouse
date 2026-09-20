package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/energy"
	"github.com/sweeney/statehouse/internal/model"
)

// statehouse is where the phone app reads a device's room from, via /state/devices. Once
// the devices namespace carries `room` instead of the free-text `location`, that has to
// survive the whole chain: namespace -> config -> store -> API -> app.
//
// `location` stays in the response for one release, carrying the same value, because
// the app ships through Xcode and is the slowest lane.

func TestDeviceResponseCarriesRoom(t *testing.T) {
	d := model.Device{
		ID:          "bigfridge",
		DisplayName: "Kitchen Fridge",
		Class:       "continuous_power_device",
		Room:        "groundfloor.kitchen",
	}

	b, err := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["room"] != "groundfloor.kitchen" {
		t.Errorf("room = %v, want groundfloor.kitchen", got["room"])
	}
	if got["location"] != "groundfloor.kitchen" {
		t.Errorf("deprecated location alias = %v, want the same value", got["location"])
	}
}

// A device covering the whole property sits in a room but does not describe it.
func TestDeviceResponseCarriesCovers(t *testing.T) {
	d := model.Device{
		ID: "electricity_meter", Class: "energy_meter",
		Room: "basement.hallway", Covers: "house",
	}
	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if got["room"] != "basement.hallway" {
		t.Errorf("room = %v", got["room"])
	}
	if got["covers"] != "house" {
		t.Errorf("covers = %v, want house", got["covers"])
	}
}

// A namespace that has not been republished still carries `location`, and must keep
// working untouched.
func TestLegacyLocationStillPopulatesRoom(t *testing.T) {
	d := model.Device{ID: "bigfridge", Class: "continuous_power_device", Location: "kitchen"}

	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if got["room"] != "kitchen" {
		t.Errorf("room = %v, want the legacy location value", got["room"])
	}
}

// The device profile endpoint reads from device.Profile rather than
// model.Device, so it needs the same room/location fallback. Without it a
// republished namespace empties the profile's location and leaves nothing in
// its place.
func TestDeviceProfileResponseResolvesRoom(t *testing.T) {
	got := buildDeviceProfileResponse(device.Profile{
		Class:    "continuous_power_device",
		Room:     "groundfloor.kitchen",
		Strategy: energy.StrategyCounter,
	})
	if got.Location != "groundfloor.kitchen" {
		t.Errorf("profile location = %q, want the room id", got.Location)
	}
}

func TestDeviceProfileResponseKeepsLegacyLocation(t *testing.T) {
	got := buildDeviceProfileResponse(device.Profile{
		Class:    "continuous_power_device",
		Location: "kitchen",
		Strategy: energy.StrategyCounter,
	})
	if got.Location != "kitchen" {
		t.Errorf("profile location = %q, want the legacy value", got.Location)
	}
}

// Production served room="house" for the electricity meter, central heating and hot
// water: the legacy `location: house` was a coverage scope, and Place() returned it
// verbatim as a room. `house` is a reserved series key and not a legal room id.
func TestLegacyHouseLocationIsReportedAsCoverageNotARoom(t *testing.T) {
	// Covers arrives already resolved from config.DeviceConfig.Coverage(); the DTO
	// passes it through. Location is still carried so the deprecated alias is exercised.
	d := model.Device{ID: "electricity_meter", Class: "energy_meter",
		Location: "house", Covers: "house"}

	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if _, ok := got["room"]; ok {
		t.Errorf("room = %v, want absent: `house` names no room", got["room"])
	}
	if got["covers"] != "house" {
		t.Errorf("covers = %v, want house", got["covers"])
	}
}

// The devices namespace declares `floor` alongside `room` for every device, and
// countinghouse and greenhouse both relay it. statehouse dropped it at the config
// boundary, so /state could not answer which storey a device was on.
func TestDeviceResponseCarriesFloor(t *testing.T) {
	d := model.Device{
		ID: "basement-dishwasher", Class: "cycle_power_device",
		Room: "basement.kitchen", Floor: "basement",
	}

	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if got["floor"] != "basement" {
		t.Errorf("floor = %v, want basement", got["floor"])
	}
}

// An undeclared floor is UNKNOWN, not a value derived from the room id's
// "<floor>.<slug>" shape. Splitting the id here would be a second implementation of
// the floorplan's taxonomy, disagreeing the moment a room id is spelled unexpectedly.
func TestFloorIsNeverDerivedFromTheRoomID(t *testing.T) {
	d := model.Device{ID: "bigfridge", Class: "continuous_power_device",
		Room: "groundfloor.kitchen"}

	b, _ := json.Marshal(buildDeviceResponse(d, time.Time{}, nil, false))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if v, ok := got["floor"]; ok {
		t.Errorf("floor = %v, want absent: the namespace declared none", v)
	}
}

// /config/devices served `location` as its only place field while /state served
// room+covers, so one service answered two vocabularies about the same device. The
// profile endpoint now says everything /state does.
func TestDeviceProfileResponseCarriesRoomCoversAndFloor(t *testing.T) {
	b, err := json.Marshal(buildDeviceProfileResponse(device.Profile{
		Class:    "energy_meter",
		Room:     "basement.hallway",
		Covers:   "house",
		Floor:    "basement",
		Strategy: energy.StrategyCounter,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	for k, want := range map[string]string{
		"room":   "basement.hallway",
		"covers": "house",
		"floor":  "basement",
		// Unchanged: the alias keeps carrying the room id for one more release,
		// exactly as DeviceResponse does. This change is purely additive.
		"location": "basement.hallway",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
}

// Dropping `covers` from the profile endpoint was the one gap that could produce wrong
// numbers rather than a missing label: the whole-house meter looked like an ordinary
// device sitting in basement.hallway, so a consumer grouping off /config/devices would
// attribute the entire property's consumption to that room.
func TestDeviceProfileResponseOmitsCoversWhenDeviceCoversItsOwnRoom(t *testing.T) {
	b, _ := json.Marshal(buildDeviceProfileResponse(device.Profile{
		Class:    "cycle_power_device",
		Room:     "basement.kitchen",
		Floor:    "basement",
		Strategy: energy.StrategyCounter,
	}))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if v, ok := got["covers"]; ok {
		t.Errorf("covers = %v, want absent: the device covers the room it sits in", v)
	}
}

// The profile endpoint must resolve the legacy `location: house` exactly as /state
// does: `house` was always a coverage scope rather than a place, so it names no room
// and must not be published as one — it is a reserved series key that the floorplan
// taxonomy forbids as a room id.
//
// /state has had this guarantee since the sentinel was fixed; the profile DTO went
// without it only because it had no room field to get wrong.
func TestDeviceProfileResponseReportsLegacyHouseAsCoverageNotARoom(t *testing.T) {
	b, _ := json.Marshal(buildDeviceProfileResponse(device.Profile{
		Class: "energy_meter",
		// Covers arrives already resolved by config.DeviceConfig.Coverage(); the
		// legacy Location is still carried so the deprecated alias is exercised.
		Location: "house",
		Covers:   "house",
		Strategy: energy.StrategyCounter,
	}))
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if v, ok := got["room"]; ok {
		t.Errorf("room = %v, want absent: `house` names no room", v)
	}
	if v, ok := got["location"]; ok {
		t.Errorf("location = %v, want absent for the same reason", v)
	}
	if got["covers"] != "house" {
		t.Errorf("covers = %v, want house", got["covers"])
	}
}
