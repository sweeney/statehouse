package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/energy"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// EventSink consumes derived events. Implementations: MQTT publisher,
// JSONL log writer, Influx writer.
type EventSink interface {
	OnDerivedEvent(model.DerivedEvent)
}

// CanonicalSink consumes canonical events (one per raw reading).
type CanonicalSink interface {
	OnCanonicalEvent(model.CanonicalEvent)
}

// Engine is the orchestrator: it normalises readings, drives device
// state machines, derives whole-house state, and emits derived
// events to all registered sinks.
type Engine struct {
	cfg      config.Config
	resolver *device.Resolver
	store    *Store
	clock    testutil.Clock

	mu             sync.Mutex
	derivedSinks   []EventSink
	canonicalSinks []CanonicalSink
}

// NewEngine constructs an engine with the given store and config.
func NewEngine(cfg config.Config, store *Store, clock testutil.Clock) *Engine {
	return &Engine{
		cfg:      cfg,
		resolver: device.NewResolver(cfg),
		store:    store,
		clock:    clock,
	}
}

// AddDerivedSink registers a sink for derived events. Sinks are
// invoked synchronously in registration order.
func (e *Engine) AddDerivedSink(s EventSink) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.derivedSinks = append(e.derivedSinks, s)
}

// AddCanonicalSink registers a sink for canonical events.
func (e *Engine) AddCanonicalSink(s CanonicalSink) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.canonicalSinks = append(e.canonicalSinks, s)
}

// EnsureDiscovered idempotently registers a device with the engine.
// Returns the engine-facing device id.
func (e *Engine) EnsureDiscovered(ieee, friendlyName, sourceTopic string) string {
	// Identity lookup first; this preserves runtime state across
	// friendly-name renames where IEEE is stable.
	id := e.store.LookupID(ieee, friendlyName)
	if id == "" {
		id = e.resolver.ConfiguredID(ieee, friendlyName)
		if id == "" {
			id = deriveID(ieee, friendlyName)
		}
	}
	prof := e.resolver.Resolve(ieee, friendlyName)
	prof.ID = id
	dev := model.Device{
		ID:           id,
		DisplayName:  prof.DisplayName,
		Class:        prof.Class,
		Location:     prof.Location,
		Identity:     model.DeviceIdentity{IEEEAddress: ieee, FriendlyName: friendlyName},
		SourceTopic:  sourceTopic,
		Availability: model.AvailabilityUnknown,
		Activity:     model.Activity{State: model.ActivityUnknown},
		Unclassified: prof.Unclassified,
	}
	// If we already have a record, this preserves runtime state via
	// Store.Upsert. Otherwise, create a runtime now.
	if _, exists := e.store.Get(id); !exists {
		rt := device.NewRuntime(prof, e.cfg.Energy.MaxIntegrationGap)
		e.store.Upsert(id, dev, rt)
		e.emitDerived(model.DerivedEvent{
			ID:          newEventID(),
			Timestamp:   e.clock.Now(),
			Type:        model.EvtDeviceDiscovered,
			DeviceID:    id,
			DeviceClass: prof.Class,
			Summary:     fmt.Sprintf("Discovered device %s", id),
		})
	} else {
		// Refresh identity/metadata but reuse runtime.
		var rt *device.Runtime
		e.store.withEntry(id, func(ent *deviceEntry) {
			rt = ent.Runtime
			// If runtime class differs (e.g. config change), rebuild.
			if rt == nil || rt.Profile.Class != prof.Class {
				rt = device.NewRuntime(prof, e.cfg.Energy.MaxIntegrationGap)
				ent.Runtime = rt
			}
		})
		e.store.Upsert(id, dev, rt)
		// Detect friendly-name rename for the same id.
		if existing, ok := e.store.Get(id); ok && existing.Identity.FriendlyName != friendlyName && friendlyName != "" {
			e.store.Rename(id, existing.Identity.FriendlyName, friendlyName)
		}
	}
	return id
}

