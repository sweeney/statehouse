package state

import (
	"sync"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// Store holds the in-memory state of all known devices plus the
// current whole-house state. Access is serialised through an RWMutex.
type Store struct {
	mu     sync.RWMutex
	dev    map[string]*deviceEntry
	byIEEE map[string]string // ieee -> device id
	byName map[string]string // friendly_name -> device id
	house  model.House
}

type deviceEntry struct {
	Device  model.Device
	Runtime *device.Runtime

	// availabilityOfflineAt is non-nil once Z2M signalled offline; it
	// stays in offline_pending until the debounce elapses.
	availabilityOfflineAt *time.Time
}

// NewStore returns an empty store with whole-house state = unknown.
func NewStore() *Store {
	return &Store{
		dev:    make(map[string]*deviceEntry),
		byIEEE: make(map[string]string),
		byName: make(map[string]string),
		house:  model.House{State: model.HouseUnknown},
	}
}

// Upsert installs a device record at id with the given runtime. It is
// idempotent: subsequent calls update identity/profile metadata
// without resetting runtime state.
func (s *Store) Upsert(id string, d model.Device, rt *device.Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.dev[id]
	if !ok {
		entry = &deviceEntry{Device: d, Runtime: rt}
		s.dev[id] = entry
	} else {
		// Preserve mutable runtime state; refresh metadata only.
		// Do not overwrite identity fields with empty values — a
		// device payload from MQTT doesn't carry IEEE, but we may
		// already know it from a bridge/devices announcement.
		if d.DisplayName != "" {
			entry.Device.DisplayName = d.DisplayName
		}
		if d.Class != "" {
			entry.Device.Class = d.Class
		}
		if d.Location != "" {
			entry.Device.Location = d.Location
		}
		if d.Identity.IEEEAddress != "" {
			entry.Device.Identity.IEEEAddress = d.Identity.IEEEAddress
		}
		if d.Identity.FriendlyName != "" {
			entry.Device.Identity.FriendlyName = d.Identity.FriendlyName
		}
		if d.SourceTopic != "" {
			entry.Device.SourceTopic = d.SourceTopic
		}
		entry.Device.Unclassified = d.Unclassified
	}
	if d.Identity.IEEEAddress != "" {
		s.byIEEE[d.Identity.IEEEAddress] = id
	}
	if d.Identity.FriendlyName != "" {
		s.byName[d.Identity.FriendlyName] = id
	}
}

// LookupID resolves identity (IEEE preferred, friendly name as
// fallback) to a stored device id. Returns "" if not found.
func (s *Store) LookupID(ieee, friendlyName string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ieee != "" {
		if id, ok := s.byIEEE[ieee]; ok {
			return id
		}
	}
	if friendlyName != "" {
		if id, ok := s.byName[friendlyName]; ok {
			return id
		}
	}
	return ""
}

// Rename moves the friendly-name index for a device from oldName to
// newName. No-op if the device is not known.
func (s *Store) Rename(id, oldName, newName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.dev[id]
	if !ok {
		return
	}
	if oldName != "" {
		delete(s.byName, oldName)
	}
	if newName != "" {
		s.byName[newName] = id
		entry.Device.Identity.FriendlyName = newName
	}
}

// Get returns a snapshot of the device record.
func (s *Store) Get(id string) (model.Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.dev[id]
	if !ok {
		return model.Device{}, false
	}
	return entry.Device, true
}

// Devices returns a sorted-by-id snapshot map of all devices.
func (s *Store) Devices() map[string]model.Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]model.Device, len(s.dev))
	for id, e := range s.dev {
		out[id] = e.Device
	}
	return out
}

// House returns the current whole-house state.
func (s *Store) House() model.House {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.house
}

// Snapshot returns the full point-in-time state.
func (s *Store) Snapshot() model.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := model.Snapshot{
		GeneratedAt: time.Now().UTC(),
		House:       s.house,
		Devices:     make(map[string]model.Device, len(s.dev)),
	}
	for id, e := range s.dev {
		out.Devices[id] = e.Device
	}
	return out
}

// withEntry is a helper that runs fn while holding the write lock.
// Used by the engine to mutate a single device's record.
func (s *Store) withEntry(id string, fn func(*deviceEntry)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.dev[id]
	if !ok {
		return false
	}
	fn(entry)
	return true
}

// setHouse atomically replaces the whole-house state. Returns true if
// the state value changed.
func (s *Store) setHouse(h model.House) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.house.State != h.State
	if changed {
		h.LastChanged = time.Now().UTC()
	} else {
		h.LastChanged = s.house.LastChanged
	}
	s.house = h
	return changed
}
