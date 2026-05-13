package mqtt

import (
	"log/slog"

	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/zigbee2mqtt"
)

// Z2MSubscriber wires MQTT message handlers for Zigbee2MQTT topics to
// the state engine. It is responsible for:
//   - parsing retained bridge/devices to seed the device registry,
//   - turning per-device messages into Readings via the normaliser,
//   - turning availability messages into availability transitions.
type Z2MSubscriber struct {
	Engine *state.Engine
	Base   string
	Logger *slog.Logger
}

// HandleMessage is the single entry point for the MQTT message
// callback. It dispatches based on topic shape.
func (s *Z2MSubscriber) HandleMessage(topic string, payload []byte, retained bool) {
	if s == nil || s.Engine == nil {
		return
	}
	if topic == s.Base+"/bridge/devices" {
		s.handleBridgeDevices(payload)
		return
	}
	// bridge/* messages are otherwise ignored.
	if topic == s.Base+"/bridge/state" || topic == s.Base+"/bridge/info" ||
		topic == s.Base+"/bridge/logging" || topic == s.Base+"/bridge/log" ||
		topic == s.Base+"/bridge/event" || topic == s.Base+"/bridge/extensions" {
		return
	}
	if name, ok := zigbee2mqtt.AvailabilityFromTopic(s.Base, topic); ok {
		s.handleAvailability(name, payload, topic)
		return
	}
	if name := zigbee2mqtt.TopicFriendlyName(s.Base, topic); name != "" {
		s.handleDevicePayload(name, topic, payload)
		return
	}
}

func (s *Z2MSubscriber) handleBridgeDevices(payload []byte) {
	devs, err := zigbee2mqtt.ParseBridgeDevices(payload)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("bridge/devices parse failed", "error", err)
		}
		return
	}
	for _, d := range devs {
		topic := s.Base + "/" + d.FriendlyName
		s.Engine.EnsureDiscovered(d.IEEEAddress, d.FriendlyName, topic)
	}
	if s.Logger != nil {
		s.Logger.Info("bridge devices ingested", "count", len(devs))
	}
}

func (s *Z2MSubscriber) handleAvailability(friendlyName string, payload []byte, topic string) {
	avail, ok := zigbee2mqtt.ParseAvailability(payload)
	if !ok {
		return
	}
	s.Engine.SetAvailability("", friendlyName, topic, avail)
}

func (s *Z2MSubscriber) handleDevicePayload(friendlyName, topic string, payload []byte) {
	// Empty payload is normal on bridge restart; ignore.
	if len(payload) == 0 {
		return
	}
	// Some devices publish non-JSON payloads on subtopics; protect.
	if payload[0] != '{' {
		return
	}
	reading, err := zigbee2mqtt.ParseDevicePayload(payload)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Debug("device payload parse failed", "topic", topic, "error", err)
		}
		return
	}
	if !reading.HasAnyMeasurement() {
		// Nothing useful in this payload (e.g. empty bridge restart).
		return
	}
	reading.SourceTopic = topic
	s.Engine.IngestReading("", friendlyName, topic, reading)
}
