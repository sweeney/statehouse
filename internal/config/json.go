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

func (e *EnergyConfig) UnmarshalJSON(b []byte) error {
	var raw struct {
		DivergenceWarningPct float64 `json:"divergence_warning_pct"`
		MaxIntegrationGap    *string `json:"max_integration_gap"`
	}
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
	var raw struct {
		OfflineDebounce *string `json:"offline_debounce"`
	}
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
	var raw struct {
		QuietAfter    *string `json:"quiet_after"`
		EmptyAfter    *string `json:"empty_after"`
		SleepingAfter *string `json:"sleeping_after"`
		Timezone      string  `json:"timezone"`
	}
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

func (t *Thresholds) UnmarshalJSON(b []byte) error {
	var raw struct {
		IdleBelowW           *float64 `json:"idle_below_w"`
		ActiveAboveW         *float64 `json:"active_above_w"`
		CompressorAboveW     *float64 `json:"compressor_above_w"`
		ActiveSustainedFor   *string  `json:"active_sustained_for"`
		InactiveSustainedFor *string  `json:"inactive_sustained_for"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	t.IdleBelowW = raw.IdleBelowW
	t.ActiveAboveW = raw.ActiveAboveW
	t.CompressorAboveW = raw.CompressorAboveW
	for _, f := range []struct {
		dst  **time.Duration
		src  *string
		name string
	}{
		{&t.ActiveSustainedFor, raw.ActiveSustainedFor, "active_sustained_for"},
		{&t.InactiveSustainedFor, raw.InactiveSustainedFor, "inactive_sustained_for"},
	} {
		if f.src == nil {
			continue
		}
		d, err := parseDuration(f.src, f.name)
		if err != nil {
			return err
		}
		*f.dst = &d
	}
	return nil
}
