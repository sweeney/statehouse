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

// houseIdentity is the synthetic DeviceIdentity attached to canonical
// events produced by the whole-house electricity aggregator. The
// scheme is "house" and there is no protocol-stable primary, so the
// event carries the synthetic id only.
var houseIdentity = model.DeviceIdentity{Scheme: "house", Display: HouseDeviceID}

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
	cfgMu    sync.RWMutex
	cfg      config.Config
	resolver *device.Resolver
	store    *Store
	clock    testutil.Clock

	mu             sync.Mutex
	derivedSinks   []EventSink
	canonicalSinks []CanonicalSink

	elecMu                sync.Mutex
	grossIntegrator       *energy.Integrator
	monitoredIntegrator   *energy.Integrator
	unmonitoredIntegrator *energy.Integrator
	lastElecAt            time.Time
	startedAt             time.Time // session start; Since for SessionEnergy
	// Latest authoritative meter period totals, carried across recomputes
	// so plug-triggered recomputes don't drop them. Nil until a meter
	// reading supplies them.
	meterTodayKWh *float64
	meterWeekKWh  *float64
	meterMonthKWh *float64
}

type engineSnap struct {
	cfg      config.Config
	resolver *device.Resolver
}

func (e *Engine) snap() engineSnap {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return engineSnap{cfg: e.cfg, resolver: e.resolver}
}

