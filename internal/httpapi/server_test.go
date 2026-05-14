package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/history"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func setup(t *testing.T) (*Server, *state.Engine) {
	t.Helper()
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"short_burst_power_device": {
			DefaultThresholds: config.Thresholds{
				IdleBelowW:   testutil.PtrF64(5),
				ActiveAboveW: testutil.PtrF64(50),
			},
			EnergyStrategy: "integration",
			NameHints:      []string{"kettle"},
		},
	}
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	log, _ := history.Open("", 1, 1, 16)
	srv := New(":0", store, log, nil, nil, nil)
	engine.AddCanonicalSink(srv)
	engine.AddDerivedSink(srv)
	return srv, engine
}

func TestHandleHealth(t *testing.T) {
	srv, _ := setup(t)
	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("expected status ok, got %s", body)
	}
}

func TestHandleStateAndDevices(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	p := 2000.0
	engine.IngestReading(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"}, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: ts, PowerW: &p})

	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	var snap SnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(snap.Devices))
	}
	if snap.House.State != model.HouseActive {
		t.Fatalf("expected house active, got %q", snap.House.State)
	}

	r = httptest.NewRequest(http.MethodGet, "/state/devices/kettle", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200 for /state/devices/kettle, got %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/state/devices/unknown", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown device, got %d", w.Code)
	}
}

