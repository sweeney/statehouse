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
	mqttpkg "github.com/sweeney/statehouse/internal/mqtt"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// fakeClient is a minimal mqtt.Client for use in metrics tests.
type fakeClient struct{ reconnects uint64 }

func (f *fakeClient) Connect() error                                      { return nil }
func (f *fakeClient) Disconnect()                                         {}
func (f *fakeClient) Subscribe(_ string, _ byte, _ mqttpkg.Handler) error { return nil }
func (f *fakeClient) Publish(_ string, _ byte, _ bool, _ []byte) error    { return nil }
func (f *fakeClient) IsConnected() bool                                   { return true }
func (f *fakeClient) Reconnects() uint64                                  { return f.reconnects }

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
	srv := New(":0", store, log, nil, nil, nil, cfg.DeviceClasses)
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
	if snap.House.Activity.State == model.HouseActivityUnknown {
		t.Fatalf("expected house activity state to be set after high-power reading, got %q", snap.House.Activity.State)
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
	// mqtt_publishes_dropped_total must be present so operators can
	// alert on the new failure mode introduced by the bounded publish
	// queue (issue #50). The field reports 0 here because no Publisher
	// is wired to the test Server.
	if !strings.Contains(w.Body.String(), `"mqtt_publishes_dropped_total":0`) {
		t.Fatalf("expected mqtt_publishes_dropped_total=0 to be surfaced, got %s", w.Body.String())
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
	// All three dimension states must be non-empty strings; the valid zero
	// state is "unknown", not the empty string "".
	if string(h.Occupancy.State) == "" {
		t.Errorf("expected Occupancy.State to be a non-empty string (e.g. %q), got empty", model.OccupancyUnknown)
	}
	if string(h.Activity.State) == "" {
		t.Errorf("expected Activity.State to be a non-empty string (e.g. %q), got empty", model.HouseActivityUnknown)
	}
	if string(h.Mode.State) == "" {
		t.Errorf("expected Mode.State to be a non-empty string (e.g. %q), got empty", model.ModeUnknown)
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
		House:       model.House{},
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
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})
	if resp.SchemaVersion != "net.swee.statehouse.snapshot.v1" {
		t.Errorf("expected schema_version %q, got %q", "net.swee.statehouse.snapshot.v1", resp.SchemaVersion)
	}
}

func TestSnapshot_AgoFieldsPresentWhenTimestampNull(t *testing.T) {
	// When last_changed and last_seen are null (device not yet seen), the
	// _ago fields must still appear in the JSON as null — not be omitted.
	// A consumer should be able to rely on the key always being present.
	d := freshDevice("d1", "continuous_power_device")
	// zero times → both timestamps will be null

	snap := makeDeviceSnap(d)
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	raw, _ := json.Marshal(resp.Devices["d1"])
	rawStr := string(raw)

	for _, key := range []string{`"last_changed_ago"`, `"last_seen_ago"`} {
		if !strings.Contains(rawStr, key) {
			t.Errorf("expected %s key in device JSON even when null, got: %s", key, rawStr)
		}
	}
}

