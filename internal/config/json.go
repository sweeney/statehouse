package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Issue #86: each behaviour section used to be decoded into a fresh zero
// struct, so a key the document left out became zero rather than its
// default. Production's house block left out sleeping_after, which became
// 0s, and the house never entered sleeping mode.
//
// The UnmarshalJSON methods below therefore decode over a copy of Default()'s
// section: a key that is absent or null keeps its default, and a key that is
// present replaces it, including an explicit zero ("0s" for sleeping_after is
// documented as "never enter sleeping"). They start from Default() rather
// than from the receiver because encoding/json hands them a freshly allocated
// zero struct for a *Section field, so the receiver holds nothing to keep.
// Every wire field is a pointer so absent (nil) and present differ.

// parseDuration parses a duration string (e.g. "30m", "10s") from a JSON
// field. ok is false when the key is absent, null or "" (all of which leave
// the default in place); a string that does not parse is an error.
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

// overlayDuration sets *dst from s when the key is present, and leaves it
// alone when it is absent.
func overlayDuration(dst *time.Duration, s *string, field string) error {
	d, ok, err := parseDuration(s, field)
	if ok {
		*dst = d
	}
	return err
}

// energyJSON, availabilityJSON and houseJSON are the wire shapes the
// UnmarshalJSON methods below read. They are named so the example-document
// tests can decode each section strictly: DisallowUnknownFields does not
// reach inside a custom UnmarshalJSON.
type energyJSON struct {
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
	Timezone      *string `json:"timezone"`
}

// UnmarshalJSON sets e to Default().Energy with the keys present in b applied.
func (e *EnergyConfig) UnmarshalJSON(b []byte) error {
	var raw energyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	next := Default().Energy
	if raw.DivergenceWarningPct != nil {
		next.DivergenceWarningPct = *raw.DivergenceWarningPct
	}
	if err := overlayDuration(&next.MaxIntegrationGap, raw.MaxIntegrationGap, "max_integration_gap"); err != nil {
		return err
	}
	*e = next
	return nil
}

// UnmarshalJSON sets a to Default().Availability with the keys present in b
// applied.
func (a *AvailabilityConfig) UnmarshalJSON(b []byte) error {
	var raw availabilityJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	next := Default().Availability
	if err := overlayDuration(&next.OfflineDebounce, raw.OfflineDebounce, "offline_debounce"); err != nil {
		return err
	}
	*a = next
	return nil
}

// UnmarshalJSON sets h to Default().House with the keys present in b applied.
// The timezone is stored as written: the remote fetcher checks that it
// resolves before the section is applied.
func (h *HouseConfig) UnmarshalJSON(b []byte) error {
	var raw houseJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	next := Default().House
	if raw.Timezone != nil {
		next.Timezone = *raw.Timezone
	}
	for _, f := range []struct {
		dst  *time.Duration
		src  *string
		name string
	}{
		{&next.QuietAfter, raw.QuietAfter, "quiet_after"},
		{&next.EmptyAfter, raw.EmptyAfter, "empty_after"},
		{&next.SleepingAfter, raw.SleepingAfter, "sleeping_after"},
	} {
		if err := overlayDuration(f.dst, f.src, f.name); err != nil {
			return err
		}
	}
	*h = next
	return nil
}
