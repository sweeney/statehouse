package mqtt

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// Publisher is the EventSink that fans derived events out to MQTT
// topics under PublishPrefix. It also publishes per-device state and
// the periodic whole-house snapshot.
//
// Builders, when non-nil, wrap the raw model into the same DTO shape
// the HTTP API publishes (schema_version, summary, warnings, etc.).
// Set them via BuildSnapshot / BuildHouse / BuildDevice. If left nil,
// the publisher falls back to the raw model.Snapshot/House/Device so
// internal tests don't need to import the DTO package.
type Publisher struct {
	Client Client
	Prefix string
	Store  *state.Store
	Logger *slog.Logger

	// BuildSnapshot, when non-nil, transforms a model.Snapshot into the
	// payload published on house/state/snapshot. now is supplied for
	// age/staleness computation.
	BuildSnapshot func(snap model.Snapshot, now time.Time) any
	// BuildHouse, when non-nil, transforms a model.House into the
	// payload published on house/state/house.
	BuildHouse func(h model.House) any
	// BuildDevice, when non-nil, transforms a model.Device into the
	// payload published on house/state/devices/{id}.
	BuildDevice func(d model.Device, now time.Time) any

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
	now := time.Now()
	p.publishJSON(p.Prefix+"/events/derived", false, ev)
	if ev.DeviceID != "" {
		if d, ok := p.Store.Get(ev.DeviceID); ok {
			p.publishJSON(p.Prefix+"/state/devices/"+ev.DeviceID, true, p.devicePayload(d, now))
		}
	}
	switch ev.Type {
	case model.EvtHouseStateChanged:
		p.publishJSON(p.Prefix+"/state/house", true, p.housePayload(p.Store.House()))
	}
	// Always refresh full snapshot on any state-relevant event. This
	// is cheap relative to MQTT round-trip and gives downstreams a
	// single source of truth.
	if relevantForSnapshot(ev.Type) {
		p.publishJSON(p.Prefix+"/state/snapshot", true, p.snapshotPayload(p.Store.Snapshot(), now))
	}
}

// PublishSnapshot is exposed for periodic emission (independent of
// derived events).
func (p *Publisher) PublishSnapshot() {
	if p == nil || p.Client == nil || p.Store == nil {
		return
	}
	now := time.Now()
	p.publishJSON(p.Prefix+"/state/snapshot", true, p.snapshotPayload(p.Store.Snapshot(), now))
	p.publishJSON(p.Prefix+"/state/house", true, p.housePayload(p.Store.House()))
}

// snapshotPayload returns either the DTO-wrapped snapshot (BuildSnapshot
// set) or the raw model.Snapshot.
func (p *Publisher) snapshotPayload(snap model.Snapshot, now time.Time) any {
	if p.BuildSnapshot != nil {
		return p.BuildSnapshot(snap, now)
	}
	return snap
}

func (p *Publisher) housePayload(h model.House) any {
	if p.BuildHouse != nil {
		return p.BuildHouse(h)
	}
	return h
}

func (p *Publisher) devicePayload(d model.Device, now time.Time) any {
	if p.BuildDevice != nil {
		return p.BuildDevice(d, now)
	}
	return d
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
