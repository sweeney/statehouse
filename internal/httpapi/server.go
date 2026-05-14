package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/sweeney/statehouse/internal/history"
	"github.com/sweeney/statehouse/internal/influx"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/mqtt"
	"github.com/sweeney/statehouse/internal/state"
)

// Server hosts the JSON HTTP API.
type Server struct {
	Listen string
	Store  *state.Store
	Log    *history.Log
	MQTT   mqtt.Client
	Influx *influx.Writer
	Logger *slog.Logger

	started time.Time

	srv *http.Server

	canonicalCount uint64
	derivedCount   uint64
}

// New returns a configured Server.
func New(listen string, store *state.Store, log *history.Log, mqtt mqtt.Client, infl *influx.Writer, logger *slog.Logger) *Server {
	return &Server{
		Listen:  listen,
		Store:   store,
		Log:     log,
		MQTT:    mqtt,
		Influx:  infl,
		Logger:  logger,
		started: time.Now().UTC(),
	}
}

// OnCanonicalEvent satisfies state.CanonicalSink for metrics counting.
func (s *Server) OnCanonicalEvent(_ model.CanonicalEvent) { atomic.AddUint64(&s.canonicalCount, 1) }

// OnDerivedEvent satisfies state.EventSink for metrics counting.
func (s *Server) OnDerivedEvent(_ model.DerivedEvent) { atomic.AddUint64(&s.derivedCount, 1) }

// newMux builds and returns the ServeMux used by both Start and tests.
// Centralising route registration here means tests always exercise the
// same routes as the running server.
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

// Start runs the HTTP server until the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	s.srv = &http.Server{
		Addr:              s.Listen,
		Handler:           newMux(s),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := s.srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	type health struct {
		Status          string  `json:"status"`
		UptimeSeconds   float64 `json:"uptime_seconds"`
		MQTTConnected   bool    `json:"mqtt_connected"`
		InfluxEnabled   bool    `json:"influx_enabled"`
		InfluxReachable bool    `json:"influx_reachable,omitempty"`
		Goroutines      int     `json:"goroutines"`
	}
	h := health{
		Status:        "ok",
		UptimeSeconds: time.Since(s.started).Seconds(),
		MQTTConnected: s.MQTT != nil && s.MQTT.IsConnected(),
		InfluxEnabled: s.Influx != nil && s.Influx.Enabled,
		Goroutines:    runtime.NumGoroutine(),
	}
	if h.InfluxEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		h.InfluxReachable = s.Influx.Ping(ctx)
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildSnapshot(s.Store.Snapshot(), time.Now()))
}

func (s *Server) handleHouse(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildHouseResponse(s.Store.House()))
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	devices := s.Store.Devices()
	out := make(map[string]DeviceResponse, len(devices))
	for id, d := range devices {
		out[id] = buildDeviceResponse(d, now, nil)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/state/devices/"):]
	if id == "" {
		s.handleDevices(w, r)
		return
	}
	d, ok := s.Store.Get(id)
	if !ok {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, buildDeviceResponse(d, time.Now(), nil))
}

func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	if s.Log == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.Log.Recent(limit))
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	type metrics struct {
		UptimeSeconds   float64 `json:"uptime_seconds"`
		DeviceCount     int     `json:"device_count"`
		CanonicalEvents uint64  `json:"canonical_events_total"`
		DerivedEvents   uint64  `json:"derived_events_total"`
		InfluxQueued    uint64  `json:"influx_writes_queued,omitempty"`
		InfluxFailure   uint64  `json:"influx_writes_failure,omitempty"`
	}
	m := metrics{
		UptimeSeconds:   time.Since(s.started).Seconds(),
		DeviceCount:     len(s.Store.Devices()),
		CanonicalEvents: atomic.LoadUint64(&s.canonicalCount),
		DerivedEvents:   atomic.LoadUint64(&s.derivedCount),
	}
	if s.Influx != nil && s.Influx.Enabled {
		m.InfluxQueued, m.InfluxFailure = s.Influx.Stats()
	}
	writeJSON(w, http.StatusOK, m)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
