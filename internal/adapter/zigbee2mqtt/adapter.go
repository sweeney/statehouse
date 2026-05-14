package zigbee2mqtt

import (
	"log/slog"
	"sync"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// SchemeName is the canonical scheme identifier this adapter stamps on
// every DeviceIdentity it emits. "zigbee" lives one level above
// "zigbee2mqtt" — the engine doesn't know that Zigbee can also be
// fronted by other bridges (Conbee, Deconz, Home Assistant ZHA), but
// for the V1 source we only have Z2M.
const SchemeName = "zigbee"

// Adapter translates Zigbee2MQTT MQTT traffic into engine calls. It:
//
//   - parses retained bridge/devices payloads to learn the
//     friendly_name → ieee_address mapping,
//   - turns per-device messages into model.Reading values via the
//     payload parser,
//   - turns availability messages into engine availability transitions,
//   - emits identity records using ("zigbee", ieee_address,
//     friendly_name) whenever the IEEE is known, falling back to
//     ("zigbee", friendly_name, friendly_name) if it isn't yet.
type Adapter struct {
	engine *state.Engine
	base   string
	logger *slog.Logger

	mu        sync.RWMutex
	ieeeByFN  map[string]string // friendly_name -> ieee_address
}

// New constructs a Z2M adapter rooted at the given base topic
// (typically "zigbee2mqtt"). The logger may be nil.
func New(engine *state.Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = "zigbee2mqtt"
	}
	return &Adapter{
		engine:   engine,
		base:     base,
		logger:   logger,
		ieeeByFN: make(map[string]string),
	}
}

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return SchemeName }

// Subscriptions implements adapter.Adapter. A single wildcard catches
// everything under the bridge's namespace; topic-level routing
// happens in HandleMessage.
func (a *Adapter) Subscriptions() []string {
	return []string{a.base + "/#"}
}

// HandleMessage dispatches one inbound MQTT message based on topic shape.
func (a *Adapter) HandleMessage(topic string, payload []byte, retained bool) {
	if a == nil || a.engine == nil {
		return
	}
	if topic == a.base+"/bridge/devices" {
		a.handleBridgeDevices(payload)
		return
	}
	// Other bridge/* messages carry no per-device telemetry.
	if topic == a.base+"/bridge/state" || topic == a.base+"/bridge/info" ||
		topic == a.base+"/bridge/logging" || topic == a.base+"/bridge/log" ||
		topic == a.base+"/bridge/event" || topic == a.base+"/bridge/extensions" {
		return
	}
	if name, ok := AvailabilityFromTopic(a.base, topic); ok {
		a.handleAvailability(name, payload, topic)
		return
	}
	if name := TopicFriendlyName(a.base, topic); name != "" {
		a.handleDevicePayload(name, topic, payload)
		return
	}
}

func (a *Adapter) handleBridgeDevices(payload []byte) {
	devs, err := ParseBridgeDevices(payload)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("bridge/devices parse failed", "error", err)
		}
		return
	}
	a.mu.Lock()
	for _, d := range devs {
		if d.FriendlyName != "" && d.IEEEAddress != "" {
			a.ieeeByFN[d.FriendlyName] = d.IEEEAddress
		}
	}
	a.mu.Unlock()
	for _, d := range devs {
		topic := a.base + "/" + d.FriendlyName
		a.engine.EnsureDiscovered(a.identityFor(d.FriendlyName, d.IEEEAddress), topic)
	}
	if a.logger != nil {
		a.logger.Info("bridge devices ingested", "count", len(devs))
	}
}

func (a *Adapter) handleAvailability(friendlyName string, payload []byte, topic string) {
	avail, ok := ParseAvailability(payload)
	if !ok {
		return
	}
	a.engine.SetAvailability(a.identityFor(friendlyName, ""), topic, avail)
}

func (a *Adapter) handleDevicePayload(friendlyName, topic string, payload []byte) {
	// Empty payload is normal on bridge restart; ignore.
	if len(payload) == 0 {
		return
	}
	// Some devices publish non-JSON payloads on subtopics; protect.
	if payload[0] != '{' {
		return
	}
	reading, err := ParseDevicePayload(payload)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("zigbee2mqtt parse failed", "topic", topic, "error", err)
		}
		return
	}
	if !reading.HasAnyMeasurement() {
		return
	}
	reading.SourceTopic = topic
	a.engine.IngestReading(a.identityFor(friendlyName, ""), topic, reading)
}

// identityFor builds a DeviceIdentity from a friendly name plus
// optional IEEE. If IEEE is unknown but we've previously seen this
// device in bridge/devices we fill it in from the cache; otherwise
// Primary falls back to the friendly name so the engine still has a
// stable-ish key, and merges identity later when IEEE is learned.
func (a *Adapter) identityFor(friendlyName, ieee string) model.DeviceIdentity {
	if ieee == "" && friendlyName != "" {
		a.mu.RLock()
		ieee = a.ieeeByFN[friendlyName]
		a.mu.RUnlock()
	}
	id := model.DeviceIdentity{Scheme: SchemeName, Display: friendlyName}
	if ieee != "" {
		id.Primary = ieee
	} else {
		id.Primary = friendlyName
	}
	return id
}
