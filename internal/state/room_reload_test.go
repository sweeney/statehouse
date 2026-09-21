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

// Floor reaches a running service the same way Room does — a republished namespace
// plus SIGHUP, not a restart — so it follows Room through Upsert's merge rather than
// only being set at discovery.
func TestReloadConfigPropagatesFloor(t *testing.T) {
	cfgWithFloor := func(room, floor string) config.Config {
		cfg := config.Default()
		cfg.Devices = map[string]config.DeviceConfig{
			"kitchen_dishwasher": {
				Scheme:      "zigbee",
				Primary:     "0x00158d0000000001",
				Class:       "cycle_power_device",
				DisplayName: "Kitchen dishwasher",
				Room:        room,
				Floor:       floor,
			},
		}
		return cfg
	}

	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	eng := NewEngine(cfgWithFloor("", ""), store, clock)

	ident := model.DeviceIdentity{
		Scheme:  "zigbee",
		Primary: "0x00158d0000000001",
		Display: "Kitchen dishwasher",
	}
	id := eng.EnsureDiscovered(ident, "test")

	eng.ReloadConfig(cfgWithFloor("groundfloor.kitchen", "groundfloor"))

	d, ok := store.Get(id)
	if !ok {
		t.Fatalf("device %q not found after reload", id)
	}
	if d.Floor != "groundfloor" {
		t.Errorf("Floor = %q after reload, want groundfloor", d.Floor)
	}

	// And an absent floor must not wipe the one already held, matching Room.
	eng.ReloadConfig(cfgWithFloor("groundfloor.kitchen", ""))
	d, _ = store.Get(id)
	if d.Floor != "groundfloor" {
		t.Errorf("Floor = %q, want the previous value to survive an absent one", d.Floor)
	}
}

// The reload assertions above read store.Get — the model.Device, which is what /state
// serves. /config/devices is served from the paired record, and the two used to be
// updated by opposite rules: the device merges so an absent field does not overwrite,
// while Runtime.Profile is replaced wholesale by the EnsureDiscovered that ReloadConfig
// runs for every known device. A republished record that dropped a placement key
// therefore cleared it from one and not the other, and the two endpoints disagreed.
//
// ProfiledDevice closes that by construction: placement is read from the device half,
// so there is one source of truth rather than two code paths independently choosing to
// be sticky. This pins the contract at the store, where the divergence originated —
// internal/httpapi asserts the same thing end to end through the mux.
//
// Deliberately NOT asserted: that Runtime.Profile keeps its own placement across the
// reload. It does not, and that is fine — nothing serves placement from it. Pinning
// that would re-specify the bug as the intended behaviour.
func TestProfiledDevicesKeepPlacementWhenAReloadStopsDeclaringIt(t *testing.T) {
	cfg := func(room, floor, covers string) config.Config {
		c := config.Default()
		c.Devices = map[string]config.DeviceConfig{
			"kitchen_dishwasher": {
				Scheme: "zigbee", Primary: "0x00158d0000000001",
				Class: "cycle_power_device", DisplayName: "Kitchen dishwasher",
				Room: room, Floor: floor, Covers: covers,
			},
		}
		return c
	}

	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	eng := NewEngine(cfg("groundfloor.kitchen", "groundfloor", "house"), store, clock)

	ident := model.DeviceIdentity{
		Scheme: "zigbee", Primary: "0x00158d0000000001", Display: "Kitchen dishwasher",
	}
	id := eng.EnsureDiscovered(ident, "test")

	// The namespace is republished mid-migration, carrying the device but none of its
	// placement — the window DeviceConfig.Coverage() already warns about.
	eng.ReloadConfig(cfg("", "", ""))

	pd, ok := store.GetProfiled(id)
	if !ok {
		t.Fatalf("device %q has no paired record after reload", id)
	}
	if pd.Device.Place() != "groundfloor.kitchen" {
		t.Errorf("Place() = %q after reload, want the previous room to survive", pd.Device.Place())
	}
	if pd.Device.Floor != "groundfloor" {
		t.Errorf("Floor = %q after reload, want groundfloor", pd.Device.Floor)
	}
	if pd.Device.Covers != "house" {
		t.Errorf("Covers = %q after reload, want house", pd.Device.Covers)
	}
	// The profile half still answers for what only it knows, so the pairing has not
	// simply been emptied.
	if pd.Profile.Class != "cycle_power_device" {
		t.Errorf("Profile.Class = %q, want cycle_power_device", pd.Profile.Class)
	}
}

// ProfiledDevices and GetProfiled must agree on membership, or /config/devices and
// /config/devices/{id} would disagree about whether a device exists.
func TestProfiledDevicesAndGetProfiledAgreeOnMembership(t *testing.T) {
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	eng := NewEngine(roomReloadCfg("groundfloor.kitchen", "", ""), store, clock)
	id := eng.EnsureDiscovered(model.DeviceIdentity{
		Scheme: "zigbee", Primary: "0x00158d0000000001", Display: "Kitchen dishwasher",
	}, "test")

	all := store.ProfiledDevices()
	if _, ok := all[id]; !ok {
		t.Fatalf("ProfiledDevices omits %q", id)
	}
	if _, ok := store.GetProfiled(id); !ok {
		t.Errorf("GetProfiled omits %q, which ProfiledDevices includes", id)
	}
	if _, ok := store.GetProfiled("no_such_device"); ok {
		t.Errorf("GetProfiled invented an unknown device")
	}
}