// IngestReading is the canonical entry point: feed one parsed device
// reading from one source topic into the engine.
func (e *Engine) IngestReading(ieee, friendlyName, sourceTopic string, reading model.Reading) {
	id := e.EnsureDiscovered(ieee, friendlyName, sourceTopic)
	if reading.Timestamp.IsZero() {
		reading.Timestamp = e.clock.Now()
	}
	e.emitCanonicalForReading(id, ieee, friendlyName, sourceTopic, reading)

	var (
		outcome device.Outcome
		profile device.Profile
		// Snapshot of cycle state for divergence calc.
		divergencePct float64
		divergenceHit bool
	)
	e.store.withEntry(id, func(ent *deviceEntry) {
		profile = ent.Runtime.Profile
		// Mark availability online if any data arrives — the device is
		// clearly speaking.
		if ent.Device.Availability != model.AvailabilityOnline {
			ent.Device.Availability = model.AvailabilityOnline
			ent.availabilityOfflineAt = nil
		}
		// Seed counter baseline before any activity so first cycle has
		// a baseline. SeedCounter is a no-op while a cycle is active.
		if reading.EnergyKWh != nil {
			ent.Runtime.SeedCounter(*reading.EnergyKWh)
		}
		outcome = ent.Runtime.OnReading(reading.Timestamp, reading)
		// Push latest measurements onto the public record.
		l := &ent.Device.Latest
		l.LastSeen = reading.Timestamp
		if reading.PowerW != nil {
			v := *reading.PowerW
			l.PowerW = &v
		}
		if reading.VoltageV != nil {
			v := *reading.VoltageV
			l.VoltageV = &v
		}
		if reading.EnergyKWh != nil {
			v := *reading.EnergyKWh
			l.EnergyKWh = &v
		}
		if reading.TemperatureC != nil {
			v := *reading.TemperatureC
			l.TemperatureC = &v
		}
		if reading.HumidityPct != nil {
			v := *reading.HumidityPct
			l.HumidityPct = &v
		}
		if reading.LinkQuality != nil {
			v := *reading.LinkQuality
			l.LinkQuality = &v
		}
		// Activity sub-state.
		if outcome.NewActivity != outcome.PrevActivity {
			now := reading.Timestamp
			if outcome.PrevActivity == model.ActivityUnknown {
				ent.Device.Activity.Since = now
			} else {
				ent.Device.Activity.Since = now
			}
			ent.Device.Activity.State = outcome.NewActivity
			ent.Device.Activity.LastChanged = now
			ent.Device.Activity.Confidence = 0.9
		} else if outcome.NewActivity != model.ActivityUnknown && ent.Device.Activity.State == model.ActivityUnknown {
			ent.Device.Activity.State = outcome.NewActivity
			ent.Device.Activity.LastChanged = reading.Timestamp
			ent.Device.Activity.Since = reading.Timestamp
			ent.Device.Activity.Confidence = 0.9
		}
		// Cycle copy.
		if outcome.Cycle != nil {
			cc := *outcome.Cycle
			ent.Device.Cycle = &cc
		}
		// Divergence check on cycle completion.
		if outcome.CycleFinished && ent.Device.Cycle != nil {
			pct := energy.DivergencePct(ent.Device.Cycle.Energy.ReportedKWhDelta, ent.Device.Cycle.Energy.IntegratedKWh)
			if pct >= e.cfg.Energy.DivergenceWarningPct {
				ent.Runtime.MarkDivergence(pct)
				cc := *outcome.Cycle
				cc.Energy.DivergencePct = pct
				cc.Energy.DivergenceWarning = true
				ent.Device.Cycle = &cc
				divergencePct = pct
				divergenceHit = true
			}
		}
	})

	if outcome.PrevActivity != outcome.NewActivity {
		e.emitActivityChange(id, profile, outcome, reading.Timestamp)
	}
	if outcome.CycleStarted {
		e.emitCycleStarted(id, profile, reading.Timestamp)
	}
	if outcome.CycleFinished {
		e.emitCycleFinished(id, profile, reading.Timestamp, outcome.Cycle)
	}
	if divergenceHit {
		e.emitDerived(model.DerivedEvent{
			ID:          newEventID(),
			Timestamp:   reading.Timestamp,
			Type:        model.EvtEnergyDivergenceWarning,
			DeviceID:    id,
			DeviceClass: profile.Class,
			Summary:     fmt.Sprintf("Energy divergence %.0f%% for %s", divergencePct, id),
			Severity:    "warning",
			Evidence: map[string]any{
				"reported_kwh_delta": outcome.Cycle.Energy.ReportedKWhDelta,
				"integrated_kwh":     outcome.Cycle.Energy.IntegratedKWh,
				"selected_source":    outcome.Cycle.Energy.PrimarySource,
				"divergence_pct":     divergencePct,
			},
		})
	}

	// Re-derive whole-house state on every activity transition.
	if outcome.PrevActivity != outcome.NewActivity || outcome.CycleStarted || outcome.CycleFinished {
		e.RecomputeHouse()
	}
}