func TestSnapshot_NoZeroTimestamps(t *testing.T) {
	// Device with zero-value Activity.LastChanged — should become null, not "0001-01-01T00:00:00Z"
	d := freshDevice("d1", "short_burst_power_device")
	d.Activity.LastChanged = time.Time{} // zero

	snap := makeDeviceSnap(d)
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	dev := resp.Devices["d1"]
	if dev.Activity.LastChanged != nil {
		t.Errorf("expected null LastChanged for zero time, got %v", dev.Activity.LastChanged)
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
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	dev := resp.Devices["d1"]
	if !dev.Latest.Stale {
		t.Error("expected stale=true for device with LastSeen 20 min ago and 900s threshold")
	}
	if dev.Latest.LastSeenAgo == nil {
		t.Fatal("expected age_seconds to be present")
	}
	const wantAge = 1200
	if got := *dev.Latest.LastSeenAgo; got < wantAge-1 || got > wantAge+1 {
		t.Errorf("expected last_seen_ago ≈ %v, got %v", wantAge, got)
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
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

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
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

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
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

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
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

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
		House:       model.House{},
		Devices: map[string]model.Device{
			"d1": d1,
			"d2": d2,
		},
	}
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

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

func TestSnapshot_CycleDivergenceWarningInWarnings(t *testing.T) {
	// A finished cycle with DivergenceWarning=true must surface "cycle_divergence"
	// in device.Warnings so the summary warning_count picks it up.
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	finished := now.Add(-2 * time.Minute)
	d := freshDevice("d1", "cycle_power_device")
	d.Cycle = &model.Cycle{
		Active:          false,
		StartedAt:       now.Add(-10 * time.Minute),
		FinishedAt:      &finished,
		DurationSeconds: 480,
		Energy: model.CycleEnergy{
			PrimarySource:     "integration",
			ReportedKWhDelta:  0,
			IntegratedKWh:     0.007,
			SelectedKWh:       0.007,
			DivergencePct:     100,
			DivergenceWarning: true,
		},
	}
	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	dev := resp.Devices["d1"]
	found := false
	for _, w := range dev.Warnings {
		if w == "cycle_divergence" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cycle_divergence in warnings, got %v", dev.Warnings)
	}
	if resp.Summary.WarningCount != 1 {
		t.Errorf("expected warning_count=1 for cycle_divergence device, got %d", resp.Summary.WarningCount)
	}
}

func TestSnapshot_CycleDivergenceNotFlaggedWhenOK(t *testing.T) {
	// A finished cycle with DivergenceWarning=false must NOT add "cycle_divergence"
	// to warnings.
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	finished := now.Add(-2 * time.Minute)
	d := freshDevice("d1", "cycle_power_device")
	d.Cycle = &model.Cycle{
		Active:          false,
		StartedAt:       now.Add(-10 * time.Minute),
		FinishedAt:      &finished,
		DurationSeconds: 480,
		Energy: model.CycleEnergy{
			PrimarySource:     "counter",
			ReportedKWhDelta:  0.1,
			IntegratedKWh:     0.105,
			SelectedKWh:       0.1,
			DivergencePct:     4.8,
			DivergenceWarning: false,
		},
	}
	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	dev := resp.Devices["d1"]
	for _, w := range dev.Warnings {
		if w == "cycle_divergence" {
			t.Errorf("must NOT add cycle_divergence warning when DivergenceWarning=false, got %v", dev.Warnings)
		}
	}
	if resp.Summary.WarningCount != 0 {
		t.Errorf("expected warning_count=0, got %d", resp.Summary.WarningCount)
	}
}

func TestSnapshot_CompressorCycleType(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	started := now.Add(-5 * time.Minute)

	cases := []struct {
		class    string
		wantType string
	}{
		{"continuous_power_device", "compressor_cycle"},
		{"short_burst_power_device", "appliance_cycle"},
	}

	for _, tc := range cases {
		d := freshDevice("d1", tc.class)
		d.Cycle = &model.Cycle{
			Active:    true,
			StartedAt: started,
		}

		snap := makeDeviceSnap(d)
		resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

		dev := resp.Devices["d1"]
		if dev.Cycle == nil {
			t.Fatalf("class %q: expected cycle to be present", tc.class)
		}
		if dev.Cycle.Type != tc.wantType {
			t.Errorf("class %q: expected cycle.type %q, got %q", tc.class, tc.wantType, dev.Cycle.Type)
		}
	}
}

func TestSnapshot_DivergenceWarning(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	finished := now.Add(-2 * time.Minute)
	divPct := 18.5

	d := freshDevice("d1", "cycle_power_device")
	d.Cycle = &model.Cycle{
		Active:          false,
		StartedAt:       started,
		FinishedAt:      &finished,
		DurationSeconds: 480,
		Energy: model.CycleEnergy{
			PrimarySource:     "counter",
			ReportedKWhDelta:  0.1,
			IntegratedKWh:     0.1185,
			SelectedKWh:       0.1,
			DivergencePct:     divPct,
			DivergenceWarning: true,
		},
	}

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	dev := resp.Devices["d1"]
	if dev.Cycle == nil {
		t.Fatal("expected cycle to be present")
	}
	div := dev.Cycle.Energy.Divergence

	if div.Status != "warning" {
		t.Errorf("expected divergence.status %q, got %q", "warning", div.Status)
	}
	if div.Warning == nil {
		t.Fatal("expected divergence.warning to be non-nil")
	}
	if !*div.Warning {
		t.Errorf("expected divergence.warning == true, got false")
	}
	if div.Pct == nil {
		t.Fatal("expected divergence.pct to be non-nil")
	}
	if math.Abs(*div.Pct-divPct) > 0.001 {
		t.Errorf("expected divergence.pct ≈ %v, got %v", divPct, *div.Pct)
	}
	if div.Reason != "" {
		t.Errorf("expected divergence.reason to be empty for a finished cycle, got %q", div.Reason)
	}
}

func TestSnapshot_ZeroLastSeenNotStale(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)

	// freshDevice leaves Latest.LastSeen at its zero value.
	d := freshDevice("d1", "short_burst_power_device")

	snap := makeDeviceSnap(d)
	resp := buildSnapshot(snap, nil, nil, now, nil, time.Time{})

	dev := resp.Devices["d1"]

	if dev.Latest.Stale {
		t.Error("expected stale=false for device with zero LastSeen")
	}
	if dev.Latest.LastSeenAgo != nil {
		t.Errorf("expected last_seen_ago to be null for zero LastSeen, got %v", *dev.Latest.LastSeenAgo)
	}

	hasStaleWarning := false
	for _, w := range dev.Warnings {
		if w == "stale_device" {
			hasStaleWarning = true
		}
	}
	if hasStaleWarning {
		t.Errorf("expected warnings to not contain %q for zero LastSeen, got %v", "stale_device", dev.Warnings)
	}

	// Verify raw JSON: last_seen must be null, last_seen_ago must be null (present but null).
	raw, _ := json.Marshal(dev.Latest)
	rawStr := string(raw)
	if !strings.Contains(rawStr, `"last_seen":null`) {
		t.Errorf("expected last_seen:null in JSON for zero LastSeen, got %s", rawStr)
	}
	if !strings.Contains(rawStr, `"last_seen_ago":null`) {
		t.Errorf("expected last_seen_ago:null in JSON for zero LastSeen, got %s", rawStr)
	}
}

// TestIdentity_PresentInDevicesEndpoint verifies that /state/devices/{id}
// and /state/devices include the identity block with scheme, primary, and display.
func TestIdentity_PresentInDevicesEndpoint(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	p := 2000.0
	engine.IngestReading(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc123", Display: "kettle"}, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: ts, PowerW: &p})

	// Single device endpoint.
	r := httptest.NewRequest(http.MethodGet, "/state/devices/kettle", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var dev DeviceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &dev); err != nil {
		t.Fatalf("parse /state/devices/kettle: %v", err)
	}
	if dev.Identity == nil {
		t.Fatal("identity must be present in /state/devices/{id}, got nil")
	}
	if dev.Identity.Scheme != "zigbee" {
		t.Errorf("identity.scheme = %q, want zigbee", dev.Identity.Scheme)
	}
	if dev.Identity.Primary != "0xabc123" {
		t.Errorf("identity.primary = %q, want 0xabc123", dev.Identity.Primary)
	}
	if dev.Identity.Display != "kettle" {
		t.Errorf("identity.display = %q, want kettle", dev.Identity.Display)
	}

	// Device list endpoint.
	r = httptest.NewRequest(http.MethodGet, "/state/devices", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var devMap map[string]DeviceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &devMap); err != nil {
		t.Fatalf("parse /state/devices: %v", err)
	}
	if devMap["kettle"].Identity == nil {
		t.Fatal("identity must be present in /state/devices list, got nil")
	}
}

// TestIdentity_AbsentInSnapshot verifies that the identity block is omitted
// from device entries in the /state snapshot response.
func TestIdentity_AbsentInSnapshot(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	p := 2000.0
	engine.IngestReading(model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc123", Display: "kettle"}, "zigbee2mqtt/kettle",
		model.Reading{Timestamp: ts, PowerW: &p})

	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	var snap SnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("parse /state: %v", err)
	}
	dev, ok := snap.Devices["kettle"]
	if !ok {
		t.Fatal("kettle not found in snapshot devices")
	}
	if dev.Identity != nil {
		t.Errorf("identity must be absent in /state snapshot, got %+v", dev.Identity)
	}

	// Also verify via raw JSON that the key is not present at all.
	raw := w.Body.Bytes()
	if strings.Contains(string(raw), `"identity"`) {
		t.Error("identity key must not appear anywhere in /state JSON")
	}
}

func TestHandleConfigDevices_EmptyStore(t *testing.T) {
	srv, _ := setup(t)
	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/config/devices", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var out map[string]DeviceProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(out))
	}
}

