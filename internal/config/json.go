package config

import (
	"fmt"
	"time"
)

// The behaviour sections (energy, availability, house) are decoded as an
// overlay: each UnmarshalJSON below writes only the keys the document names
// and leaves every other field of the receiver as it was. overlayBehaviour
// (behaviour.go) calls them on a copy of Default()'s section, so a key a
// document leaves out keeps its default instead of becoming zero. That is
// issue #86: production's house block left out sleeping_after, which became
// 0s, and the house never entered sleeping mode.
//
// To make "absent" and "present" distinguishable, every field of the wire
// shapes is a pointer: nil means the key was absent or null (keep the current
// value), non-nil means it was present (replace it, even with a zero). An
// explicit zero is a real setting: `sleeping_after: "0s"` is documented as
// "never enter sleeping", so it must not be mistaken for "not set".
//
// Each decoder is strict (strictUnmarshal): an unknown or misspelt key is an
// error, not ignored. On any error the receiver is left exactly as it was.
// Range checks (negative durations, the timezone) are not done here but in
// each section's validate method, so one place owns a section's rules.

// parseDuration parses a duration string (e.g. "30m", "10s") from a JSON
// field. ok is false when the key is absent or null (s == nil), meaning
// "leave the current value alone". An empty string is an error rather than
// "unset": before #86 it decoded to 0 (sleeping off), so reading it as the
// default now would silently turn sleeping mode on. A string that does not
// parse is an error naming the field.
func parseDuration(s *string, field string) (d time.Duration, ok bool, err error) {
	if s == nil {
		return 0, false, nil
	}
	if *s == "" {
		return 0, false, fmt.Errorf("%s: empty string is not a duration; write \"0s\" or leave the key out", field)
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
// UnmarshalJSON methods below read. Their json tags are the complete list of
// keys each section accepts: strictUnmarshal refuses any other.
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
	if err := strictUnmarshal(b, &raw); err != nil {
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
	if err := strictUnmarshal(b, &raw); err != nil {
		return err
	}
	return overlayDurations([]durationField{
		{&a.OfflineDebounce, raw.OfflineDebounce, "offline_debounce"},
	})
}

// UnmarshalJSON overlays the keys present in b onto h; see the note at the
// top of this file. The timezone is stored as written: HouseConfig.validate
// checks that it resolves before the section is applied.
func (h *HouseConfig) UnmarshalJSON(b []byte) error {
	var raw houseJSON
	if err := strictUnmarshal(b, &raw); err != nil {
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
