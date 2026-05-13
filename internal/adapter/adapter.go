// Package adapter defines the protocol-agnostic interface every
// telemetry source plugs into. An Adapter is responsible for
// translating raw MQTT messages (or, in future, other transports)
// into engine calls — EnsureDiscovered / IngestReading /
// SetAvailability — that don't carry protocol vocabulary.
//
// The engine has no knowledge of Zigbee2MQTT, Tasmota, Shelly, Homie
// or DIY raw-topic sensors. Each of those is one concrete Adapter.
// To add a new source, write an adapter; the engine stays still.
package adapter

// Adapter is the contract every protocol implementation satisfies.
// HandleMessage's signature deliberately matches mqtt.Handler so it
// can be passed straight to mqtt.Client.Subscribe.
type Adapter interface {
	// Name is a short scheme identifier — "zigbee", "tasmota",
	// "shelly", "homie", "topic". The engine uses this as the
	// Scheme component of every DeviceIdentity the adapter emits.
	Name() string

	// Subscriptions returns the MQTT topic filters the adapter wants
	// to receive. main wires these through mqtt.Client.Subscribe and
	// dispatches matching messages to HandleMessage. Adapters may
	// return overlapping filters; deduplication is the caller's
	// problem.
	Subscriptions() []string

	// HandleMessage is invoked for every message that matched one of
	// the adapter's Subscriptions. Implementations are responsible
	// for parsing the payload, resolving identity, and calling into
	// the engine. They must not block for long — paho dispatches on
	// its own goroutines, but a slow adapter still serialises the
	// broker's inbound stream.
	HandleMessage(topic string, payload []byte, retained bool)
}
