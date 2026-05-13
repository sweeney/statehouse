package mqtt

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// Publisher is the EventSink that fans derived events out to MQTT
// topics under PublishPrefix. It also publishes per-device state and
// the periodic whole-house snapshot.
type Publisher struct {
	Client Client
	Prefix string
	Store  *state.Store
	Logger *slog.Logger

	mu sync.Mutex
}

// OnDerivedEvent implements state.EventSink. For each derived event:
//   - publish the event itself on house/events/derived,
//   - publish the affected per-device snapshot on
//     house/state/devices/{id} (if applicable),
//   - publish the house snapshot on house/state/house when house state
//     changes.
func (p *Publisher) OnDerivedEvent(ev model.DerivedEvent) {
	if p == nil || p.Client == nil {
		return
	}
	p.publishJSON(p.Prefix+"/events/derived", false, ev)
	if ev.DeviceID != "" {
		if d, ok := p.Store.Get(ev.DeviceID); ok {
			p.publishJSON(p.Prefix+"/state/devices/"+ev.DeviceID, true, d)
		}
	}
	switch ev.Type {
	case model.EvtHouseStateChanged:
		p.publishJSON(p.Prefix+"/state/house", true, p.Store.House())
	}
	// Always refresh full snapshot on any state-relevant event. This
	// is cheap relative to MQTT round-trip and gives downstreams a
	// single source of truth.
	if relevantForSnapshot(ev.Type) {
		p.publishJSON(p.Prefix+"/state/snapshot", true, p.Store.Snapshot())
	}
}

// PublishSnapshot is exposed for periodic emission (independent of
// derived events).
func (p *Publisher) PublishSnapshot() {
	if p == nil || p.Client == nil || p.Store == nil {
		return
	}
	p.publishJSON(p.Prefix+"/state/snapshot", true, p.Store.Snapshot())
	p.publishJSON(p.Prefix+"/state/house", true, p.Store.House())
}

func (p *Publisher) publishJSON(topic string, retained bool, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("mqtt marshal failed", "topic", topic, "error", err)
		}
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Client.Publish(topic, 0, retained, b); err != nil {
		if p.Logger != nil {
			p.Logger.Warn("mqtt publish failed", "topic", topic, "error", err)
		}
	}
}

func relevantForSnapshot(t model.DerivedEventType) bool {
	switch t {
	case model.EvtDeviceActivityChanged,
		model.EvtCycleStarted, model.EvtCycleFinished,
		model.EvtContinuousCycleStarted, model.EvtContinuousCycleFinished,
		model.EvtMediaActive, model.EvtMediaInactive,
		model.EvtHouseStateChanged, model.EvtDeviceAvailabilityChanged:
		return true
	}
	return false
}