func TestHandleConfigDevices_ResolutionSources(t *testing.T) {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"short_burst_power_device": {
			NameHints:      []string{"kettle"},
			EnergyStrategy: "counter",
			DefaultThresholds: config.Thresholds{
				IdleBelowW:   testutil.PtrF64(5),
				ActiveAboveW: testutil.PtrF64(50),
			},
		},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"the_kettle": {
			Scheme:         "zigbee",
			Primary:        "0xaabbccddeeff0011",
			Class:          "short_burst_power_device",
			EnergyStrategy: "integration",
		},
	}
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	srv := New(":0", store, nil, nil, nil, nil, cfg.DeviceClasses)

	mux := newMux(srv)

	// device_config: matched by primary key in cfg.Devices.
	engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0xaabbccddeeff0011", Display: "kettle"},
		"zigbee2mqtt/kettle",
	)
	// name_hint: no device config entry, display name triggers name hint.
	engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0xdeadbeef", Display: "another_kettle"},
		"zigbee2mqtt/another_kettle",
	)
	// unclassified: no match at all.
	engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0x1234", Display: "mystery_box"},
		"zigbee2mqtt/mystery_box",
	)

	r := httptest.NewRequest(http.MethodGet, "/config/devices", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var out map[string]DeviceProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(out), out)
	}

	kettle := out["the_kettle"]
	if kettle.Resolution != "device_config" {
		t.Errorf("the_kettle: want resolution=device_config, got %q", kettle.Resolution)
	}
	if kettle.Class != "short_burst_power_device" {
		t.Errorf("the_kettle: want class=short_burst_power_device, got %q", kettle.Class)
	}
	if kettle.EnergyStrategy != "integration" {
		t.Errorf("the_kettle: want energy_strategy=integration (per-device override), got %q", kettle.EnergyStrategy)
	}
	if kettle.Thresholds == nil {
		t.Error("the_kettle: thresholds must be present (inherited from class config)")
	}

	// name_hint: find by display name since we don't have a stable ID.
	var hintDevice DeviceProfileResponse
	for id, d := range out {
		if id != "the_kettle" && d.Class == "short_burst_power_device" {
			hintDevice = d
			break
		}
	}
	if hintDevice.Resolution != "name_hint" {
		t.Errorf("another_kettle: want resolution=name_hint, got %q", hintDevice.Resolution)
	}
	if hintDevice.EnergyStrategy != "counter" {
		t.Errorf("another_kettle: want energy_strategy=counter (class default), got %q", hintDevice.EnergyStrategy)
	}

	// unclassified.
	var unclassified DeviceProfileResponse
	for _, d := range out {
		if d.Resolution == "unclassified" {
			unclassified = d
			break
		}
	}
	if unclassified.Class != "unclassified" {
		t.Errorf("mystery_box: want class=unclassified, got %q", unclassified.Class)
	}
	if unclassified.Thresholds != nil {
		t.Error("unclassified device must not have thresholds")
	}
}

