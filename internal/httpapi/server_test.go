package httpapi

import (
	"encoding/json"
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
				IdleBelowW: 5, ActiveAboveW: 50,
				ActiveSustainedFor: 0, InactiveSustainedFor: 0,
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
	var snap model.Snapshot
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

// newMux constructs the same mux the running server would, without
// listening on a socket. We replicate the wiring here so we can hit
// handlers directly.
func newMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/state", s.handleState)
	mux.HandleFunc("/state/house", s.handleHouse)
	mux.HandleFunc("/state/devices", s.handleDevices)
	mux.HandleFunc("/state/devices/", s.handleDevice)
	mux.HandleFunc("/events/recent", s.handleRecent)
	mux.HandleFunc("/metrics", s.handleMetrics)
	return mux
}