// SetAvailability is called when an availability MQTT message arrives.
// Offline transitions are debounced; online transitions are immediate.
func (e *Engine) SetAvailability(ieee, friendlyName, sourceTopic string, a model.Availability) {
	id := e.EnsureDiscovered(ieee, friendlyName, sourceTopic)
	now := e.clock.Now()
	var (
		emitChange bool
		newAvail   model.Availability
		debounce   = e.cfg.Availability.OfflineDebounce
	)
	e.store.withEntry(id, func(ent *deviceEntry) {
		switch a {
		case model.AvailabilityOnline:
			if ent.Device.Availability != model.AvailabilityOnline {
				ent.Device.Availability = model.AvailabilityOnline
				ent.availabilityOfflineAt = nil
				emitChange = true
				newAvail = model.AvailabilityOnline
			}
		case model.AvailabilityOffline:
			if debounce > 0 {
				if ent.availabilityOfflineAt == nil {
					t := now
					ent.availabilityOfflineAt = &t
					if ent.Device.Availability != model.AvailabilityOfflinePending {
						ent.Device.Availability = model.AvailabilityOfflinePending
						emitChange = true
						newAvail = model.AvailabilityOfflinePending
					}
				} else if now.Sub(*ent.availabilityOfflineAt) >= debounce {
					if ent.Device.Availability != model.AvailabilityOffline {
						ent.Device.Availability = model.AvailabilityOffline
						emitChange = true
						newAvail = model.AvailabilityOffline
					}
				}
			} else {
				if ent.Device.Availability != model.AvailabilityOffline {
					ent.Device.Availability = model.AvailabilityOffline
					emitChange = true
					newAvail = model.AvailabilityOffline
				}
			}
		}
	})
	if emitChange {
		e.emitDerived(model.DerivedEvent{
			ID:        newEventID(),
			Timestamp: now,
			Type:      model.EvtDeviceAvailabilityChanged,
			DeviceID:  id,
			Evidence:  map[string]any{"availability": string(newAvail)},
		})
	}
}

// Tick is meant to be called by a periodic ticker. It applies any
// time-driven transitions (e.g. offline debounce maturing) and
// re-derives whole-house state.
func (e *Engine) Tick() {
	now := e.clock.Now()
	debounce := e.cfg.Availability.OfflineDebounce
	type matured struct {
		id   string
		from model.Availability
	}
	var changed []matured
	devices := e.store.Devices()
	for id := range devices {
		e.store.withEntry(id, func(ent *deviceEntry) {
			if ent.availabilityOfflineAt == nil {
				return
			}
			if ent.Device.Availability == model.AvailabilityOfflinePending && now.Sub(*ent.availabilityOfflineAt) >= debounce {
				from := ent.Device.Availability
				ent.Device.Availability = model.AvailabilityOffline
				changed = append(changed, matured{id: id, from: from})
			}
		})
	}
	for _, c := range changed {
		e.emitDerived(model.DerivedEvent{
			ID:        newEventID(),
			Timestamp: now,
			Type:      model.EvtDeviceAvailabilityChanged,
			DeviceID:  c.id,
			Evidence:  map[string]any{"availability": string(model.AvailabilityOffline)},
		})
	}
	e.RecomputeHouse()
}

// RecomputeHouse derives a conservative whole-house state from the
// current device state and notifies sinks on change.
func (e *Engine) RecomputeHouse() {
	now := e.clock.Now()
	house := DeriveHouseState(now, e.cfg.House, e.store.Devices())
	changed := e.store.setHouse(house)
	if changed {
		e.emitDerived(model.DerivedEvent{
			ID:        newEventID(),
			Timestamp: now,
			Type:      model.EvtHouseStateChanged,
			Summary:   fmt.Sprintf("House state -> %s", house.State),
			Evidence: map[string]any{
				"state":      string(house.State),
				"confidence": house.Confidence,
				"signals":    house.Signals,
			},
		})
	}
}

func (e *Engine) emitActivityChange(id string, profile device.Profile, outcome device.Outcome, ts time.Time) {
	e.emitDerived(model.DerivedEvent{
		ID:          newEventID(),
		Timestamp:   ts,
		Type:        model.EvtDeviceActivityChanged,
		DeviceID:    id,
		DeviceClass: profile.Class,
		Summary:     fmt.Sprintf("%s -> %s", outcome.PrevActivity, outcome.NewActivity),
		Evidence: map[string]any{
			"from": string(outcome.PrevActivity),
			"to":   string(outcome.NewActivity),
		},
	})
	switch profile.Class {
	case device.ClassMedia:
		if outcome.NewActivity == model.ActivityActive {
			e.emitDerived(model.DerivedEvent{ID: newEventID(), Timestamp: ts, Type: model.EvtMediaActive, DeviceID: id, DeviceClass: profile.Class})
		}
		if outcome.NewActivity == model.ActivityStandby {
			e.emitDerived(model.DerivedEvent{ID: newEventID(), Timestamp: ts, Type: model.EvtMediaInactive, DeviceID: id, DeviceClass: profile.Class})
		}
	}
}

