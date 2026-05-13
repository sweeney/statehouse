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
