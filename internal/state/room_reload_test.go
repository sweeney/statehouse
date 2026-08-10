package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

func roomReloadCfg(room, covers, location string) config.Config {
	cfg := config.Default()
	cfg.Devices = map[string]config.DeviceConfig{
		"kitchen_dishwasher": {
			Scheme:      "zigbee",
			Primary:     "0x00158d0000000001",
			Class:       "cycle_power_device",
			DisplayName: "Kitchen dishwasher",
			Room:        room,
			Covers:      covers,
			Location:    location,
		},
	}
	return cfg
}

// Publishing rooms to the devices namespace is the whole point of the
// floorplan migration, and a republished namespace reaches a running service
// through SIGHUP, not a restart. Room and Covers must therefore survive a
// config reload onto an already-discovered device exactly as Location does —
// otherwise rooms appear to be ignored until someone happens to restart.
func TestReloadConfigPropagatesRoomAndCovers(t *testing.T) {
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	eng := NewEngine(roomReloadCfg("", "", "kitchen"), store, clock)

	ident := model.DeviceIdentity{
		Scheme:  "zigbee",
		Primary: "0x00158d0000000001",
		Display: "Kitchen dishwasher",
	}
	id := eng.EnsureDiscovered(ident, "test")

	// The devices namespace is republished with floorplan ids.
	eng.ReloadConfig(roomReloadCfg("groundfloor.kitchen", "house", "kitchen-updated"))

	d, ok := store.Get(id)
	if !ok {
		t.Fatalf("device %q not found after reload", id)
	}
	if d.Room != "groundfloor.kitchen" {
		t.Errorf("Room = %q after reload, want groundfloor.kitchen", d.Room)
	}
	if d.Covers != "house" {
		t.Errorf("Covers = %q after reload, want house", d.Covers)
	}
	// Location already behaved this way; pin it so the three stay consistent.
	if d.Location != "kitchen-updated" {
		t.Errorf("Location = %q after reload, want kitchen-updated", d.Location)
	}
}

// A reload that no longer names a room must not silently clear the one the
// device already has: Upsert merges non-empty fields, and Room follows the
// same rule as Location so a partial config cannot wipe state.
func TestReloadConfigDoesNotClearRoomWhenAbsent(t *testing.T) {
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	eng := NewEngine(roomReloadCfg("groundfloor.kitchen", "house", "kitchen"), store, clock)

	ident := model.DeviceIdentity{
		Scheme:  "zigbee",
		Primary: "0x00158d0000000001",
		Display: "Kitchen dishwasher",
	}
	id := eng.EnsureDiscovered(ident, "test")

	eng.ReloadConfig(roomReloadCfg("", "", ""))

	d, _ := store.Get(id)
	if d.Room != "groundfloor.kitchen" {
		t.Errorf("Room = %q, want the previous value to survive an absent one", d.Room)
	}
	if d.Covers != "house" {
		t.Errorf("Covers = %q, want the previous value to survive an absent one", d.Covers)
	}
}
