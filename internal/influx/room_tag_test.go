package influx

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// This file used to pin the `location` tag on device points, resolving the room
// through Place() so a republished namespace supplying only `room` kept tagging.
// That mattered while the tag was still written; this branch retires it, so those
// tests were replaced by their inverses.
//
// statehouse writes points from three paths, and the tag is now gone from all of
// them:
//
//	OnCanonicalEvent      device_power, device_environment, device_battery,
//	                      device_ups, device_alarm, device_radio
//	OnDerivedEvent        appliance_cycle, device_activity, house_state
//	writeHouseElectricity house_electricity
//
// Every recent change to writer.go covered some of those paths and not others —
// tagSite missed OnDerivedEvent entirely and was caught in production, Place()
// reached only one reader. The tag is therefore removed from every path in one
// commit rather than one path at a time, and the test below asserts the absence
// on the path most recently forgotten.

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

// TestApplianceCyclePointsCarryNoLocationTag covers the derived-event path. Left
// alone it would have kept writing `location` after every other path stopped —
// and once the devices namespace is published, Place() returns a room id, so the
// tag would have carried room ids under a name meaning something else, in new
// history, indefinitely.
func TestApplianceCyclePointsCarryNoLocationTag(t *testing.T) {
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
	tags := tagMap(api.Points[0])
	if got, ok := tags["location"]; ok {
		t.Errorf("appliance_cycle still writes a location tag (%q)", got)
	}
	// The tags that replace it must survive.
	for _, want := range []string{"device_id", "class"} {
		if tags[want] == "" {
			t.Errorf("tag %q is missing: %v", want, tags)
		}
	}
}
