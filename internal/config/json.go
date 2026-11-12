package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// The behaviour sections (energy, availability, house) are decoded as an
// overlay: each UnmarshalJSON below writes only the keys the document names
// and leaves every other field of the receiver as it was. The remote fetcher
// relies on that. It calls them on a copy of the section cfg already holds
// (the defaults), so a key a document leaves out keeps its default instead of
// becoming zero. That is issue #86: production's house block left out
// sleeping_after, which became 0s, and the house never entered sleeping mode.
//
// To make "absent" and "present" distinguishable, every field of the wire
// shapes is a pointer: nil means the key was absent (keep the current value),
// non-nil means it was present (replace it, even with a zero). An explicit
// zero is a real setting: `sleeping_after: "0s"` is documented as "never
// enter sleeping", so it must not be mistaken for "not set".

// parseDuration parses a duration string (e.g. "30m", "10s") from a JSON
// field. ok is false when the key is absent (s == nil) or empty (""), which
// both mean "leave the current value alone". An empty string is treated as
// absent rather than as an error for compatibility with documents written
// before #86, where it was silently skipped. A string that is present but
// does not parse is an error naming the field.
func parseDuration(s *string, field string) (d time.Duration, ok bool, err error) {
	if s == nil || *s == "" {
		return 0, false, nil
	}
	d, err = time.ParseDuration(*s)
	if err != nil {
		return 0, false, fmt.Errorf("%s: %w", field, err)
	}
	return d, true, nil
}

// overlayDurations parses each present duration into its destination,
// leaving absent ones untouched. It parses all of them before writing any,
// so a bad value in one field leaves the receiver wholly unchanged rather
// than half-applied.
func overlayDurations(fields []durationField) error {
	parsed := make([]time.Duration, len(fields))
	present := make([]bool, len(fields))
	for i, f := range fields {
		d, ok, err := parseDuration(f.src, f.name)
		if err != nil {
			return err
		}
		parsed[i], present[i] = d, ok
	}
	for i, f := range fields {
		if present[i] {
			*f.dst = parsed[i]
		}
	}
	return nil
}

// durationField pairs a duration's wire value with where it is stored.
type durationField struct {
	dst  *time.Duration
	src  *string
	name string
}

// energyJSON, availabilityJSON and houseJSON are the wire shapes the
// UnmarshalJSON methods below read. They are named so the example-document
// tests can decode each section strictly: DisallowUnknownFields does not
// reach inside a custom UnmarshalJSON.
type energyJSON struct {
	// A pointer, like the durations, so an energy block that names only
	// max_integration_gap does not zero the warning threshold (#86).
	DivergenceWarningPct *float64 `json:"divergence_warning_pct"`
	MaxIntegrationGap    *string  `json:"max_integration_gap"`
}

type availabilityJSON struct {
	OfflineDebounce *string `json:"offline_debounce"`
}

type houseJSON struct {
	QuietAfter    *string `json:"quiet_after"`
	EmptyAfter    *string `json:"empty_after"`
	SleepingAfter *string `json:"sleeping_after"`
	// A pointer so absent (keep the current zone) differs from "" (an
	// explicit choice of UTC). It used to be assigned unconditionally, which
	// reset the zone whenever a house block left it out (#86).
	Timezone *string `json:"timezone"`
}

// UnmarshalJSON overlays the keys present in b onto e; see the note at the
// top of this file.
func (e *EnergyConfig) UnmarshalJSON(b []byte) error {
	var raw energyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	// Durations first: they are the only thing that can fail, and e must not
	// be touched if they do.
	if err := overlayDurations([]durationField{
		{&e.MaxIntegrationGap, raw.MaxIntegrationGap, "max_integration_gap"},
	}); err != nil {
		return err
	}
	if raw.DivergenceWarningPct != nil {
		e.DivergenceWarningPct = *raw.DivergenceWarningPct
	}
	return nil
}

// UnmarshalJSON overlays the keys present in b onto a; see the note at the
// top of this file.
func (a *AvailabilityConfig) UnmarshalJSON(b []byte) error {
	var raw availabilityJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	return overlayDurations([]durationField{
		{&a.OfflineDebounce, raw.OfflineDebounce, "offline_debounce"},
	})
}

// UnmarshalJSON overlays the keys present in b onto h; see the note at the
// top of this file. The timezone is stored as written: the remote fetcher
// checks that it resolves before the section is applied.
func (h *HouseConfig) UnmarshalJSON(b []byte) error {
	var raw houseJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := overlayDurations([]durationField{
		{&h.QuietAfter, raw.QuietAfter, "quiet_after"},
		{&h.EmptyAfter, raw.EmptyAfter, "empty_after"},
		{&h.SleepingAfter, raw.SleepingAfter, "sleeping_after"},
	}); err != nil {
		return err
	}
	if raw.Timezone != nil {
		h.Timezone = *raw.Timezone
	}
	return nil
}
