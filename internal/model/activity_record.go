package model

import "time"

// ActivityRecord is a completed or in-progress semantic activity entry
// logged to the bounded recent-activity ring buffer. Unlike ActivitySignal,
// records persist after the activity ends so callers can see "last call was
// 7 minutes ago" etc.
type ActivityRecord struct {
	// ID matches the ActivitySignal ID for session-based sources so the
	// adapter can update the record when the session ends.
	ID string `json:"id"`

	// Source identifies the adapter ("intercom", "front_door", "unifi").
	Source string `json:"source"`

	// Location is an optional room/zone hint.
	Location string `json:"location,omitempty"`

	// Type is a semantic classification: "call", "door_open", "motion".
	Type string `json:"type"`

	// StartedAt is when the activity began.
	StartedAt time.Time `json:"started_at"`

	// EndedAt is when the activity finished. Nil for in-progress activities.
	EndedAt *time.Time `json:"ended_at,omitempty"`

	// Meta carries source-specific details (caller ID, cause, duration, etc.).
	Meta map[string]any `json:"meta,omitempty"`
}