// NewEngine constructs an engine with the given store and config.
func NewEngine(cfg config.Config, store *Store, clock testutil.Clock) *Engine {
	gap := cfg.Energy.MaxIntegrationGap
	return &Engine{
		cfg:                   cfg,
		resolver:              device.NewResolver(cfg),
		store:                 store,
		clock:                 clock,
		grossIntegrator:       energy.NewIntegrator(gap),
		monitoredIntegrator:   energy.NewIntegrator(gap),
		unmonitoredIntegrator: energy.NewIntegrator(gap),
		startedAt:             clock.Now(),
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
// Returns the engine-facing device id. Adapters call this whenever
// they have a stable identity to share (typically from a protocol
// discovery message like Z2M's bridge/devices), and again on every
// IngestReading.
func (e *Engine) EnsureDiscovered(identity model.DeviceIdentity, sourceTopic string) string {
	snap := e.snap()
	// Identity lookup first; preserves runtime state across renames
	// where the protocol-stable Primary key is stable.
	id := e.store.LookupID(identity)
	if id == "" {
		id = snap.resolver.ConfiguredID(identity)
		if id == "" {
			id = deriveID(identity)
		}
	}
	prof := snap.resolver.Resolve(identity)
	prof.ID = id
	dev := model.Device{
		ID:           id,
		DisplayName:  prof.DisplayName,
		Class:        prof.Class,
		Location:     prof.Location,
		Identity:     identity,
		Availability: model.AvailabilityUnknown,
		Activity:     model.Activity{State: model.ActivityUnknown},
		Unclassified: prof.Unclassified,
	}
	if _, exists := e.store.Get(id); !exists {
		rt := device.NewRuntime(prof, snap.cfg.Energy.MaxIntegrationGap)
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
		var rt *device.Runtime
		var prevDisplay string
		var wasPhantom bool
		e.store.withEntry(id, func(ent *deviceEntry) {
			rt = ent.Runtime
			if rt == nil || rt.Profile.Class != prof.Class {
				rt = device.NewRuntime(prof, e.cfg.Energy.MaxIntegrationGap)
				ent.Runtime = rt
			} else {
				rt.Profile = prof
			}
			prevDisplay = ent.Device.Identity.Display
			// A "phantom" record is one where Primary == Display — the
			// adapter fell back to using the friendly name as the primary
			// key before it learned the real IEEE address. Detect the
			// upgrade so we can purge any pre-discovery injected state.
			oldPrimary := ent.Device.Identity.Primary
			oldDisplay := ent.Device.Identity.Display
			wasPhantom = oldPrimary != "" && oldDisplay != "" && oldPrimary == oldDisplay &&
				identity.Primary != "" && identity.Primary != identity.Display
		})
		e.store.Upsert(id, dev, rt)
		// Detect display-name rename so the byDisplay index is rebuilt.
		if identity.Display != "" && prevDisplay != "" && identity.Display != prevDisplay {
			e.store.Rename(id, prevDisplay, identity.Display)
		}
		// Reset injected runtime state when a phantom display-only record
		// is upgraded to a real IEEE identity. Any Activity or Cycle that
		// was pre-seeded via the display-name fallback path cannot be
		// trusted as it may originate from a DoS-crafted topic.
		if wasPhantom {
			e.store.withEntry(id, func(ent *deviceEntry) {
				ent.Device.Activity = model.Activity{State: model.ActivityUnknown}
				ent.Device.Cycle = nil
			})
		}
	}
	return id
}

// IngestReading is the canonical entry point: feed one parsed device
// reading into the engine. Adapters call this with a normalised
// identity and Reading struct; the protocol-specific details have
// already been parsed away by the adapter.
func (e *Engine) IngestReading(identity model.DeviceIdentity, sourceTopic string, reading model.Reading) {
	snap := e.snap()
	id := e.EnsureDiscovered(identity, sourceTopic)
	if reading.Timestamp.IsZero() {
		reading.Timestamp = e.clock.Now()
	}
	e.emitCanonicalForReading(id, identity, sourceTopic, reading)

	var (
		outcome device.Outcome
		profile device.Profile
		// Snapshot of cycle state for divergence/stale-counter calc.
		divergencePct   float64
		divergenceHit   bool
		staleCounterHit bool
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
		// Accumulate all-time extremes for the measurements this device
		// reports. Copy-on-write (like Cycle below): readers get a shallow
		// Device copy that shares this *Lifetime pointer and dereference it
		// after the lock is released, so we must never mutate the pointee in
		// place. Swap in a fresh struct instead. The *Extremum pointees are
		// themselves immutable (Observe* always allocates), so a shallow copy
		// is sufficient. Allocated lazily so devices that never send a tracked
		// field carry no lifetime block at all.
		if reading.PowerW != nil || reading.TemperatureC != nil || reading.HumidityPct != nil {
			lt := &model.Lifetime{}
			if ent.Device.Lifetime != nil {
				*lt = *ent.Device.Lifetime
			}
			if reading.PowerW != nil {
				model.ObserveMax(&lt.MaxPower, *reading.PowerW, reading.Timestamp)
			}
			if reading.TemperatureC != nil {
				model.ObserveMin(&lt.MinTemperature, *reading.TemperatureC, reading.Timestamp)
				model.ObserveMax(&lt.MaxTemperature, *reading.TemperatureC, reading.Timestamp)
			}
			if reading.HumidityPct != nil {
				model.ObserveMin(&lt.MinHumidity, *reading.HumidityPct, reading.Timestamp)
				model.ObserveMax(&lt.MaxHumidity, *reading.HumidityPct, reading.Timestamp)
			}
			ent.Device.Lifetime = lt
		}
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
		if reading.PressureHPa != nil {
			v := *reading.PressureHPa
			l.PressureHPa = &v
		}
		if reading.WindSpeedMS != nil {
			v := *reading.WindSpeedMS
			l.WindSpeedMS = &v
		}
		if reading.WindDirDeg != nil {
			v := *reading.WindDirDeg
			l.WindDirDeg = &v
		}
		if reading.RainfallMM != nil {
			v := *reading.RainfallMM
			l.RainfallMM = &v
		}
		if reading.IlluminanceLux != nil {
			v := *reading.IlluminanceLux
			l.IlluminanceLux = &v
		}
		if reading.UVIndex != nil {
			v := *reading.UVIndex
			l.UVIndex = &v
		}
		if reading.BatteryRuntimeMins != nil {
			v := *reading.BatteryRuntimeMins
			l.BatteryRuntimeMins = &v
		}
		if reading.OnBattery != nil {
			v := *reading.OnBattery
			l.OnBattery = &v
		}
		if reading.LowBattery != nil {
			v := *reading.LowBattery
			l.LowBattery = &v
		}
		if reading.Battery != nil {
			v := *reading.Battery
			l.BatteryPct = &v
		}
		if reading.LinkQuality != nil {
			v := *reading.LinkQuality
			l.LinkQuality = &v
		}
		if reading.RSSI != nil {
			v := *reading.RSSI
			l.RSSI = &v
		}
		// Activity sub-state.
		if outcome.NewActivity != outcome.PrevActivity {
			now := reading.Timestamp
			ent.Device.Activity.Since = now
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
		// Divergence check on cycle completion. Only fire for counter-primary
		// devices: when the counter is the trusted source, a large gap from
		// integration signals a measurement quality issue worth alerting on.
		// For integration-primary devices the counter is already declared
		// untrustworthy (via per-device override or class config), so any
		// counter/integration mismatch is expected noise — not actionable.
		if outcome.CycleFinished && ent.Device.Cycle != nil &&
			ent.Device.Cycle.Energy.ReportedKWhDelta > 0 &&
			profile.Strategy == energy.StrategyCounter {
			pct := energy.DivergencePct(ent.Device.Cycle.Energy.ReportedKWhDelta, ent.Device.Cycle.Energy.IntegratedKWh)
			if pct >= snap.cfg.Energy.DivergenceWarningPct {
				ent.Runtime.MarkDivergence(pct)
				cc := *outcome.Cycle
				cc.Energy.DivergencePct = pct
				cc.Energy.DivergenceWarning = true
				ent.Device.Cycle = &cc
				divergencePct = pct
				divergenceHit = true
			}
		}
		// Stale counter check: counter-primary device finished a cycle
		// with no counter movement despite meaningful activity. The
		// counter is absent or stuck.
		const staleCounterMinIntegratedKWh = 0.001 // 1 Wh
		const staleCounterMinDurationS = 30
		if outcome.CycleFinished && ent.Device.Cycle != nil &&
			profile.Strategy == energy.StrategyCounter &&
			ent.Device.Cycle.Energy.ReportedKWhDelta == 0 &&
			(ent.Device.Cycle.Energy.IntegratedKWh >= staleCounterMinIntegratedKWh ||
				ent.Device.Cycle.DurationSeconds >= staleCounterMinDurationS) {
			ent.Runtime.MarkStaleCounter()
			cc := *outcome.Cycle
			cc.Energy.StaleCounter = true
			ent.Device.Cycle = &cc
			staleCounterHit = true
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
	if staleCounterHit {
		e.emitDerived(model.DerivedEvent{
			ID:          newEventID(),
			Timestamp:   reading.Timestamp,
			Type:        model.EvtEnergyStaleCounterWarning,
			DeviceID:    id,
			DeviceClass: profile.Class,
			Summary:     fmt.Sprintf("Stale energy counter for %s", id),
			Severity:    "warning",
			Evidence: map[string]any{
				"integrated_kwh":   outcome.Cycle.Energy.IntegratedKWh,
				"duration_seconds": outcome.Cycle.DurationSeconds,
			},
		})
	}

	// Re-derive whole-house state on every activity transition.
	if outcome.PrevActivity != outcome.NewActivity || outcome.CycleStarted || outcome.CycleFinished {
		e.RecomputeHouse()
	}

	if reading.PowerW != nil {
		isMeterTrigger := profile.Class == device.ClassEnergyMeter || identity.Scheme == meterScheme
		e.recomputeElectricity(reading.Timestamp, isMeterTrigger, sourceTopic, reading)
	}
}

// SetAvailability is called when an availability transition is
// reported by an adapter. Offline transitions are debounced; online
// transitions are immediate.
func (e *Engine) SetAvailability(identity model.DeviceIdentity, sourceTopic string, a model.Availability) {
	snap := e.snap()
	id := e.EnsureDiscovered(identity, sourceTopic)
	now := e.clock.Now()
	var (
		emitChange bool
		newAvail   model.Availability
		debounce   = snap.cfg.Availability.OfflineDebounce
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

// IngestSignal asserts an ActivitySignal. If a signal with the same ID
// already exists it is replaced (upsert). RecomputeHouse is called so
// house state reflects the change immediately.
func (e *Engine) IngestSignal(s model.ActivitySignal) {
	if s.Timestamp.IsZero() {
		s.Timestamp = e.clock.Now()
	}
	if s.Since.IsZero() {
		s.Since = s.Timestamp
	}
	e.store.signals.Upsert(s)
	e.emitDerived(model.DerivedEvent{
		ID:        newEventID(),
		Timestamp: s.Timestamp,
		Type:      model.EvtSignalAsserted,
		Summary:   s.Source + "/" + s.Type,
		Evidence: map[string]any{
			"signal_id": s.ID,
			"source":    s.Source,
			"type":      s.Type,
			"location":  s.Location,
			"meta":      s.Meta,
		},
	})
	e.RecomputeHouse()
}

// ClearSignal removes the signal with the given ID. No-op if not
// present. RecomputeHouse is called so house state reflects the change.
func (e *Engine) ClearSignal(id string, ts time.Time) {
	if ts.IsZero() {
		ts = e.clock.Now()
	}
	e.store.signals.ClearAt(id, ts)
	e.emitDerived(model.DerivedEvent{
		ID:        newEventID(),
		Timestamp: ts,
		Type:      model.EvtSignalCleared,
		Evidence:  map[string]any{"signal_id": id},
	})
	e.RecomputeHouse()
}

// ReloadConfig replaces the engine's active config and resolver with a fresh
// copy, then re-applies profiles to all known devices so callers see the new
// classification and thresholds without a full restart. Safe to call from any
// goroutine; concurrent reads take a short read-lock.
func (e *Engine) ReloadConfig(cfg config.Config) {
	e.cfgMu.Lock()
	e.cfg = cfg
	e.resolver = device.NewResolver(cfg)
	e.cfgMu.Unlock()

	// Re-discover each known device under the new resolver so its
	// profile is updated immediately.
	devices := e.store.Devices()
	for _, dev := range devices {
		e.EnsureDiscovered(dev.Identity, "")
	}
}

// RecordActivity appends an ActivityRecord to the recent-activity log.
func (e *Engine) RecordActivity(r model.ActivityRecord) {
	e.store.AppendActivity(r)
}

// UpdateActivity updates the most recent ActivityRecord with the given ID.
func (e *Engine) UpdateActivity(id string, fn func(*model.ActivityRecord)) {
	e.store.UpdateActivity(id, fn)
}

// Tick is meant to be called by a periodic ticker. It applies any
// time-driven transitions (e.g. offline debounce maturing,
// finished_recently TTL decay) and re-derives whole-house state.
func (e *Engine) Tick() {
	snap := e.snap()
	now := e.clock.Now()
	debounce := snap.cfg.Availability.OfflineDebounce
	type maturedAvail struct {
		id   string
		from model.Availability
	}
	type maturedActivity struct {
		id      string
		profile device.Profile
		outcome device.Outcome
	}
	var availChanged []maturedAvail
	var activityChanged []maturedActivity
	devices := e.store.Devices()
	for id := range devices {
		e.store.withEntry(id, func(ent *deviceEntry) {
			if ent.availabilityOfflineAt != nil &&
				ent.Device.Availability == model.AvailabilityOfflinePending &&
				now.Sub(*ent.availabilityOfflineAt) >= debounce {
				from := ent.Device.Availability
				ent.Device.Availability = model.AvailabilityOffline
				availChanged = append(availChanged, maturedAvail{id: id, from: from})
			}
			if ent.Runtime != nil {
				tickOut := ent.Runtime.Tick(now)
				if tickOut.PrevActivity != tickOut.NewActivity {
					ent.Device.Activity.State = tickOut.NewActivity
					ent.Device.Activity.LastChanged = now
					ent.Device.Activity.Since = now
					activityChanged = append(activityChanged, maturedActivity{
						id:      id,
						profile: ent.Runtime.Profile,
						outcome: tickOut,
					})
				}
			}
		})
	}
	for _, c := range availChanged {
		e.emitDerived(model.DerivedEvent{
			ID:        newEventID(),
			Timestamp: now,
			Type:      model.EvtDeviceAvailabilityChanged,
			DeviceID:  c.id,
			Evidence:  map[string]any{"availability": string(model.AvailabilityOffline)},
		})
	}
	for _, c := range activityChanged {
		e.emitActivityChange(c.id, c.profile, c.outcome, now)
	}
	e.store.signals.Prune(now)
	e.RecomputeHouse()
}

// RecomputeHouse derives a conservative whole-house state from the
// current device state and notifies sinks on change.
func (e *Engine) RecomputeHouse() {
	snap := e.snap()
	now := e.clock.Now()
	house := DeriveHouseState(now, snap.cfg.House, e.store.Devices(), e.store.ActiveSignals(now), e.store.LastSignalAt())
	changed := e.store.setHouse(house)
	if changed {
		e.emitDerived(model.DerivedEvent{
			ID:        newEventID(),
			Timestamp: now,
			Type:      model.EvtHouseStateChanged,
			Summary:   fmt.Sprintf("House state -> occupancy:%s activity:%s mode:%s", house.Occupancy.State, house.Activity.State, house.Mode.State),
			Evidence: map[string]any{
				"occupancy":            string(house.Occupancy.State),
				"occupancy_confidence": house.Occupancy.Confidence,
				"activity":             string(house.Activity.State),
				"activity_confidence":  house.Activity.Confidence,
				"mode":                 string(house.Mode.State),
				"mode_confidence":      house.Mode.Confidence,
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
	case device.ClassBinaryState:
		t = model.EvtCycleStarted
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
	case device.ClassBinaryState:
		t = model.EvtCycleFinished
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

func (e *Engine) emitCanonicalForReading(id string, identity model.DeviceIdentity, sourceTopic string, r model.Reading) {
	e.mu.Lock()
	sinks := append([]CanonicalSink(nil), e.canonicalSinks...)
	e.mu.Unlock()
	if len(sinks) == 0 {
		return
	}
	emit := func(cap, attr string, value any, unit string) {
		ev := model.CanonicalEvent{
			Timestamp:   r.Timestamp,
			Source:      identity.Scheme,
			SourceTopic: sourceTopic,
			DeviceID:    id,
			Identity:    identity,
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
	if r.Battery != nil {
		emit("battery", "battery_pct", *r.Battery, "%")
	}
	if r.PressureHPa != nil {
		emit("environment", "pressure_hpa", *r.PressureHPa, "hPa")
	}
	if r.WindSpeedMS != nil {
		emit("environment", "wind_speed_ms", *r.WindSpeedMS, "m/s")
	}
	if r.WindDirDeg != nil {
		emit("environment", "wind_dir_deg", *r.WindDirDeg, "deg")
	}
	if r.RainfallMM != nil {
		emit("environment", "rainfall_mm", *r.RainfallMM, "mm")
	}
	if r.IlluminanceLux != nil {
		emit("environment", "illuminance_lux", *r.IlluminanceLux, "lux")
	}
	if r.UVIndex != nil {
		emit("environment", "uv_index", *r.UVIndex, "")
	}
	if r.BatteryRuntimeMins != nil {
		emit("ups", "battery_runtime_mins", *r.BatteryRuntimeMins, "min")
	}
	if r.OnBattery != nil {
		emit("ups", "on_battery", *r.OnBattery, "")
	}
	if r.LowBattery != nil {
		emit("ups", "low_battery", *r.LowBattery, "")
	}
	if r.RSSI != nil {
		emit("radio", "rssi_dbm", *r.RSSI, "dBm")
	}
}

// deriveID generates a stable engine-facing id when none is
// configured. Display name is preferred (human-meaningful) when
// present; otherwise we fall back to the scheme-specific primary key
// with a "dev_" prefix and any "0x" prefix stripped for readability.
func deriveID(identity model.DeviceIdentity) string {
	if identity.Display != "" {
		return identity.Display
	}
	primary := identity.Primary
	if len(primary) > 2 && (primary[:2] == "0x" || primary[:2] == "0X") {
		primary = primary[2:]
	}
	if primary != "" {
		return "dev_" + primary
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