func TestHandleRecent(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	p := 2000.0
	engine.IngestReading(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"}, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: ts, PowerW: &p})

	r := httptest.NewRequest(http.MethodGet, "/events/recent?limit=5", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var entries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Local log is in-memory only (nil path) so we expect zero entries.
	// But the server should still respond 200 with []. This validates
	// HTTP plumbing in the no-log case.
	_ = entries
}

func TestHandleMetrics(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	p := 2000.0
	engine.IngestReading(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"}, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: ts, PowerW: &p})

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"device_count":1`) {
		t.Fatalf("expected device_count=1, got %s", w.Body.String())
	}
}

func TestHandleHouse(t *testing.T) {
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
		t.Fatalf("parse: %v", err)
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	srv, _ := setup(t)
	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/no-such-route", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown route, got %d", w.Code)
	}
}

// makeDeviceSnap builds a model.Snapshot with a single device for DTO tests.
func makeDeviceSnap(d model.Device) model.Snapshot {
	return model.Snapshot{
		GeneratedAt: time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		House:       model.House{State: model.HouseUnknown},
		Devices:     map[string]model.Device{d.ID: d},
	}
}

// freshDevice returns a minimal device with no zero-time surprises for tests
// that don't need specific time values.
func freshDevice(id, class string) model.Device {
	return model.Device{
		ID:           id,
		Class:        class,
		Availability: model.AvailabilityOnline,
		Activity: model.Activity{
			State: model.ActivityIdle,
		},
	}
}

func TestSnapshot_SchemaVersion(t *testing.T) {
	snap := makeDeviceSnap(freshDevice("d1", "short_burst_power_device"))
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	resp := buildSnapshot(snap, now)
	if resp.SchemaVersion != "statehouse.snapshot.v1" {
		t.Errorf("expected schema_version %q, got %q", "statehouse.snapshot.v1", resp.SchemaVersion)
	}
}

func TestSnapshot_NoZeroTimestamps(t *testing.T) {
	// Device with zero-value Activity.Since — should become null, not "0001-01-01T00:00:00Z"
	d := freshDevice("d1", "short_burst_power_device")
	d.Activity.Since = time.Time{} // zero

	snap := makeDeviceSnap(d)
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	resp := buildSnapshot(snap, now)

	dev := resp.Devices["d1"]
	if dev.Activity.Since != nil {
		t.Errorf("expected null Since for zero time, got %v", dev.Activity.Since)
	}

	// Verify via raw JSON that there's no "0001-01-01" string
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "0001-01-01") {
		t.Errorf("response JSON contains zero-time string: %s", string(raw))
	}
}

func TestSnapshot_AgeAndStale(t *testing.T) {
	// LastSeen 20 min ago; class threshold for short_burst_power_device is 900s.
	// Age = 1200s > 900s → stale == true, warnings contains "stale_device".
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-20 * time.Minute) // 1200s ago

	d := freshDevice("d1", "short_burst_power_device")
	d.Latest.LastSeen = lastSeen

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, now)

	dev := resp.Devices["d1"]
	if !dev.Latest.Stale {
		t.Error("expected stale=true for device with LastSeen 20 min ago and 900s threshold")
	}
	if dev.Latest.AgeSeconds == nil {
		t.Fatal("expected age_seconds to be present")
	}
	const wantAge = 1200.0
	if math.Abs(*dev.Latest.AgeSeconds-wantAge) > 1.0 {
		t.Errorf("expected age_seconds ≈ %v, got %v", wantAge, *dev.Latest.AgeSeconds)
	}

	hasStaleWarning := false
	for _, w := range dev.Warnings {
		if w == "stale_device" {
			hasStaleWarning = true
		}
	}
	if !hasStaleWarning {
		t.Errorf("expected warnings to contain %q, got %v", "stale_device", dev.Warnings)
	}
}

func TestSnapshot_CycleType(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	started := now.Add(-5 * time.Minute)

	d := freshDevice("d1", "cycle_power_device")
	d.Cycle = &model.Cycle{
		Active:    true,
		StartedAt: started,
	}

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, now)

	dev := resp.Devices["d1"]
	if dev.Cycle == nil {
		t.Fatal("expected cycle to be present")
	}
	if dev.Cycle.Type != "appliance_cycle" {
		t.Errorf("expected cycle.type %q, got %q", "appliance_cycle", dev.Cycle.Type)
	}
}

func TestSnapshot_DivergencePending(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	started := now.Add(-3 * time.Minute)

	d := freshDevice("d1", "cycle_power_device")
	d.Cycle = &model.Cycle{
		Active:    true,
		StartedAt: started,
	}

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, now)

	dev := resp.Devices["d1"]
	if dev.Cycle == nil {
		t.Fatal("expected cycle to be present")
	}
	div := dev.Cycle.Energy.Divergence
	if div.Status != "pending" {
		t.Errorf("expected divergence.status %q, got %q", "pending", div.Status)
	}
	if div.Reason != "cycle_active" {
		t.Errorf("expected divergence.reason %q, got %q", "cycle_active", div.Reason)
	}
	if div.Pct != nil {
		t.Errorf("expected divergence.pct to be absent when pending, got %v", *div.Pct)
	}
}

func TestSnapshot_DivergenceEvaluated(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	finished := now.Add(-2 * time.Minute)
	divPct := 5.5

	d := freshDevice("d1", "cycle_power_device")
	d.Cycle = &model.Cycle{
		Active:          false,
		StartedAt:       started,
		FinishedAt:      &finished,
		DurationSeconds: 480,
		Energy: model.CycleEnergy{
			PrimarySource:     "counter",
			ReportedKWhDelta:  0.1,
			IntegratedKWh:     0.105,
			SelectedKWh:       0.1,
			DivergencePct:     divPct,
			DivergenceWarning: false,
		},
	}

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, now)

	dev := resp.Devices["d1"]
	if dev.Cycle == nil {
		t.Fatal("expected cycle to be present")
	}
	div := dev.Cycle.Energy.Divergence
	if div.Status != "ok" && div.Status != "warning" {
		t.Errorf("expected divergence.status ok or warning, got %q", div.Status)
	}
	if div.Pct == nil {
		t.Fatal("expected divergence.pct to be present for a finished cycle")
	}
	if math.Abs(*div.Pct-divPct) > 0.001 {
		t.Errorf("expected divergence.pct=%v, got %v", divPct, *div.Pct)
	}
	if div.Warning == nil {
		t.Fatal("expected divergence.warning to be present for a finished cycle")
	}
}

func TestSnapshot_WarningsAlwaysPresent(t *testing.T) {
	// A fresh device with a very recent LastSeen should have warnings: [] not null.
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	d := freshDevice("d1", "short_burst_power_device")
	d.Latest.LastSeen = now.Add(-1 * time.Minute) // recent → not stale

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, now)

	dev := resp.Devices["d1"]
	if dev.Warnings == nil {
		t.Error("expected warnings to be a non-nil slice, got nil")
	}

	// Also verify via JSON: should be [] not null or absent.
	raw, _ := json.Marshal(dev)
	if !strings.Contains(string(raw), `"warnings":[]`) {
		t.Errorf("expected warnings:[] in JSON, got %s", string(raw))
	}
}

func TestSnapshot_Summary(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)

	// Device 1: online, active, not stale
	d1 := freshDevice("d1", "short_burst_power_device")
	d1.Availability = model.AvailabilityOnline
	d1.Activity.State = model.ActivityActive
	d1.Latest.LastSeen = now.Add(-1 * time.Minute)

	// Device 2: offline, idle, stale (20 min ago, threshold 900s)
	d2 := freshDevice("d2", "short_burst_power_device")
	d2.Availability = model.AvailabilityOffline
	d2.Activity.State = model.ActivityIdle
	d2.Latest.LastSeen = now.Add(-20 * time.Minute)

	snap := model.Snapshot{
		GeneratedAt: now,
		House:       model.House{State: model.HouseUnknown},
		Devices: map[string]model.Device{
			"d1": d1,
			"d2": d2,
		},
	}
	resp := buildSnapshot(snap, now)

	if resp.Summary.DeviceCount != 2 {
		t.Errorf("expected device_count=2, got %d", resp.Summary.DeviceCount)
	}
	if resp.Summary.OnlineCount != 1 {
		t.Errorf("expected online_count=1, got %d", resp.Summary.OnlineCount)
	}
	if resp.Summary.ActiveCount != 1 {
		t.Errorf("expected active_count=1, got %d", resp.Summary.ActiveCount)
	}
	// d2 is stale → warnings=["stale_device"] → warning_count=1
	if resp.Summary.WarningCount != 1 {
		t.Errorf("expected warning_count=1, got %d", resp.Summary.WarningCount)
	}
}
