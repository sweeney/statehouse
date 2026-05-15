// Package intercom is the adapter for Asterisk-via-MQTT phone system events.
//
// The Asterisk bridge publishes on two topic shapes:
//
//	<base>/status
//	  {"state":"online","started_at":"...","uptime_seconds":...,"timestamp":"...","events":{...}}
//
//	<base>/call/<call_id>/<event>   where event is ringing | answered | hungup
//	  {"event":"ringing","call_id":"...","from":{...},"to":{...},"timestamp":"..."}
//	  {"event":"answered",...}
//	  {"event":"hungup","cause":"...","talk_duration_seconds":...,"total_duration_seconds":...,...}
//
// The external concept is "Intercom" — Asterisk is an implementation detail.
// Each in-flight call is tracked as an ActivitySignal (scheme="intercom",
// signal ID = call_id). The signal is asserted on ringing and cleared on
// hungup. A safety TTL prevents zombie signals if a hungup is missed.
//
// The heartbeat (asterisk/status) keeps the adapter's availability signal
// alive; a missing heartbeat will let the safety TTL clear any open calls.
package intercom

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

const (
	// SchemeName is the canonical source name stamped on ActivitySignals.
	SchemeName = "intercom"

	// CallTTL is the safety-net expiry for call signals. If a hungup event
	// is missed (e.g. broker restart), the signal expires after this duration
	// rather than leaving a zombie.
	CallTTL = 10 * time.Minute
)

// Engine is the subset of state.Engine the adapter needs. Defined as an
// interface so tests can substitute a fake without a full engine.
type Engine interface {
	IngestSignal(s model.ActivitySignal)
	ClearSignal(id string, ts time.Time)
	RecordActivity(r model.ActivityRecord)
	UpdateActivity(id string, fn func(*model.ActivityRecord))
}

// Adapter implements adapter.Adapter for Asterisk MQTT events.
type Adapter struct {
	engine Engine
	base   string
	logger *slog.Logger
	sink   DerivedSink
}

// New returns an Adapter for the given base topic (typically "asterisk").
func New(engine Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = "asterisk"
	}
	return &Adapter{engine: engine, base: base, logger: logger}
}

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return SchemeName }

// Subscriptions implements adapter.Adapter.
func (a *Adapter) Subscriptions() []string {
	return []string{a.base + "/#"}
}

// HandleMessage implements adapter.Adapter.
func (a *Adapter) HandleMessage(topic string, payload []byte, retained bool) {
	if len(payload) == 0 {
		return
	}
	suffix := strings.TrimPrefix(topic, a.base+"/")
	switch {
	case suffix == "status":
		// Heartbeat — nothing to do for now; availability is implicit from
		// call signals still being active.
	case strings.HasPrefix(suffix, "call/"):
		a.handleCallEvent(topic, suffix, payload)
	}
}

// callPayload covers the common fields across ringing/answered/hungup.
type callPayload struct {
	Event       string `json:"event"`
	CallID      string `json:"call_id"`
	Timestamp   string `json:"timestamp"`
	From        struct {
		Extension string `json:"extension"`
		Name      string `json:"name"`
	} `json:"from"`
	To struct {
		Extension string `json:"extension"`
		Name      string `json:"name"`
	} `json:"to"`
	Cause                string  `json:"cause"`
	CauseDescription     string  `json:"cause_description"`
	TalkDurationSeconds  float64 `json:"talk_duration_seconds"`
	TotalDurationSeconds float64 `json:"total_duration_seconds"`
}

