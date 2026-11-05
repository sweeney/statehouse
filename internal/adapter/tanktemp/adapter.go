// Package tanktemp is the adapter for sweeney/tanktemp, which reads 1-Wire
// temperature probes fitted to a hot water cylinder. The contract is
// tanktemp's docs/WIRE.md (normative); docs/STATEHOUSE.md says what this
// adapter does with it.
//
// tanktemp publishes two retained topics:
//
//	{base}/{device_id}/state — every 30 s, one message per cylinder, listing
//	                           every configured probe bottom first, each with
//	                           a calibrated temp_c or null and an error code
//	{base}/status            — STARTUP, HEARTBEAT, SHUTDOWN, and the LWT
//	                           {"online":false,"event":"LWT"}
//
// A probe, not the cylinder, is the engine device: scheme "tanktemp",
// primary "<device_id>.<position>". The identity is the position, not the
// probe's ROM, so a probe replaced at the same height continues its series.
// The cylinder itself is a telemetry: false entry in the devices namespace
// and never becomes an engine device.
//
// What this adapter deliberately does not do:
//   - write anything for a null temp_c (never 0);
//   - accept a retained state older than staleAfter, or one whose clock was
//     unsynced, since the retained flag says nothing about age;
//   - write the same sweep twice (a repeated timestamp is a replay);
//   - create devices from the status topic's publisher health (sensors_ok,
//     unexpected, startup_problems).
//
// Per-probe liveness comes from the state messages, not the status topic: a
// probe that keeps reporting an error stops producing readings and goes
// stale like any other passive sensor, even while tanktemp is online.
package tanktemp

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sweeney/statehouse/internal/adapter/timeutil"
	"github.com/sweeney/statehouse/internal/adapter/validate"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// SchemeName is the canonical scheme this adapter stamps on identities.
const SchemeName = "tanktemp"

// DefaultBase is tanktemp's production topic prefix.
const DefaultBase = "energy/hot_water"

// staleAfter is how old a retained state may be and still be taken as the
// current value: three of tanktemp's 30 s sweeps.
const staleAfter = 90 * time.Second

// The DS18B20's rated range. A value outside it is not a reading.
const (
	minTempC = -55.0
	maxTempC = 125.0
)

var (
	reDeviceID = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	rePosition = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// Adapter implements adapter.Adapter for tanktemp's two topics.
type Adapter struct {
	engine *state.Engine
	base   string
	logger *slog.Logger

	mu sync.Mutex
	// lastSweep is the newest sweep timestamp accepted per cylinder, so a
	// replayed message is never written twice.
	lastSweep map[string]time.Time
	// probes is every probe identity seen, keyed by primary, so the status
	// topic can mark them all offline.
	probes map[string]model.DeviceIdentity
	// roms is the ROM last seen under each primary, to notice a swap.
	roms map[string]string
}

// New returns an Adapter for the given base topic (typically
// "energy/hot_water"). Logger may be nil.
func New(engine *state.Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = DefaultBase
	}
	return &Adapter{
		engine:    engine,
		base:      base,
		logger:    logger,
		lastSweep: make(map[string]time.Time),
		probes:    make(map[string]model.DeviceIdentity),
		roms:      make(map[string]string),
	}
}

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return SchemeName }

// Subscriptions implements adapter.Adapter. Exactly these two patterns, not
// {base}/#, so a future topic doesn't arrive unannounced.
func (a *Adapter) Subscriptions() []string {
	return []string{a.base + "/+/state", a.base + "/status"}
}

// HandleMessage implements adapter.Adapter.
func (a *Adapter) HandleMessage(topic string, payload []byte, retained bool) {
	if a == nil || a.engine == nil || len(payload) == 0 {
		return
	}
	if topic == a.base+"/status" {
		a.handleStatus(topic, payload)
		return
	}
	if id, ok := a.stateDeviceID(topic); ok {
		a.handleState(id, topic, payload, retained)
	}
}

// stateDeviceID extracts device_id from {base}/{device_id}/state.
func (a *Adapter) stateDeviceID(topic string) (string, bool) {
	rest, ok := strings.CutPrefix(topic, a.base+"/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/state")
	if !ok || !reDeviceID.MatchString(id) {
		return "", false
	}
	return id, true
}

type statePayload struct {
	DeviceID    string   `json:"device_id"`
	Timestamp   string   `json:"timestamp"`
	ClockSynced *bool    `json:"clock_synced"`
	Sensors     []sensor `json:"sensors"`
}

type sensor struct {
	Position string   `json:"position"`
	ROM      string   `json:"rom"`
	TempC    *float64 `json:"temp_c"`
	Error    *string  `json:"error"`
	Suspect  *string  `json:"suspect"`
}

