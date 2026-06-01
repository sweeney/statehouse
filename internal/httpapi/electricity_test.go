package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/history"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func TestBuildHouseResponse_NoElectricity_Omitted(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	h := model.House{}
	resp := buildHouseResponse(h, now)
	if resp.Electricity != nil {
		t.Fatalf("Electricity must be nil when ComputedAt is zero; got %+v", resp.Electricity)
	}

	body, _ := json.Marshal(resp)
	if got := string(body); contains(got, `"electricity"`) {
		t.Fatalf("electricity key must be omitted from JSON, got %s", got)
	}
}

func TestBuildHouseResponse_ElectricityPresent(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 30, 0, time.UTC)
	computedAt := now.Add(-25 * time.Second)
	h := model.House{
		Electricity: model.ElectricitySummary{
			GrossW: 1500, MonitoredW: 800, UnmonitoredW: 700,
			Coverage:       0.5333,
			GrossKWh:       2.5, MonitoredKWh: 1.3, UnmonitoredKWh: 1.2,
			StaleDeviceCount: 1, StaleDevices: []string{"plug_a"},
			ComputedAt: computedAt,
		},
	}
	resp := buildHouseResponse(h, now)
	if resp.Electricity == nil {
		t.Fatalf("Electricity should be present")
	}
	if resp.Electricity.GrossW != 1500 {
		t.Errorf("GrossW=%v", resp.Electricity.GrossW)
	}
	if resp.Electricity.ComputedAgo == nil || *resp.Electricity.ComputedAgo != 25 {
		t.Errorf("ComputedAgo=%v want 25", resp.Electricity.ComputedAgo)
	}
	if resp.Electricity.ComputedAt == nil || !resp.Electricity.ComputedAt.Equal(computedAt) {
		t.Errorf("ComputedAt mismatch")
	}
}

func setupWithMeter(t *testing.T) (*Server, *state.Engine) {
	t.Helper()
	cfg := config.Default()
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.Energy.Electricity = config.ElectricityConfig{
		StalenessActive: 60 * time.Second,
		StalenessIdle:   10 * time.Minute,
		IdleBelowW:      5,
	}
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"energy_meter": {},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"house_meter": {Scheme: "meter", Primary: "abcd", Class: "energy_meter"},
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

func TestHandleHouse_ElectricityEndToEnd(t *testing.T) {
	srv, engine := setupWithMeter(t)
	ts := time.Now()
	gross := 1234.0
	engine.IngestReading(
		model.DeviceIdentity{Scheme: "meter", Primary: "abcd", Display: "abcd"},
		"energy/abcd/SENSOR/electricitymeter",
		model.Reading{Timestamp: ts, PowerW: &gross},
	)

	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/state/house", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}

	var hr HouseResponse
	if err := json.Unmarshal(w.Body.Bytes(), &hr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hr.Electricity == nil {
		t.Fatalf("electricity block missing from /state/house: %s", w.Body.String())
	}
	if hr.Electricity.GrossW != gross {
		t.Errorf("GrossW=%v want %v", hr.Electricity.GrossW, gross)
	}
	// Use a deterministic assertion: ComputedAgo must be set and
	// non-negative. Asserting a specific value would race the handler's
	// clock and is not what this test is actually verifying.
	if hr.Electricity.ComputedAgo == nil {
		t.Errorf("ComputedAgo missing from response")
	} else if *hr.Electricity.ComputedAgo < 0 {
		t.Errorf("ComputedAgo=%d must not be negative", *hr.Electricity.ComputedAgo)
	}
	if hr.Electricity.ComputedAt == nil {
		t.Errorf("ComputedAt missing from response")
	}
}

// TestHandleState_ElectricityInFullSnapshot guards the contract that
// the electricity block is surfaced via /state as well as /state/house —
// the existing /state test in server_test.go doesn't cover it.
func TestHandleState_ElectricityInFullSnapshot(t *testing.T) {
	srv, engine := setupWithMeter(t)
	ts := time.Now()
	gross := 1500.0
	engine.IngestReading(
		model.DeviceIdentity{Scheme: "meter", Primary: "abcd", Display: "abcd"},
		"energy/abcd/SENSOR/electricitymeter",
		model.Reading{Timestamp: ts, PowerW: &gross},
	)

	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var snap SnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.House.Electricity == nil {
		t.Fatalf("electricity missing from /state snapshot: %s", w.Body.String())
	}
	if _, leaked := snap.Devices[state.HouseDeviceID]; leaked {
		t.Fatalf("synthetic 'house' device leaked into /state devices")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
