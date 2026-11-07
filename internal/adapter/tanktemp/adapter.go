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
//   - write the same sweep twice: a timestamp equal to the cylinder's last
//     written sweep is a replay. An older timestamp is still a new sweep,
//     because the publisher's clock may have been corrected;
//   - trust a synced timestamp more than staleAfter in the future;
//   - create devices from the status topic's publisher health (sensors_ok,
//     unexpected, startup_problems);
//   - mark probes online from a HEARTBEAT. Probes come back online only
//     when a reading is written.
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

// DefaultBase is tanktemp's production topic prefix. config.Load fills the
// same default for adapters.tanktemp.base_topic; keep the two in step.
const DefaultBase = "energy/hot_water"

// staleAfter is how old a retained state may be and still be taken as the
// current value: three of tanktemp's 30 s sweeps. It is also how far ahead
// of statehouse's clock a synced timestamp may be before it is distrusted.
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
	// lastSweep is the timestamp of the last sweep written per cylinder, so
	// a replayed message is never written twice.
	lastSweep map[string]time.Time
	// probes is every probe identity seen, keyed by primary, so the status
	// topic can mark them all offline. Entries are never removed: the set is
	// bounded by tanktemp's configured probes, and a probe dropped from that
	// config is still marked offline on each SHUTDOWN, which is harmless.
	probes map[string]model.DeviceIdentity
	// roms is the ROM last seen under each primary, to notice a swap. Never
	// pruned, for the same reason as probes.
	roms map[string]string
	// publisherOffline is true after an offline status (SHUTDOWN or LWT)
	// until a later online status or a written sweep. It lets a state
	// message that arrives after the status, typically the retained pair
	// delivered on a statehouse restart, register its probes as offline
	// rather than unknown.
	publisherOffline bool
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

// probe is a sensor with a valid position, paired with its identity.
type probe struct {
	id model.DeviceIdentity
	s  sensor
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
	write := ok && a.newSweep(deviceID, ts)

	// Register every probe, readable or not, so a probe that has never read
	// still shows up rather than being invisible.
	probes := make([]probe, 0, len(p.Sensors))
	for _, s := range p.Sensors {
		if !rePosition.MatchString(s.Position) {
			a.debug("tanktemp sensor with bad position skipped", "topic", topic, "position", s.Position)
			continue
		}
		probes = append(probes, probe{id: a.remember(deviceID, s), s: s})
	}

	a.mu.Lock()
	if write {
		// A sweep worth writing means tanktemp is alive.
		a.publisherOffline = false
	}
	offline := a.publisherOffline
	a.mu.Unlock()

	for _, pr := range probes {
		a.engine.EnsureDiscovered(pr.id, topic)
		if offline {
			// tanktemp is known to be down and this sweep is not current:
			// the probe is offline, not unknown.
			a.engine.SetAvailability(pr.id, topic, model.AvailabilityOffline)
		}
	}
	if !write {
		return
	}

	for _, pr := range probes {
		s := pr.s
		if s.TempC == nil {
			// Write nothing, never 0. The probe goes stale if this persists.
			a.debug("tanktemp probe has no value", "probe", pr.id.Primary, "error", deref(s.Error))
			continue
		}
		if !validate.FiniteInRange(*s.TempC, minTempC, maxTempC) {
			a.warn("adapter: rejected out-of-range field", "field", "temperature_c",
				"value", *s.TempC, "probe", pr.id.Primary, "topic", topic)
			continue
		}
		if s.Suspect != nil {
			// Published, and probably real (a cylinder can reach 85 °C).
			// Statehouse has no per-reading quality to carry it, so the value
			// is written and the flag logged. TODO(#82): carry it.
			a.debug("tanktemp probe value is suspect", "probe", pr.id.Primary, "suspect", *s.Suspect)
		}
		v := *s.TempC
		a.engine.IngestReading(pr.id, topic, model.Reading{Timestamp: ts, TemperatureC: &v})
	}
}

// sweepTime decides the reading time for a state message, and whether its
// readings may be written at all.
//
// A live message whose timestamp can't be trusted takes the receive time; a
// retained one is dropped, because its age is then unknown.
func (a *Adapter) sweepTime(p statePayload, now time.Time, retained bool, topic string) (time.Time, bool) {
	untrusted := func() (time.Time, bool) {
		if retained {
			return time.Time{}, false
		}
		return now, true
	}
	// WIRE.md says every key is always present, so an absent clock_synced is
	// a contract violation. Treat it as unsynced: wrongly trusting a
	// timestamp is worse than re-stamping one.
	if p.ClockSynced == nil || !*p.ClockSynced {
		// The Pi has no real-time clock, so the timestamp may be wrong.
		a.debug("tanktemp state clock not synced", "topic", topic, "retained", retained)
		return untrusted()
	}
	parsed, err := time.Parse(time.RFC3339, p.Timestamp)
	if err != nil {
		a.warn("tanktemp synced state has an unparseable timestamp",
			"topic", topic, "timestamp", p.Timestamp)
		return untrusted()
	}
	ts := timeutil.Sanitise(parsed.UTC(), now)
	if ts.Sub(now) > staleAfter {
		// A clock that jumped forward. Trusting it would date every reading
		// in the future.
		a.warn("tanktemp synced state timestamp is in the future",
			"topic", topic, "timestamp", p.Timestamp, "now", now.Format(time.RFC3339))
		return untrusted()
	}
	if retained && now.Sub(ts) > staleAfter {
		a.debug("tanktemp retained state too old to be current; waiting for the next sweep",
			"topic", topic, "timestamp", p.Timestamp)
		return time.Time{}, false
	}
	return ts, true
}

// newSweep reports whether ts is a sweep not yet written for this cylinder,
// and records it if so. A replay (the retained message redelivered on
// reconnect) repeats the last timestamp exactly. Anything else is new, even
// if older, since the publisher's clock may have been corrected.
func (a *Adapter) newSweep(deviceID string, ts time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if last, ok := a.lastSweep[deviceID]; ok && ts.Equal(last) {
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

// statusPayload ignores tanks. One tanktemp serves one base topic (WIRE.md:
// a second would need its own prefix), so every probe this adapter has seen
// belongs to the publisher whose status this is.
type statusPayload struct {
	Online *bool  `json:"online"`
	Event  string `json:"event"`
}

// handleStatus follows the publisher's lifecycle. Only going offline
// (SHUTDOWN, or the broker's LWT) changes probes: every known probe is
// marked offline, and probes registered later stay offline until a sweep is
// written. Coming back online is left to the state messages, since a
// HEARTBEAT says tanktemp is alive, not that any one probe reads.
func (a *Adapter) handleStatus(topic string, payload []byte) {
	var s statusPayload
	if err := json.Unmarshal(payload, &s); err != nil {
		a.debug("tanktemp status parse failed", "topic", topic, "error", err)
		return
	}
	if s.Online == nil {
		return
	}
	a.mu.Lock()
	a.publisherOffline = !*s.Online
	if *s.Online {
		a.mu.Unlock()
		return
	}
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
