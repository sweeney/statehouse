package influx

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// The `site` tag exists because a second site already does. It is in the sites
// namespace today, so the moment it reports telemetry into the same bucket
// device ids collide and that history is permanently ambiguous. Adding the tag is
// cheap now and impossible retroactively.
//
// It is `site`, not `location`: `location` is already in the data meaning a room.

func TestEveryDevicePointCarriesTheSiteTag(t *testing.T) {
	w, api, store := newWriterTest(t)
	w.Site = "home"
	seedDevice(t, store, "bigfridge", "continuous_power_device", "kitchen")

	ts := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for _, attr := range []string{"power_w", "voltage_v", "energy_kwh", "temperature_c", "humidity_pct"} {
		w.OnCanonicalEvent(model.CanonicalEvent{
			DeviceID: "bigfridge", Attribute: attr, Value: 1.0, Timestamp: ts,
		})
	}

	points := api.Points
	if len(points) == 0 {
		t.Fatal("no points written")
	}
	for _, p := range points {
		if got := tagMap(p)["site"]; got != "home" {
			t.Errorf("%s point: site tag = %q, want %q", p.Name(), got, "home")
		}
	}
}

func TestHouseElectricityPointsCarryTheSiteTag(t *testing.T) {
	w, api, _ := newWriterTest(t)
	w.Site = "home"

	w.OnCanonicalEvent(model.CanonicalEvent{
		DeviceID:   "electricity_meter",
		Capability: state.HouseElectricityCapability,
		Attribute:  "gross_w",
		Value:      1500.0,
		Timestamp:  time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	})

	points := api.Points
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	if got := tagMap(points[0])["site"]; got != "home" {
		t.Errorf("site tag = %q, want %q", got, "home")
	}
}

// An unconfigured site must not write an empty tag value: an empty tag is a distinct
// series in Influx and would be worse than no tag at all.
func TestNoSiteConfiguredWritesNoSiteTag(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "bigfridge", "continuous_power_device", "kitchen")

	w.OnCanonicalEvent(model.CanonicalEvent{
		DeviceID: "bigfridge", Attribute: "power_w", Value: 1.0,
		Timestamp: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	})

	points := api.Points
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	if _, ok := tagMap(points[0])["site"]; ok {
		t.Error("a writer with no site must omit the tag, not write an empty one")
	}
}

// Step 11 of the floorplan migration: statehouse stops writing the `location` tag.
//
// The tag was write-only. Both read services decoded it into a Row field neither ever
// read, and every `location:` inside a Flux query is timezone.location, not this tag.
// The one genuine consumer is cmd/climate-dashboard, a standalone dev tool whose chart
// label already falls back to the device id when the tag is absent.
//
// Old points keep their stale tag and are simply unread: no backfill, so no backfill
// bugs, and a room rename never touches stored data.
func TestLocationTagIsNoLongerWritten(t *testing.T) {
	w, api, store := newWriterTest(t)
	w.Site = "home"
	seedDevice(t, store, "bigfridge", "continuous_power_device", "kitchen")

	w.OnCanonicalEvent(model.CanonicalEvent{
		DeviceID: "bigfridge", Attribute: "power_w", Value: 1.0,
		Timestamp: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	})

	points := api.Points
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	tags := tagMap(points[0])
	if _, ok := tags["location"]; ok {
		t.Errorf("location tag is still written: %v", tags)
	}
	// The tags that replace it must still be there.
	for _, want := range []string{"device_id", "class", "site"} {
		if tags[want] == "" {
			t.Errorf("tag %q is missing: %v", want, tags)
		}
	}
}

// Derived events produce three more measurements — appliance_cycle,
// device_activity and house_state — through a separate write path. They need
// the site tag for the same reason the device points do: without it those
// series are ambiguous the moment a second property reports, and the gap
// cannot be repaired retroactively. house_state carries no device_id at all,
// so site is the only thing distinguishing one property's house state from
// another's.
func TestDerivedEventPointsCarryTheSiteTag(t *testing.T) {
	ts := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name        string
		measurement string
		ev          model.DerivedEvent
	}{
		{
			name:        "appliance_cycle",
			measurement: "appliance_cycle",
			ev: model.DerivedEvent{
				Type:        model.EvtCycleFinished,
				DeviceID:    "dishwasher",
				DeviceClass: "cycle_power_device",
				Timestamp:   ts,
				Evidence:    map[string]any{"duration_seconds": 3600.0},
			},
		},
		{
			name:        "device_activity",
			measurement: "device_activity",
			ev: model.DerivedEvent{
				Type:        model.EvtDeviceActivityChanged,
				DeviceID:    "dishwasher",
				DeviceClass: "cycle_power_device",
				Timestamp:   ts,
				Evidence:    map[string]any{"from": "idle", "to": "active"},
			},
		},
		{
			name:        "house_state",
			measurement: "house_state",
			ev: model.DerivedEvent{
				Type:      model.EvtHouseStateChanged,
				Timestamp: ts,
				Evidence: map[string]any{
					"occupancy":            "occupied",
					"occupancy_confidence": 0.9,
					"activity":             "active",
					"activity_confidence":  0.8,
					"mode":                 "day",
					"mode_confidence":      0.7,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, api, store := newWriterTest(t)
			w.Site = "home"
			seedDevice(t, store, "dishwasher", "cycle_power_device", "kitchen")

			w.OnDerivedEvent(tc.ev)

			if len(api.Points) != 1 {
				t.Fatalf("points = %d, want 1", len(api.Points))
			}
			p := api.Points[0]
			if p.Name() != tc.measurement {
				t.Fatalf("measurement = %q, want %q", p.Name(), tc.measurement)
			}
			if got := tagMap(p)["site"]; got != "home" {
				t.Errorf("%s: site tag = %q, want %q", tc.measurement, got, "home")
			}
		})
	}
}

// And an unconfigured site must still omit the tag on this path rather than
// writing an empty value.
func TestDerivedEventPointsOmitEmptySiteTag(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "dishwasher", "cycle_power_device", "kitchen")

	w.OnDerivedEvent(model.DerivedEvent{
		Type:        model.EvtDeviceActivityChanged,
		DeviceID:    "dishwasher",
		DeviceClass: "cycle_power_device",
		Timestamp:   time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		Evidence:    map[string]any{"from": "idle", "to": "active"},
	})

	if len(api.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(api.Points))
	}
	if _, ok := tagMap(api.Points[0])["site"]; ok {
		t.Error("a writer with no site must omit the tag on derived events too")
	}
}
