// Package boiler is the adapter for the sweeney/boiler-sensor daemon.
//
// boiler-sensor publishes two MQTT topics:
//
//	<base>/events  — per-transition payload:
//	  {"boiler":{"timestamp":"...","event":"CH_ON",
//	             "ch":{"state":"ON"},"hw":{"state":"OFF"}}}
//
//	<base>/system  — lifecycle, with two shapes:
//	  simple:  {"system":{"timestamp":"...","event":"SHUTDOWN",
//	                      "reason":"SIGTERM","source":"last_will"}}
//	  rich:    {"status":{"event":"STARTUP","ch":"OFF","hw":"OFF",...}}
//
// The publisher exposes two logical channels (central heating, hot
// water), each ON/OFF. The adapter splits them into two engine
// devices keyed by scheme="boiler" with Primary "ch" / "hw" so they
// have independent activity and cycle records. The publisher's own
// metrics (uptime, event counts, mqtt status) are not house state
// and are dropped.
package boiler

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/sweeney/statehouse/internal/adapter/timeutil"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// SchemeName is the canonical scheme this adapter stamps on identities.
const SchemeName = "boiler"

// Channel identifiers (Primary key within scheme=boiler).
const (
	ChannelCH = "ch"
	ChannelHW = "hw"
)

// DisplayCH / DisplayHW are the default human-readable names. They
// can be overridden by the engine's resolver via a configured devices
// entry under scheme=boiler.
const (
	DisplayCH = "central_heating"
	DisplayHW = "hot_water"
)

// Adapter implements adapter.Adapter for boiler-sensor's two topics.
type Adapter struct {
	engine *state.Engine
	base   string
	logger *slog.Logger
}

// New returns an Adapter for the given base topic (typically
// "energy/boiler/sensor"). Logger may be nil.
func New(engine *state.Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = "energy/boiler/sensor"
	}
	return &Adapter{engine: engine, base: base, logger: logger}
}

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return SchemeName }

// Subscriptions implements adapter.Adapter. boiler-sensor only
// publishes to exactly these two topics, so we subscribe directly
// rather than wildcarding.
func (a *Adapter) Subscriptions() []string {
	return []string{a.base + "/events", a.base + "/system"}
}

// HandleMessage implements adapter.Adapter.
func (a *Adapter) HandleMessage(topic string, payload []byte, retained bool) {
	if a == nil || a.engine == nil || len(payload) == 0 {
		return
	}
	switch topic {
	case a.base + "/events":
		a.handleEvents(topic, payload, retained)
	case a.base + "/system":
		a.handleSystem(topic, payload, retained)
	}
}

// boilerEnvelope decodes the {"boiler":{...}} events shape.
type boilerEnvelope struct {
	Boiler struct {
		Timestamp string `json:"timestamp"`
		Event     string `json:"event"`
		CH        struct {
			State string `json:"state"`
		} `json:"ch"`
		HW struct {
			State string `json:"state"`
		} `json:"hw"`
	} `json:"boiler"`
}

func (a *Adapter) handleEvents(topic string, payload []byte, retained bool) {
	var env boilerEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		if a.logger != nil {
			a.logger.Debug("boiler/events parse failed", "error", err)
		}
		return
	}
	// Every events payload carries both channel states, so we feed
	// both — the engine no-ops when state is unchanged.
	at := a.stamp(env.Boiler.Timestamp, retained)
	a.ingestChannel(ChannelCH, DisplayCH, env.Boiler.CH.State, topic, at)
	a.ingestChannel(ChannelHW, DisplayHW, env.Boiler.HW.State, topic, at)
}

// systemEnvelope decodes the simple {"system":{...}} shape. The rich
// {"status":{...}} shape used by STARTUP / HEARTBEAT / full SHUTDOWN
// payloads is decoded into statusEnvelope below.
type systemEnvelope struct {
	System struct {
		Timestamp string `json:"timestamp"`
		Event     string `json:"event"`
		Reason    string `json:"reason"`
		Source    string `json:"source"`
	} `json:"system"`
}

