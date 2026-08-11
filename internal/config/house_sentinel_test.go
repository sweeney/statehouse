package config

import "testing"

// The legacy free-text `location` carried two different facts. Usually a place, but
// for the three whole-property devices — the electricity meter, central heating and
// hot water — it carried `house`, which is a coverage scope and not a room.
//
// Resolving it as a room republishes exactly the conflation this migration removes,
// and `house` is a reserved series key that the floorplan taxonomy forbids as a room
// id. Production was serving room="house" for all three.
func TestLegacyHouseLocationIsCoverageNotARoom(t *testing.T) {
	d := DeviceConfig{Location: "house"}

	if got := d.Place(); got != "" {
		t.Errorf("Place() = %q, want empty: `house` is a scope, not a room", got)
	}
	if got := d.Coverage(); got != "house" {
		t.Errorf("Coverage() = %q, want house", got)
	}
}

func TestExplicitCoversWins(t *testing.T) {
	d := DeviceConfig{Room: "groundfloor.boiler-room", Covers: "house"}

	if got := d.Place(); got != "groundfloor.boiler-room" {
		t.Errorf("Place() = %q, want the room it sits in", got)
	}
	if got := d.Coverage(); got != "house" {
		t.Errorf("Coverage() = %q, want house", got)
	}
}

// A namespace mid-migration may publish `room` before it publishes `covers`. Losing
// the coverage fact in that window would make republishing order load-bearing with
// nothing enforcing it, so the legacy spelling is honoured even once a room exists.
func TestLegacyHouseSurvivesARoomBeingPublishedFirst(t *testing.T) {
	d := DeviceConfig{Room: "groundfloor.boiler-room", Location: "house"}

	if got := d.Place(); got != "groundfloor.boiler-room" {
		t.Errorf("Place() = %q, want the room", got)
	}
	if got := d.Coverage(); got != "house" {
		t.Errorf("Coverage() = %q, want house — coverage must not depend on publish order", got)
	}
}

func TestOrdinaryDeviceIsUnaffected(t *testing.T) {
	for _, d := range []DeviceConfig{
		{Location: "kitchen"},
		{Room: "groundfloor.kitchen"},
	} {
		if d.Place() == "" {
			t.Errorf("%+v: Place() lost the room", d)
		}
		if got := d.Coverage(); got != "" {
			t.Errorf("%+v: Coverage() = %q, want empty (covers its own room)", d, got)
		}
	}
}