func TestHandleConfigDevice_SingleLookup(t *testing.T) {
	srv, engine := setup(t)
	mux := newMux(srv)
	engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"},
		"zigbee2mqtt/kettle",
	)

	// Known device — 200.
	r := httptest.NewRequest(http.MethodGet, "/config/devices/kettle", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var p DeviceProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Class != "short_burst_power_device" {
		t.Errorf("want class=short_burst_power_device, got %q", p.Class)
	}

	// Unknown device — 404.
	r = httptest.NewRequest(http.MethodGet, "/config/devices/no_such_device", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown device, got %d", w.Code)
	}
}

func TestHandleConfigDevices_Thresholds(t *testing.T) {
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	inactiveDur := 2 * time.Second
	activeDur := 500 * time.Millisecond
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"short_burst_power_device": {
			NameHints:      []string{"kettle"},
			EnergyStrategy: "counter",
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           testutil.PtrF64(5),
				ActiveAboveW:         testutil.PtrF64(50),
				ActiveSustainedFor:   &activeDur,
				InactiveSustainedFor: &inactiveDur,
			},
		},
	}
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	srv := New(":0", store, nil, nil, nil, nil, cfg.DeviceClasses)
	mux := newMux(srv)

	engine.EnsureDiscovered(
		model.DeviceIdentity{Scheme: "zigbee", Primary: "0xabc", Display: "kettle"},
		"zigbee2mqtt/kettle",
	)

	r := httptest.NewRequest(http.MethodGet, "/config/devices", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	var out map[string]DeviceProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var p DeviceProfileResponse
	for _, v := range out {
		p = v
		break
	}
	if p.Thresholds == nil {
		t.Fatal("expected thresholds to be present")
	}
	if p.Thresholds.IdleBelowW == nil || *p.Thresholds.IdleBelowW != 5 {
		t.Errorf("want idle_below_w=5, got %v", p.Thresholds.IdleBelowW)
	}
	if p.Thresholds.ActiveAboveW == nil || *p.Thresholds.ActiveAboveW != 50 {
		t.Errorf("want active_above_w=50, got %v", p.Thresholds.ActiveAboveW)
	}
	if p.Thresholds.ActiveSustainedSec == nil || *p.Thresholds.ActiveSustainedSec != 0.5 {
		t.Errorf("want active_sustained_for_sec=0.5, got %v", p.Thresholds.ActiveSustainedSec)
	}
	if p.Thresholds.InactiveSustainedSec == nil || *p.Thresholds.InactiveSustainedSec != 2.0 {
		t.Errorf("want inactive_sustained_for_sec=2.0, got %v", p.Thresholds.InactiveSustainedSec)
	}
}

