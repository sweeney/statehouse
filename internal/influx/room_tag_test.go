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
// tests were removed here rather than left asserting behaviour the branch deletes.
//
// One case survives, and it is not vestigial. statehouse writes the location tag
// from two places:
//
//	OnCanonicalEvent    device_power, device_environment, device_battery,
//	                    device_ups, device_alarm, device_radio   — tag removed
//	OnDerivedEvent      appliance_cycle                          — tag STILL WRITTEN
//
// This branch only removes the first. `appliance_cycle` continues to carry
// `location`, so the tag is not fully retired and the test below records that
// rather than pretending otherwise. Removing the second write is left to the
// migration work; when it happens, invert this test.

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

// TestApplianceCyclePointsStillTagRoomAsLocation documents the write path this
// branch does not touch. It is deliberately an assertion that the tag is
// present: if someone removes that write without updating this test, it fails
// and the gap closes visibly instead of silently.
func TestApplianceCyclePointsStillTagRoomAsLocation(t *testing.T) {
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
		t.Errorf("appliance_cycle location tag = %q, want the room id; "+
			"if this write was intentionally removed, invert this test", got)
	}
}
