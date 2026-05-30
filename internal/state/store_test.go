package state

import (
	"testing"

	"github.com/sweeney/statehouse/internal/model"
)

func TestStore_LookupByPrimary(t *testing.T) {
	s := NewStore()
	id := model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"}
	s.Upsert("kettle", model.Device{ID: "kettle", Identity: id}, nil)
	if got := s.LookupID(id); got != "kettle" {
		t.Fatalf("lookup by full identity: got %q want kettle", got)
	}
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc"}); got != "kettle" {
		t.Fatalf("lookup by primary only: got %q want kettle", got)
	}
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: "kettle"}); got != "kettle" {
		t.Fatalf("lookup by display only: got %q want kettle", got)
	}
}

func TestStore_LookupIsSchemeScoped(t *testing.T) {
	// Two different adapters can use the same Display string without
	// colliding — keys are namespaced by Scheme.
	s := NewStore()
	s.Upsert("z_kettle", model.Device{ID: "z_kettle", Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"}}, nil)
	s.Upsert("t_kettle", model.Device{ID: "t_kettle", Identity: model.DeviceIdentity{Scheme: "tasmota", Primary: "DVES_001", Display: "kettle"}}, nil)
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: "kettle"}); got != "z_kettle" {
		t.Errorf("zigbee/kettle: got %q want z_kettle", got)
	}
	if got := s.LookupID(model.DeviceIdentity{Scheme: "tasmota", Display: "kettle"}); got != "t_kettle" {
		t.Errorf("tasmota/kettle: got %q want t_kettle", got)
	}
}

func TestStore_PrimaryLearnedLaterDoesNotCreateDuplicate(t *testing.T) {
	// First contact: only Display known. Adapter fell back to
	// Primary=Display so the engine still has a key.
	s := NewStore()
	first := model.DeviceIdentity{Scheme: "zigbee", Primary: "kitchen_kettle", Display: "kitchen_kettle"}
	s.Upsert("kitchen_kettle", model.Device{ID: "kitchen_kettle", Identity: first}, nil)
	// Later: bridge/devices arrives and the adapter now knows IEEE.
	// It re-emits with the canonical Primary. The store should resolve
	// the existing record by Display (since byPrimary doesn't yet
	// contain the IEEE) and update its Primary in place.
	canonical := model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kitchen_kettle"}
	if got := s.LookupID(canonical); got != "kitchen_kettle" {
		t.Fatalf("LookupID by canonical identity should resolve via Display fallback: got %q", got)
	}
	s.Upsert("kitchen_kettle", model.Device{ID: "kitchen_kettle", Identity: canonical}, nil)
	d, _ := s.Get("kitchen_kettle")
	if d.Identity.Primary != "0xabc" {
		t.Fatalf("expected Primary upgraded to 0xabc, got %q", d.Identity.Primary)
	}
	// And byPrimary now resolves it too.
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc"}); got != "kitchen_kettle" {
		t.Fatalf("byPrimary lookup after upgrade: got %q want kitchen_kettle", got)
	}
}

func TestStore_UpsertDoesNotEraseKnownPrimaryWithEmptyOne(t *testing.T) {
	// A subsequent payload that doesn't carry the canonical Primary
	// must not blank out the IEEE we already learned.
	s := NewStore()
	canonical := model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "k"}
	s.Upsert("k", model.Device{ID: "k", Identity: canonical}, nil)
	partial := model.DeviceIdentity{Scheme: "zigbee", Display: "k"}
	s.Upsert("k", model.Device{ID: "k", Identity: partial}, nil)
	d, _ := s.Get("k")
	if d.Identity.Primary != "0xabc" {
		t.Fatalf("expected Primary preserved, got %q", d.Identity.Primary)
	}
}

func TestStore_PhantomUpgradeEvictsOldPrimaryIndex(t *testing.T) {
	// Reproduces the leak in issue #49: a phantom Primary=Display key
	// must not linger in byPrimary after the IEEE upgrade lands.
	s := NewStore()
	phantom := model.DeviceIdentity{Scheme: "zigbee", Primary: "kitchen_kettle", Display: "kitchen_kettle"}
	s.Upsert("kitchen_kettle", model.Device{ID: "kitchen_kettle", Identity: phantom}, nil)
	canonical := model.DeviceIdentity{Scheme: "zigbee", Primary: "0x00158d000123abcd", Display: "kitchen_kettle"}
	s.Upsert("kitchen_kettle", model.Device{ID: "kitchen_kettle", Identity: canonical}, nil)
	// The phantom Primary key must no longer resolve — otherwise a stale
	// lookup against the old key still returns the live device id.
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Primary: "kitchen_kettle"}); got != "" {
		t.Fatalf("expected phantom byPrimary key evicted after upgrade, got %q", got)
	}
	if got := s.LookupID(canonical); got != "kitchen_kettle" {
		t.Fatalf("canonical lookup after upgrade: got %q", got)
	}
}