func TestHandleConfigDevices_ContentType(t *testing.T) {
	srv, _ := setup(t)
	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/config/devices", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestHandleMetrics_HeapStats(t *testing.T) {
	srv, _ := setup(t)
	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, field := range []string{"heap_alloc_bytes", "heap_sys_bytes", "gc_cycles_total", "last_gc_pause_ms"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in /metrics", field)
		}
	}
	if v, _ := m["heap_alloc_bytes"].(float64); v <= 0 {
		t.Errorf("heap_alloc_bytes should be > 0, got %v", v)
	}
	if v, _ := m["heap_sys_bytes"].(float64); v <= 0 {
		t.Errorf("heap_sys_bytes should be > 0, got %v", v)
	}
}

func TestHandleMetrics_RecentLogStats(t *testing.T) {
	cfg := config.Default()
	store := state.NewStore()
	log, _ := history.Open("", 1, 1, 16)
	srv := New(":0", store, log, nil, nil, nil, cfg.DeviceClasses)
	mux := newMux(srv)

	// Append events directly — Stats() reports what's in the log regardless
	// of how it got there.
	for i := 0; i < 3; i++ {
		_ = log.Append("derived", map[string]any{"type": "test_event"})
	}

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := m["recent_log_events"]; !ok {
		t.Error("missing field recent_log_events")
	}
	if _, ok := m["recent_log_size_bytes"]; !ok {
		t.Error("missing field recent_log_size_bytes")
	}
	// Discovery emits one event; the in-memory log must reflect it.
	if v, _ := m["recent_log_events"].(float64); v < 1 {
		t.Errorf("recent_log_events should be >= 1, got %v", v)
	}
}

func TestHandleMetrics_MQTTReconnects(t *testing.T) {
	srv, _ := setup(t)
	srv.MQTT = &fakeClient{reconnects: 3}
	mux := newMux(srv)

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"mqtt_reconnects_total":3`) {
		t.Errorf("expected mqtt_reconnects_total:3, got %s", w.Body.String())
	}
}
