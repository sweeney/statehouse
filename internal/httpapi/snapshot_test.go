package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

// TestStalenessConfigOverride verifies that a non-nil StalenessSeconds pointer
// in DeviceClassConfig overrides the class default in stalenessSecondsForClass
// and propagates through buildDeviceResponse.
func TestStalenessConfigOverride(t *testing.T) {
	// Part 1: direct unit-test of stalenessSecondsForClass.
	const class = "short_burst_power_device"

	got := stalenessSecondsForClass(class, nil)
	if got != 900 {
		t.Errorf("stalenessSecondsForClass(%q, nil) = %v, want 900", class, got)
	}

	sixty := 60
	got = stalenessSecondsForClass(class, &sixty)
	if got != 60 {
		t.Errorf("stalenessSecondsForClass(%q, &60) = %v, want 60", class, got)
	}

	// Part 2: end-to-end through buildDeviceResponse.
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-2 * time.Minute) // 120 s ago

	d := freshDevice("k1", class)
	d.Latest.LastSeen = lastSeen

	// Class default threshold is 900 s; 120 s < 900 s → not stale.
	resp := buildDeviceResponse(d, now, nil)
	if resp.Latest.Stale {
		t.Error("expected stale=false with class-default threshold (900 s) and LastSeen 2 min ago")
	}

	// Override to 60 s; 120 s >= 60 s → stale.
	resp = buildDeviceResponse(d, now, &sixty)
	if !resp.Latest.Stale {
		t.Error("expected stale=true with override threshold (60 s) and LastSeen 2 min ago")
	}

	hasStaleWarning := false
	for _, w := range resp.Warnings {
		if w == "stale_device" {
			hasStaleWarning = true
		}
	}
	if !hasStaleWarning {
		t.Errorf("expected warnings to contain %q, got %v", "stale_device", resp.Warnings)
	}
}

// TestHandleDevices_DTOFields verifies that GET /state/devices returns proper
// DTO-transformed devices with non-null fields and no zero-time strings.
func TestHandleDevices_DTOFields(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)

	ts := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	p := 2.0 // low power — idle kettle
	engine.IngestReading(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"},
		"zigbee2mqtt/kettle",
		model.Reading{Timestamp: ts, PowerW: &p},
	)

	r := httptest.NewRequest(http.MethodGet, "/state/devices", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var devices map[string]DeviceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &devices); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	// The device key is its ID which matches the display name "kettle".
	dev, ok := devices["kettle"]
	if !ok {
		t.Fatalf("expected device %q to be present; got keys: %v", "kettle", keys(devices))
	}

	// Warnings must be a non-nil slice ([] not null).
	if dev.Warnings == nil {
		t.Error("expected Warnings to be a non-nil slice, got nil")
	}

	// LastSeen must be non-nil: device was just seen.
	if dev.Latest.LastSeen == nil {
		t.Error("expected Latest.LastSeen to be non-nil after ingesting a reading")
	}

	// Activity.Since and Activity.LastChanged must not be the zero time.
	raw, err := json.Marshal(dev)
	if err != nil {
		t.Fatalf("marshal device: %v", err)
	}
	rawStr := string(raw)
	if strings.Contains(rawStr, "0001-01-01") {
		t.Errorf("device JSON contains zero-time string: %s", rawStr)
	}
}

// TestHandleHouse_LastChangedNullWhenZero verifies that a house with a zero
// LastChanged timestamp serialises last_changed as null (not "0001-01-01…").
func TestHandleHouse_LastChangedNullWhenZero(t *testing.T) {
	srv, _ := setup(t)
	mux := newMux(srv)

	r := httptest.NewRequest(http.MethodGet, "/state/house", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var h HouseResponse
	if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	// A freshly-created house has zero LastChanged; DTO must map it to nil.
	if h.LastChanged != nil {
		t.Errorf("expected LastChanged to be nil for zero-time house, got %v", h.LastChanged)
	}

	// Verify via raw JSON bytes that the zero instant is absent.
	if strings.Contains(w.Body.String(), "0001-01-01") {
		t.Errorf("house JSON contains zero-time string: %s", w.Body.String())
	}
}

// keys returns the map keys as a slice, for diagnostic messages.
func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