func TestStore_UpsertRenameEvictsOldDisplayIndex(t *testing.T) {
	// A display change via Upsert (not Rename) must also evict the old
	// byDisplay entry — Rename is not the only mutator.
	s := NewStore()
	s.Upsert("k", model.Device{ID: "k", Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "old_name"}}, nil)
	s.Upsert("k", model.Device{ID: "k", Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "new_name"}}, nil)
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: "old_name"}); got != "" {
		t.Fatalf("expected old display evicted after Upsert rename, got %q", got)
	}
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: "new_name"}); got != "k" {
		t.Fatalf("new display lookup: got %q", got)
	}
}

func TestStore_RenameUpdatesDisplayIndex(t *testing.T) {
	s := NewStore()
	s.Upsert("k", model.Device{ID: "k", Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "old"}}, nil)
	s.Rename("k", "old", "new")
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: "new"}); got != "k" {
		t.Fatalf("expected new display to resolve, got %q", got)
	}
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: "old"}); got != "" {
		t.Fatalf("expected old display to be removed, got %q", got)
	}
}

// TestStore_UpsertTombstonesOrphanedPhantomOnDisplayCollision reproduces
// the "islaav duplicate" production bug: two separate phantoms exist for
// the same physical device (one keyed by IEEE, one by friendly name).
// When bridge/devices updates the IEEE device's Display to the friendly
// name, the friendly-name phantom is orphaned and must be tombstoned.
func TestStore_UpsertTombstonesOrphanedPhantomOnDisplayCollision(t *testing.T) {
	s := NewStore()

	// Step 1: Z2M uses IEEE as friendly name during interview — phantom
	// with Primary==Display==ieee.
	ieee := "0x20a716fffea4634f"
	s.Upsert(ieee, model.Device{ID: ieee, Identity: model.DeviceIdentity{
		Scheme: "zigbee", Primary: ieee, Display: ieee,
	}}, nil)

	// Step 2: An availability or payload for the user-given friendly name
	// arrives before bridge/devices maps ieee→name. The adapter falls back
	// to Primary=Display=fn, creating a second phantom.
	fn := "islaav"
	s.Upsert(fn, model.Device{ID: fn, Identity: model.DeviceIdentity{
		Scheme: "zigbee", Primary: fn, Display: fn,
	}}, nil)

	if got := len(s.Devices()); got != 2 {
		t.Fatalf("setup: expected 2 phantom devices, got %d", got)
	}

	// Step 3: bridge/devices arrives with the canonical mapping. The
	// engine finds the IEEE device via byPrimary and updates its Display.
	// Upsert must tombstone the fn phantom whose byDisplay entry is stolen.
	s.Upsert(ieee, model.Device{ID: ieee, Identity: model.DeviceIdentity{
		Scheme: "zigbee", Primary: ieee, Display: fn,
	}}, nil)

	devs := s.Devices()
	if len(devs) != 1 {
		t.Fatalf("expected exactly 1 device after phantom tombstone, got %d: %v", len(devs), devs)
	}
	d, ok := s.Get(ieee)
	if !ok {
		t.Fatalf("expected device %q to survive", ieee)
	}
	if d.Identity.Display != fn {
		t.Errorf("Display: got %q, want %q", d.Identity.Display, fn)
	}
	if _, exists := s.Get(fn); exists {
		t.Errorf("phantom %q must be tombstoned", fn)
	}
	// byDisplay must route to the real device.
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Display: fn}); got != ieee {
		t.Errorf("byDisplay lookup: got %q, want %q", got, ieee)
	}
	// The phantom's byPrimary entry must be evicted.
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Primary: fn}); got != "" {
		t.Errorf("phantom byPrimary must be evicted, got %q", got)
	}
}

// TestStore_UpsertDoesNotTombstoneRealDeviceOnDisplayCollision guards
// against false positives: a real device (Primary != Display) that loses
// a byDisplay collision must not be deleted — it's still reachable via
// its stable Primary key.
func TestStore_UpsertDoesNotTombstoneRealDeviceOnDisplayCollision(t *testing.T) {
	s := NewStore()

	// Two real devices with distinct IEEEs but the same display name
	// (data inconsistency, but must not destroy either device).
	s.Upsert("device_a", model.Device{ID: "device_a", Identity: model.DeviceIdentity{
		Scheme: "zigbee", Primary: "0xaaa", Display: "shared_name",
	}}, nil)
	s.Upsert("device_b", model.Device{ID: "device_b", Identity: model.DeviceIdentity{
		Scheme: "zigbee", Primary: "0xbbb", Display: "shared_name",
	}}, nil)

	// device_a is a real device and must not be tombstoned.
	if _, ok := s.Get("device_a"); !ok {
		t.Fatalf("real device_a must not be tombstoned by a display collision")
	}
	// device_a is still reachable by its Primary.
	if got := s.LookupID(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xaaa"}); got != "device_a" {
		t.Errorf("device_a primary lookup: got %q, want device_a", got)
	}
}