func (a *Adapter) handleCallEvent(sourceTopic, suffix string, payload []byte) {
	// suffix is "call/<call_id>/<event>"
	parts := strings.SplitN(suffix, "/", 3)
	if len(parts) != 3 {
		return
	}
	callID := parts[1]
	if callID == "" {
		return
	}

	var p callPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		if a.logger != nil {
			a.logger.Debug("intercom: call payload parse failed", "topic", sourceTopic, "error", err)
		}
		return
	}

	ts := parseTimestamp(p.Timestamp)

	switch strings.ToLower(p.Event) {
	case "ringing":
		meta := map[string]any{
			"from_extension": p.From.Extension,
			"from_name":      p.From.Name,
			"to_extension":   p.To.Extension,
			"to_name":        p.To.Name,
		}
		a.engine.IngestSignal(model.ActivitySignal{
			ID:         callID,
			Source:     SchemeName,
			Type:       "call_ringing",
			Confidence: 0.85,
			Since:      ts,
			ExpiresAt:  ts.Add(CallTTL),
			Meta:       meta,
		})
		a.engine.RecordActivity(model.ActivityRecord{
			ID:        callID,
			Source:    SchemeName,
			Type:      "call",
			StartedAt: ts,
			Meta:      meta,
		})
		a.emitCallEvent(model.EvtIntercomRinging, callID, ts, p)

	case "answered":
		// Upgrade the signal: higher confidence, reset TTL from answer time.
		a.engine.IngestSignal(model.ActivitySignal{
			ID:         callID,
			Source:     SchemeName,
			Type:       "call_active",
			Confidence: 0.95,
			Since:      ts,
			ExpiresAt:  ts.Add(CallTTL),
			Meta: map[string]any{
				"from_extension": p.From.Extension,
				"from_name":      p.From.Name,
				"to_extension":   p.To.Extension,
				"to_name":        p.To.Name,
			},
		})
		answeredAt := ts.Format(time.RFC3339)
		a.engine.UpdateActivity(callID, func(r *model.ActivityRecord) {
			if r.Meta == nil {
				r.Meta = make(map[string]any)
			}
			r.Meta["answered_at"] = answeredAt
		})
		a.emitCallEvent(model.EvtIntercomAnswered, callID, ts, p)

	case "hungup":
		a.engine.ClearSignal(callID, ts)
		endedAt := ts
		a.engine.UpdateActivity(callID, func(r *model.ActivityRecord) {
			r.EndedAt = &endedAt
			if r.Meta == nil {
				r.Meta = make(map[string]any)
			}
			if p.Cause != "" {
				r.Meta["cause"] = p.Cause
			}
			if p.TalkDurationSeconds > 0 {
				r.Meta["talk_duration_seconds"] = p.TalkDurationSeconds
			}
		})
		a.emitCallEvent(model.EvtIntercomHungup, callID, ts, p)
	}
}

// DerivedSink is an optional EventSink the adapter can emit rich call
// events to. Set via SetDerivedSink after construction.
type DerivedSink interface {
	OnDerivedEvent(model.DerivedEvent)
}

// SetDerivedSink wires up an event sink for rich intercom events. If not
// set, rich events are silently dropped (signal-based occupancy still works).
func (a *Adapter) SetDerivedSink(s DerivedSink) {
	a.sink = s
}

func (a *Adapter) emitCallEvent(evtType model.DerivedEventType, callID string, ts time.Time, p callPayload) {
	if a.sink == nil {
		return
	}
	ev := model.DerivedEvent{
		Timestamp: ts,
		Type:      evtType,
		Evidence: map[string]any{
			"call_id":        callID,
			"from_extension": p.From.Extension,
			"from_name":      p.From.Name,
			"to_extension":   p.To.Extension,
			"to_name":        p.To.Name,
		},
	}
	if p.Cause != "" {
		ev.Evidence["cause"] = p.Cause
		ev.Evidence["cause_description"] = p.CauseDescription
	}
	if p.TalkDurationSeconds > 0 {
		ev.Evidence["talk_duration_seconds"] = p.TalkDurationSeconds
	}
	if p.TotalDurationSeconds > 0 {
		ev.Evidence["total_duration_seconds"] = p.TotalDurationSeconds
	}
	a.sink.OnDerivedEvent(ev)
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now()
	}
	return t
}