func (a *Adapter) handleState(deviceID, topic string, payload []byte, retained bool) {
	var p statePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		a.debug("tanktemp state parse failed", "topic", topic, "error", err)
		return
	}
	if p.DeviceID != "" && p.DeviceID != deviceID {
		a.warn("tanktemp state names a different cylinder than its topic; dropped",
			"topic", topic, "device_id", p.DeviceID)
		return
	}

	// now comes from the engine, never the wall clock, so the reading shares a
	// time base with the engine state derived from it.
	now := a.engine.Now()
	ts, ok := a.sweepTime(p, now, retained, topic)

	// Register every probe, readable or not, so a probe that has never read
	// still shows up (as unknown) rather than being invisible.
	idents := make([]model.DeviceIdentity, 0, len(p.Sensors))
	for _, s := range p.Sensors {
		if !rePosition.MatchString(s.Position) {
			a.debug("tanktemp sensor with bad position skipped", "topic", topic, "position", s.Position)
			idents = append(idents, model.DeviceIdentity{})
			continue
		}
		id := a.remember(deviceID, s)
		a.engine.EnsureDiscovered(id, topic)
		idents = append(idents, id)
	}
	if !ok || !a.newSweep(deviceID, ts) {
		return
	}

	for i, s := range p.Sensors {
		id := idents[i]
		if id.Primary == "" {
			continue
		}
		if s.TempC == nil {
			// Write nothing, never 0. The probe goes stale if this persists.
			a.debug("tanktemp probe has no value", "probe", id.Primary, "error", deref(s.Error))
			continue
		}
		if !validate.FiniteInRange(*s.TempC, minTempC, maxTempC) {
			a.warn("adapter: rejected out-of-range field", "field", "temperature_c",
				"value", *s.TempC, "probe", id.Primary, "topic", topic)
			continue
		}
		if s.Suspect != nil {
			// Published, and probably real (a cylinder can reach 85 °C).
			// Statehouse has no per-reading quality to carry it, so the value
			// is written and the flag logged.
			a.debug("tanktemp probe value is suspect", "probe", id.Primary, "suspect", *s.Suspect)
		}
		v := *s.TempC
		a.engine.IngestReading(id, topic, model.Reading{Timestamp: ts, TemperatureC: &v})
	}
}

// sweepTime decides the reading time for a state message, and whether its
// readings may be written at all.
func (a *Adapter) sweepTime(p statePayload, now time.Time, retained bool, topic string) (time.Time, bool) {
	synced := p.ClockSynced == nil || *p.ClockSynced
	if !synced {
		// The Pi has no real-time clock: the timestamp may be wrong. A live
		// message takes the receive time; a retained one has no trustworthy
		// age at all, so it is dropped.
		if retained {
			a.debug("tanktemp retained state with unsynced clock dropped", "topic", topic)
			return time.Time{}, false
		}
		return now, true
	}
	parsed, err := time.Parse(time.RFC3339, p.Timestamp)
	if err != nil {
		a.debug("tanktemp state timestamp unparseable", "topic", topic, "timestamp", p.Timestamp)
		if retained {
			return time.Time{}, false
		}
		return now, true
	}
	ts := timeutil.Sanitise(parsed.UTC(), now)
	if retained && now.Sub(ts) > staleAfter {
		a.debug("tanktemp retained state too old to be current; waiting for the next sweep",
			"topic", topic, "timestamp", p.Timestamp)
		return time.Time{}, false
	}
	return ts, true
}

// newSweep reports whether ts is newer than the last sweep accepted for this
// cylinder, and records it if so. tanktemp republishes every 30 s, so a
// repeated timestamp is a replay of a retained message.
func (a *Adapter) newSweep(deviceID string, ts time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if last, ok := a.lastSweep[deviceID]; ok && !ts.After(last) {
		return false
	}
	a.lastSweep[deviceID] = ts
	return true
}

// remember records the probe for the status topic and notes a ROM change
// under the same position: a probe replaced in place.
func (a *Adapter) remember(deviceID string, s sensor) model.DeviceIdentity {
	primary := deviceID + "." + s.Position
	id := model.DeviceIdentity{Scheme: SchemeName, Primary: primary, Display: primary}
	a.mu.Lock()
	a.probes[primary] = id
	prev, seen := a.roms[primary]
	if s.ROM != "" {
		a.roms[primary] = s.ROM
	}
	a.mu.Unlock()
	if seen && s.ROM != "" && prev != s.ROM {
		a.info("tanktemp probe replaced", "probe", primary, "old_rom", prev, "new_rom", s.ROM)
	}
	return id
}

type statusPayload struct {
	Online *bool  `json:"online"`
	Event  string `json:"event"`
}

// handleStatus follows the publisher's lifecycle. Only going offline
// (SHUTDOWN, or the broker's LWT) changes probes: every known probe is
// marked offline. Coming back online is left to the state messages, since a
// HEARTBEAT says tanktemp is alive, not that any one probe reads.
func (a *Adapter) handleStatus(topic string, payload []byte) {
	var s statusPayload
	if err := json.Unmarshal(payload, &s); err != nil {
		a.debug("tanktemp status parse failed", "topic", topic, "error", err)
		return
	}
	if s.Online == nil || *s.Online {
		return
	}
	a.mu.Lock()
	ids := make([]model.DeviceIdentity, 0, len(a.probes))
	for _, id := range a.probes {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	a.info("tanktemp offline; marking its probes offline", "event", s.Event, "probes", len(ids))
	for _, id := range ids {
		a.engine.SetAvailability(id, topic, model.AvailabilityOffline)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *Adapter) debug(msg string, args ...any) {
	if a.logger != nil {
		a.logger.Debug(msg, args...)
	}
}

func (a *Adapter) info(msg string, args ...any) {
	if a.logger != nil {
		a.logger.Info(msg, args...)
	}
}

func (a *Adapter) warn(msg string, args ...any) {
	if a.logger != nil {
		a.logger.Warn(msg, args...)
	}
}
