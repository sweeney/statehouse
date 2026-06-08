package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

// TestBuildDeviceResponse_LifetimeOmittedWhenAbsent confirms the lifetime
// block is absent from the wire when the device has no aggregates yet.
func TestBuildDeviceResponse_LifetimeOmittedWhenAbsent(t *testing.T) {
	now := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	d := model.Device{ID: "plug", Class: "short_burst_power_device"}

	resp := BuildDeviceResponse(d, now, nil)
	if resp.Lifetime != nil {
		t.Fatalf("expected nil Lifetime, got %+v", resp.Lifetime)
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["lifetime"]; ok {
		t.Errorf("expected lifetime key omitted, got: %s", b)
	}
}

// TestBuildDeviceResponse_LifetimeSerialized confirms the lifetime block and
// its extremum value/at shape appear on the wire when present.
func TestBuildDeviceResponse_LifetimeSerialized(t *testing.T) {
	now := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	peakAt := now.Add(-time.Minute)
	d := model.Device{
		ID:    "plug",
		Class: "short_burst_power_device",
		Lifetime: &model.Lifetime{
			MaxPower: &model.Extremum{Value: 2400, At: peakAt},
		},
	}

	resp := BuildDeviceResponse(d, now, nil)
	if resp.Lifetime == nil || resp.Lifetime.MaxPower == nil {
		t.Fatalf("expected lifetime max power, got %+v", resp.Lifetime)
	}
	if resp.Lifetime.MaxPower.Value != 2400 || !resp.Lifetime.MaxPower.At.Equal(peakAt) {
		t.Errorf("unexpected max power: %+v", resp.Lifetime.MaxPower)
	}
	// Only reported measurements are present.
	if resp.Lifetime.MinTemperature != nil {
		t.Errorf("expected no temperature extreme, got %+v", resp.Lifetime.MinTemperature)
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m struct {
		Lifetime struct {
			MaxPower *struct {
				Value float64   `json:"value"`
				At    time.Time `json:"at"`
			} `json:"max_power_w"`
			MinTemperature json.RawMessage `json:"min_temperature_c"`
		} `json:"lifetime"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Lifetime.MaxPower == nil || m.Lifetime.MaxPower.Value != 2400 || !m.Lifetime.MaxPower.At.Equal(peakAt) {
		t.Errorf("max_power_w wire shape wrong: %s", b)
	}
	if m.Lifetime.MinTemperature != nil {
		t.Errorf("expected min_temperature_c omitted, got: %s", b)
	}
}
