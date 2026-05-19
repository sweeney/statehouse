package model

import "time"

// CanonicalEvent is the normalised representation of a single piece of
// telemetry. It is what state machines consume; raw MQTT payload shape
// must not leak past the normaliser.
type CanonicalEvent struct {
	Timestamp   time.Time      `json:"ts"`
	Source      string         `json:"source"`
	SourceTopic string         `json:"source_topic,omitempty"`
	DeviceID    string         `json:"device_id"`
	Identity    DeviceIdentity `json:"identity"`
	Capability  string         `json:"capability"`
	Attribute   string         `json:"attribute"`
	Value       any            `json:"value"`
	Unit        string         `json:"unit,omitempty"`
	Quality     map[string]any `json:"quality,omitempty"`
}

// DerivedEventType enumerates derived events the engine emits. These
// are stable identifiers consumed by downstream MQTT topics and HTTP
// API clients.
type DerivedEventType string

const (
	EvtDeviceDiscovered          DerivedEventType = "device_discovered"
	EvtDeviceAvailabilityChanged DerivedEventType = "device_availability_changed"
	EvtDeviceActivityChanged     DerivedEventType = "device_activity_changed"
	EvtDeviceActivityStarted     DerivedEventType = "device_activity_started"
	EvtDeviceActivityFinished    DerivedEventType = "device_activity_finished"
	EvtShortBurstDetected        DerivedEventType = "short_burst_detected"
	EvtCycleStarted              DerivedEventType = "cycle_started"
	EvtCycleFinished             DerivedEventType = "cycle_finished"
	EvtCycleEnergyRecorded       DerivedEventType = "cycle_energy_recorded"
	EvtContinuousCycleStarted    DerivedEventType = "continuous_device_active_cycle_started"
	EvtContinuousCycleFinished   DerivedEventType = "continuous_device_active_cycle_finished"
	EvtMediaActive               DerivedEventType = "media_active"
	EvtMediaInactive             DerivedEventType = "media_inactive"
	EvtEnergyDivergenceWarning   DerivedEventType = "energy_divergence_warning"
	EvtEnergyStaleCounterWarning DerivedEventType = "energy_stale_counter_warning"
	EvtHouseStateChanged         DerivedEventType = "house_state_changed"

	// Intercom events. Source is the intercom adapter; the call_id and
	// participant details are in Evidence.
	EvtIntercomRinging  DerivedEventType = "intercom_ringing"
	EvtIntercomAnswered DerivedEventType = "intercom_answered"
	EvtIntercomHungup   DerivedEventType = "intercom_hungup"

	// Signal lifecycle events emitted by the engine when a signal is
	// asserted or cleared.
	EvtSignalAsserted DerivedEventType = "signal_asserted"
	EvtSignalCleared  DerivedEventType = "signal_cleared"
)

// DerivedEvent is something the engine concluded from one or more
// canonical events.
type DerivedEvent struct {
	ID          string           `json:"id"`
	Timestamp   time.Time        `json:"ts"`
	Type        DerivedEventType `json:"type"`
	DeviceID    string           `json:"device_id,omitempty"`
	DeviceClass string           `json:"device_class,omitempty"`
	Summary     string           `json:"summary,omitempty"`
	Evidence    map[string]any   `json:"evidence,omitempty"`
	Severity    string           `json:"severity,omitempty"`
}
