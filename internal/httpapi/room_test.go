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