func (e *Engine) emitCycleStarted(id string, profile device.Profile, ts time.Time) {
	t := model.EvtDeviceActivityStarted
	switch profile.Class {
	case device.ClassCyclePower:
		t = model.EvtCycleStarted
	case device.ClassContinuous:
		t = model.EvtContinuousCycleStarted
	}
	e.emitDerived(model.DerivedEvent{
		ID: newEventID(), Timestamp: ts, Type: t,
		DeviceID: id, DeviceClass: profile.Class,
		Summary: fmt.Sprintf("%s started", id),
	})
}

func (e *Engine) emitCycleFinished(id string, profile device.Profile, ts time.Time, cycle *model.Cycle) {
	t := model.EvtDeviceActivityFinished
	switch profile.Class {
	case device.ClassCyclePower:
		t = model.EvtCycleFinished
	case device.ClassContinuous:
		t = model.EvtContinuousCycleFinished
	}
	ev := model.DerivedEvent{
		ID: newEventID(), Timestamp: ts, Type: t,
		DeviceID: id, DeviceClass: profile.Class,
		Summary: fmt.Sprintf("%s finished", id),
	}
	if cycle != nil {
		ev.Evidence = map[string]any{
			"duration_seconds":    cycle.DurationSeconds,
			"selected_energy_kwh": cycle.Energy.SelectedKWh,
			"energy_source":       cycle.Energy.PrimarySource,
			"reported_kwh_delta":  cycle.Energy.ReportedKWhDelta,
			"integrated_kwh":      cycle.Energy.IntegratedKWh,
		}
	}
	e.emitDerived(ev)
	if cycle != nil && profile.Class == device.ClassCyclePower {
		e.emitDerived(model.DerivedEvent{
			ID: newEventID(), Timestamp: ts, Type: model.EvtCycleEnergyRecorded,
			DeviceID: id, DeviceClass: profile.Class,
			Evidence: ev.Evidence,
		})
	}
	// Short-burst classification gets a richer marker too.
	if profile.Class == device.ClassShortBurst && cycle != nil && cycle.DurationSeconds <= 300 {
		e.emitDerived(model.DerivedEvent{
			ID: newEventID(), Timestamp: ts, Type: model.EvtShortBurstDetected,
			DeviceID: id, DeviceClass: profile.Class,
			Evidence: ev.Evidence,
		})
	}
}

func (e *Engine) emitDerived(ev model.DerivedEvent) {
	e.mu.Lock()
	sinks := append([]EventSink(nil), e.derivedSinks...)
	e.mu.Unlock()
	for _, s := range sinks {
		s.OnDerivedEvent(ev)
	}
}

func (e *Engine) emitCanonicalForReading(id, ieee, friendlyName, sourceTopic string, r model.Reading) {
	e.mu.Lock()
	sinks := append([]CanonicalSink(nil), e.canonicalSinks...)
	e.mu.Unlock()
	if len(sinks) == 0 {
		return
	}
	emit := func(cap, attr string, value any, unit string) {
		ev := model.CanonicalEvent{
			Timestamp:   r.Timestamp,
			Source:      "mqtt",
			SourceTopic: sourceTopic,
			DeviceID:    id,
			Identity:    model.DeviceIdentity{IEEEAddress: ieee, FriendlyName: friendlyName},
			Capability:  cap,
			Attribute:   attr,
			Value:       value,
			Unit:        unit,
		}
		if r.LinkQuality != nil {
			ev.Quality = map[string]any{"linkquality": *r.LinkQuality}
		}
		for _, s := range sinks {
			s.OnCanonicalEvent(ev)
		}
	}
	if r.PowerW != nil {
		emit("power", "power_w", *r.PowerW, "W")
	}
	if r.VoltageV != nil {
		emit("power", "voltage_v", *r.VoltageV, "V")
	}
	if r.CurrentA != nil {
		emit("power", "current_a", *r.CurrentA, "A")
	}
	if r.EnergyKWh != nil {
		emit("energy", "energy_kwh", *r.EnergyKWh, "kWh")
	}
	if r.State != nil {
		emit("switch", "state", *r.State, "")
	}
	if r.TemperatureC != nil {
		emit("temperature", "temperature_c", *r.TemperatureC, "C")
	}
	if r.HumidityPct != nil {
		emit("humidity", "humidity_pct", *r.HumidityPct, "%")
	}
}

// deriveID generates a stable engine-facing id when none is
// configured. Prefer the IEEE address (compact form, no 0x prefix);
// fall back to the friendly name.
func deriveID(ieee, friendlyName string) string {
	if friendlyName != "" {
		return friendlyName
	}
	if len(ieee) > 2 && (ieee[:2] == "0x" || ieee[:2] == "0X") {
		return "dev_" + ieee[2:]
	}
	if ieee != "" {
		return "dev_" + ieee
	}
	return "dev_unknown"
}

func newEventID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	return "evt_" + hex.EncodeToString(b[:])
}
