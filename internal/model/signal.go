package model

import "time"

// ActivitySignal is a presence/activity assertion from a non-device
// source — an intercom call, a WiFi session, a PIR trigger, a door
// event. Unlike devices, signals have no state machine or power
// readings; they are asserted for a duration (TTL) or until explicitly
// cleared by the adapter that created them.
type ActivitySignal struct {
	// Timestamp is when IngestSignal was called; set by the engine if zero.
	Timestamp time.Time `json:"ts"`

	// ID is unique per signal instance. For session-based signals (calls,
	// WiFi) the adapter uses the protocol's own session ID so it can clear
	// the right signal on end. For instantaneous signals (PIR, door) a
	// generated ID is fine.
	ID string `json:"id"`

	// Source identifies the adapter that owns the signal ("intercom",
	// "unifi", "pir"). Stable across restarts; used for filtering and
	// display.
	Source string `json:"source"`

	// Location is an optional room/zone hint ("office", "hallway",
	// "house"). Empty means house-wide.
	Location string `json:"location,omitempty"`

	// Type is a source-specific classification: "call_ringing",
	// "call_active", "motion", "wifi_session", "door_open".
	Type string `json:"type"`

	// Confidence is the adapter's estimate that this signal represents
	// genuine occupancy/activity, in [0,1].
	Confidence float64 `json:"confidence"`

	// Since is when the signal was first asserted.
	Since time.Time `json:"since"`

	// ExpiresAt is the safety-net expiry. Zero means no automatic expiry
	// — the signal lives until ClearSignal is called. Adapters for
	// session-based sources (calls, WiFi) should still set a generous TTL
	// (e.g. 1h) so a missed end-event doesn't leave a zombie signal.
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	// Meta carries source-specific details for logging/display (caller
	// ID, MAC address, sensor name, etc.). Not used by house-state logic.
	Meta map[string]any `json:"meta,omitempty"`
}

// IsExpired reports whether the signal has passed its expiry time.
// Signals with a zero ExpiresAt never expire automatically.
func (s ActivitySignal) IsExpired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && now.After(s.ExpiresAt)
}
