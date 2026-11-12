package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// parseDuration parses a duration string (e.g. "30m", "10s") from a JSON
// field. Returns an error if the string is present but invalid.
func parseDuration(s *string, field string) (time.Duration, error) {
	if s == nil || *s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(*s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	return d, nil
}

// energyJSON, availabilityJSON and houseJSON are the wire shapes the
// UnmarshalJSON methods below read. They are named so the example-document
// tests can decode each section strictly: DisallowUnknownFields does not
// reach inside a custom UnmarshalJSON.
type energyJSON struct {
	DivergenceWarningPct float64 `json:"divergence_warning_pct"`
	MaxIntegrationGap    *string `json:"max_integration_gap"`
}

type availabilityJSON struct {
	OfflineDebounce *string `json:"offline_debounce"`
}

type houseJSON struct {
	QuietAfter    *string `json:"quiet_after"`
	EmptyAfter    *string `json:"empty_after"`
	SleepingAfter *string `json:"sleeping_after"`
	Timezone      string  `json:"timezone"`
}

func (e *EnergyConfig) UnmarshalJSON(b []byte) error {
	var raw energyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	e.DivergenceWarningPct = raw.DivergenceWarningPct
	d, err := parseDuration(raw.MaxIntegrationGap, "max_integration_gap")
	if err != nil {
		return err
	}
	if d != 0 {
		e.MaxIntegrationGap = d
	}
	return nil
}

func (a *AvailabilityConfig) UnmarshalJSON(b []byte) error {
	var raw availabilityJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	d, err := parseDuration(raw.OfflineDebounce, "offline_debounce")
	if err != nil {
		return err
	}
	if d != 0 {
		a.OfflineDebounce = d
	}
	return nil
}

func (h *HouseConfig) UnmarshalJSON(b []byte) error {
	var raw houseJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	h.Timezone = raw.Timezone
	for _, f := range []struct {
		dst  *time.Duration
		src  *string
		name string
	}{
		{&h.QuietAfter, raw.QuietAfter, "quiet_after"},
		{&h.EmptyAfter, raw.EmptyAfter, "empty_after"},
		{&h.SleepingAfter, raw.SleepingAfter, "sleeping_after"},
	} {
		d, err := parseDuration(f.src, f.name)
		if err != nil {
			return err
		}
		if d != 0 {
			*f.dst = d
		}
	}
	return nil
}
