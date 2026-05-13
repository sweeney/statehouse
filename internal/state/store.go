package state

import (
	"sync"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

// Store holds the in-memory state of all known devices plus the
// current whole-house state. Access is serialised through an RWMutex.
//
// Devices are indexed by two scheme-aware keys:
//
//   - byPrimary maps "scheme:primary" (e.g. "zigbee:0x00158d...") to
//     the engine-facing device id. This is the stable canonical index.
//   - byDisplay maps "scheme:display" (e.g. "zigbee:kitchen_kettle")
//     to the device id. It exists for the case where a device's
//     payload arrives before its protocol-stable id is known — the
//     adapter falls back to Primary=Display, and the engine merges
//     the records when the real Primary is later learned.
type Store struct {
	mu        sync.RWMutex
	dev       map[string]*deviceEntry
	byPrimary map[string]string
	byDisplay map[string]string
	house     model.House
}

type deviceEntry struct {
	Device  model.Device
	Runtime *device.Runtime

	// availabilityOfflineAt is non-nil once the adapter signalled
	// offline; it stays in offline_pending until the debounce elapses.
	availabilityOfflineAt *time.Time
}

// NewStore returns an empty store with whole-house state = unknown.
func NewStore() *Store {
	return &Store{
		dev:       make(map[string]*deviceEntry),
		byPrimary: make(map[string]string),
		byDisplay: make(map[string]string),
		house:     model.House{State: model.HouseUnknown},
	}
}

// Upsert installs a device record at id with the given runtime. It is
// idempotent: subsequent calls update identity/profile metadata
// without resetting runtime state. Empty identity fields don't
// overwrite previously-known non-empty ones — a device payload that
// arrives without the canonical Primary keeps the IEEE we already
// learned from bridge/devices.
func (s *Store) Upsert(id string, d model.Device, rt *device.Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.dev[id]
	if !ok {
		entry = &deviceEntry{Device: d, Runtime: rt}
		s.dev[id] = entry
	} else {
		if d.DisplayName != "" {
			entry.Device.DisplayName = d.DisplayName
		}
		if d.Class != "" {
			entry.Device.Class = d.Class
		}
		if d.Location != "" {
			entry.Device.Location = d.Location
		}
		if d.Identity.Scheme != "" {
			entry.Device.Identity.Scheme = d.Identity.Scheme
		}
		if d.Identity.Primary != "" {
			entry.Device.Identity.Primary = d.Identity.Primary
		}
		if d.Identity.Display != "" {
			entry.Device.Identity.Display = d.Identity.Display
		}
		entry.Device.Unclassified = d.Unclassified
	}
	// Refresh indexes from the merged identity, not from the raw input
	// — that way a partial Upsert doesn't drop indexes set earlier.
	merged := entry.Device.Identity
	if merged.Scheme != "" && merged.Primary != "" {
		s.byPrimary[merged.Scheme+":"+merged.Primary] = id
	}
	if merged.Scheme != "" && merged.Display != "" {
		s.byDisplay[merged.Scheme+":"+merged.Display] = id
	}
}

// LookupID resolves an identity to a stored device id. It checks the
// canonical "scheme:primary" index first, then "scheme:display" as a
// fallback. Returns "" if no record matches.
func (s *Store) LookupID(identity model.DeviceIdentity) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if identity.Scheme != "" && identity.Primary != "" {
		if id, ok := s.byPrimary[identity.Scheme+":"+identity.Primary]; ok {
			return id
		}
	}
	if identity.Scheme != "" && identity.Display != "" {
		if id, ok := s.byDisplay[identity.Scheme+":"+identity.Display]; ok {
			return id
		}
	}
	return ""
}

// Rename updates the display-name index for a device. No-op if the
// device is not known. Adapters call this when an upstream protocol
// renames a device (e.g. a Z2M friendly_name change).
func (s *Store) Rename(id, oldDisplay, newDisplay string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.dev[id]
	if !ok {
		return
	}
	scheme := entry.Device.Identity.Scheme
	if oldDisplay != "" && scheme != "" {
		delete(s.byDisplay, scheme+":"+oldDisplay)
	}
	if newDisplay != "" && scheme != "" {
		s.byDisplay[scheme+":"+newDisplay] = id
		entry.Device.Identity.Display = newDisplay
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

// Devices returns a snapshot map of all devices.
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