type statusEnvelope struct {
	Status struct {
		Timestamp string `json:"timestamp"`
		Event     string `json:"event"`
		CH        string `json:"ch"`
		HW        string `json:"hw"`
		MQTT      struct {
			Connected bool `json:"connected"`
		} `json:"mqtt"`
	} `json:"status"`
}

func (a *Adapter) handleSystem(topic string, payload []byte, retained bool) {
	// Distinguish the two shapes by which top-level key is present.
	// We try status first because it carries channel state we can
	// seed (e.g. STARTUP gives us the initial CH/HW).
	if hasKey(payload, "status") {
		var s statusEnvelope
		if err := json.Unmarshal(payload, &s); err != nil {
			if a.logger != nil {
				a.logger.Debug("boiler/system status parse failed", "error", err)
			}
			return
		}
		a.applyStatus(s, topic, retained)
		return
	}
	if hasKey(payload, "system") {
		var s systemEnvelope
		if err := json.Unmarshal(payload, &s); err != nil {
			if a.logger != nil {
				a.logger.Debug("boiler/system system parse failed", "error", err)
			}
			return
		}
		a.applySystem(s, topic)
	}
}

func (a *Adapter) applyStatus(s statusEnvelope, topic string, retained bool) {
	// Channel state is authoritative — seed both channels.
	at := a.stamp(s.Status.Timestamp, retained)
	a.ingestChannel(ChannelCH, DisplayCH, s.Status.CH, topic, at)
	a.ingestChannel(ChannelHW, DisplayHW, s.Status.HW, topic, at)
	// Lifecycle: STARTUP / HEARTBEAT imply online; SHUTDOWN implies
	// offline (the simple envelope handler covers explicit LWT).
	switch strings.ToUpper(s.Status.Event) {
	case "STARTUP", "HEARTBEAT", "RECONNECTED":
		a.setBothAvailable(model.AvailabilityOnline, topic)
	case "SHUTDOWN":
		a.setBothAvailable(model.AvailabilityOffline, topic)
	}
}

func (a *Adapter) applySystem(s systemEnvelope, topic string) {
	switch strings.ToUpper(s.System.Event) {
	case "STARTUP", "HEARTBEAT", "RECONNECTED":
		a.setBothAvailable(model.AvailabilityOnline, topic)
	case "SHUTDOWN":
		// The publisher's clean SHUTDOWN, or the broker's retained LWT
		// (Source="last_will"), both mean the boiler-sensor isn't
		// speaking. Channel states from before that point remain in
		// the store; engine debounce handles the rapid restart case.
		a.setBothAvailable(model.AvailabilityOffline, topic)
	}
}

func (a *Adapter) setBothAvailable(avail model.Availability, topic string) {
	a.engine.SetAvailability(a.identity(ChannelCH, DisplayCH), topic, avail)
	a.engine.SetAvailability(a.identity(ChannelHW, DisplayHW), topic, avail)
}

// stamp turns boiler-sensor's RFC3339 timestamp into the reading's time and
// age. A retained message without a usable one has no known age (#93).
func (a *Adapter) stamp(raw string, retained bool) model.Reading {
	ts, dated := timeutil.RFC3339Dated(raw, a.engine.Now())
	return model.Reading{Timestamp: ts, UndatedReplay: retained && !dated}
}

// ingestChannel feeds one channel's state into the engine, stamped by at
// (a Reading carrying only Timestamp and UndatedReplay).
func (a *Adapter) ingestChannel(primary, display, state, topic string, at model.Reading) {
	s := strings.ToUpper(strings.TrimSpace(state))
	if s != "ON" && s != "OFF" {
		return
	}
	r := at
	r.State = &s
	a.engine.IngestReading(a.identity(primary, display), topic, r)
}

func (a *Adapter) identity(primary, display string) model.DeviceIdentity {
	return model.DeviceIdentity{Scheme: SchemeName, Primary: primary, Display: display}
}

// hasKey reports whether the JSON object payload has a top-level key
// with the given name. Cheap structural check used to disambiguate
// the two boiler-sensor /system envelope shapes without parsing both.
func hasKey(payload []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
