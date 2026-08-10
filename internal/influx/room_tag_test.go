package influx

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// The `location` tag on Influx points is the room a device sits in. Once the
// devices namespace is republished it supplies `room` and stops supplying the
// free-text `location`, so a writer reading Device.Location directly would
// silently stop tagging points — ahead of step-11-drop-location-tag, which is
// the change that is supposed to retire the tag deliberately. An Influx series
// that loses a tag cannot be repaired afterwards, so the writer resolves the
// room through Place() like every other reader.

func seedRoomDevice(t *testing.T, store *state.Store, id, class, room string) {
	t.Helper()
	rt := device.NewRuntime(device.Profile{Class: class}, 30*time.Minute)
	store.Upsert(id, model.Device{
		ID:       id,
		Class:    class,
		Room:     room,
		Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "0x1", Display: id},
	}, rt)
}

func TestDevicePointsTagRoomAsLocation(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedRoomDevice(t, store, "bigfridge", "continuous_power_device", "groundfloor.kitchen")

	w.OnCanonicalEvent(model.CanonicalEvent{
		DeviceID:  "bigfridge",
		Attribute: "power_w",
		Value:     1.0,
		Timestamp: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	})

	if len(api.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(api.Points))
	}
	if got := tagMap(api.Points[0])["location"]; got != "groundfloor.kitchen" {
		t.Errorf("location tag = %q, want the room id; a device with only Room set must still be tagged", got)
	}
}

func TestApplianceCyclePointsTagRoomAsLocation(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedRoomDevice(t, store, "dishwasher", "cycle_power_device", "groundfloor.kitchen")

	w.OnDerivedEvent(model.DerivedEvent{
		ID:          "evt-1",
		Type:        model.EvtCycleFinished,
		DeviceID:    "dishwasher",
		DeviceClass: "cycle_power_device",
		Timestamp:   time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		Evidence:    map[string]any{"duration_seconds": 3600.0},
	})

	if len(api.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(api.Points))
	}
	if got := tagMap(api.Points[0])["location"]; got != "groundfloor.kitchen" {
		t.Errorf("appliance_cycle location tag = %q, want the room id", got)
	}
}

// A namespace that has not been republished still supplies only `location`,
// and must keep tagging exactly as before.
func TestLegacyLocationStillTagsPoints(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "bigfridge", "continuous_power_device", "kitchen")

	w.OnCanonicalEvent(model.CanonicalEvent{
		DeviceID:  "bigfridge",
		Attribute: "power_w",
		Value:     1.0,
		Timestamp: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	})

	if got := tagMap(api.Points[0])["location"]; got != "kitchen" {
		t.Errorf("location tag = %q, want the legacy value kitchen", got)
	}
}
