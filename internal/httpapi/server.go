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

	"github.com/sweeney/identity/common/auth"
	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/history"
	"github.com/sweeney/statehouse/internal/influx"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/mqtt"
	"github.com/sweeney/statehouse/internal/state"
)

// Server hosts the JSON HTTP API.
type Server struct {
	Listen        string
	Store         *state.Store
	Log           *history.Log
	MQTT          mqtt.Client
	Influx        *influx.Writer
	Logger        *slog.Logger
	DeviceClasses map[string]config.DeviceClassConfig

	// IdentityURL is the base URL of the identity service (e.g. "https://id.swee.net").
	// When set, all routes except /healthz require a valid Bearer JWT.
	// When empty, auth is disabled (useful for local development and tests).
	IdentityURL string

	// Publisher, when non-nil, surfaces its drop counter on /metrics.
	// Set by main.go after construction; tests may leave it nil.
	Publisher *mqtt.Publisher

	// RemoteConfig, when non-nil, surfaces namespace fetch status on /healthz.
	RemoteConfig *config.Fetcher

	// Version is the build commit set via -ldflags; empty string when running
	// outside a tagged deploy.
	Version string

	started time.Time

	srv      *http.Server
	verifier *auth.JWKSVerifier

	canonicalCount uint64
	derivedCount   uint64
}

// New returns a configured Server. deviceClasses may be nil; when a class is
// absent or its StalenessSeconds pointer is nil the DTO layer falls back to
// the per-class default.
func New(listen string, store *state.Store, log *history.Log, mqtt mqtt.Client, infl *influx.Writer, logger *slog.Logger, deviceClasses map[string]config.DeviceClassConfig) *Server {
	return &Server{
		Listen:        listen,
		Store:         store,
		Log:           log,
		MQTT:          mqtt,
		Influx:        infl,
		Logger:        logger,
		DeviceClasses: deviceClasses,
		started:       time.Now().UTC(),
	}
}

// stalenessFor returns the configured StalenessSeconds override for a device
// class, or nil to use the class default.
func (s *Server) stalenessFor(class string) *int {
	if s == nil || s.DeviceClasses == nil {
		return nil
	}
	if c, ok := s.DeviceClasses[class]; ok {
		return c.StalenessSeconds
	}
	return nil
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

	auth := s.authMiddleware()
	mux.Handle("/state", auth(http.HandlerFunc(s.handleState)))
	mux.Handle("/state/house", auth(http.HandlerFunc(s.handleHouse)))
	mux.Handle("/state/devices", auth(http.HandlerFunc(s.handleDevices)))
	mux.Handle("/state/devices/", auth(http.HandlerFunc(s.handleDevice)))
	mux.Handle("/state/activity", auth(http.HandlerFunc(s.handleActivity)))
	mux.Handle("/events/recent", auth(http.HandlerFunc(s.handleRecent)))
	mux.Handle("/metrics", auth(http.HandlerFunc(s.handleMetrics)))
	mux.Handle("/config/devices", auth(http.HandlerFunc(s.handleConfigDevices)))
	mux.Handle("/config/devices/", auth(http.HandlerFunc(s.handleConfigDevice)))
	return mux
}

// authMiddleware returns a middleware that validates Bearer JWTs when
// IdentityURL is set. When IdentityURL is empty it returns a no-op wrapper
// so existing tests and local runs work without auth configured.
func (s *Server) authMiddleware() func(http.Handler) http.Handler {
	if s.IdentityURL == "" {
		return func(h http.Handler) http.Handler { return h }
	}
	verifier, err := auth.NewJWKSVerifier(auth.JWKSVerifierConfig{
		IssuerURL: s.IdentityURL,
		Issuer:    s.IdentityURL,
		Logger:    s.Logger,
	})
	if err != nil {
		// Only fails when IssuerURL or Issuer is empty, guarded above.
		panic(err)
	}
	s.verifier = verifier
	return func(h http.Handler) http.Handler { return requireAuth(verifier, h) }
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
	type nsStatus struct {
		OK           bool      `json:"ok"`
		FetchedAt    time.Time `json:"fetched_at"`
		FetchedAtAgo int       `json:"fetched_at_ago"`
		Error        string    `json:"error,omitempty"`
	}
	type health struct {
		Status          string               `json:"status"`
		Version         string               `json:"version,omitempty"`
		StartedAt       time.Time            `json:"started_at"`
		StartedAgo      int                  `json:"started_ago"`
		MQTTConnected   bool                 `json:"mqtt_connected"`
		InfluxEnabled   bool                 `json:"influx_enabled"`
		InfluxReachable bool                 `json:"influx_reachable,omitempty"`
		Goroutines      int                  `json:"goroutines"`
		RemoteConfig    map[string]*nsStatus `json:"remote_config,omitempty"`
	}
	h := health{
		Status:        "ok",
		Version:       s.Version,
		StartedAt:     s.started,
		StartedAgo:    int((time.Since(s.started) + 500*time.Millisecond) / time.Second),
		MQTTConnected: s.MQTT != nil && s.MQTT.IsConnected(),
		InfluxEnabled: s.Influx != nil && s.Influx.Enabled,
		Goroutines:    runtime.NumGoroutine(),
	}
	if h.InfluxEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		h.InfluxReachable = s.Influx.Ping(ctx)
	}
	if s.RemoteConfig != nil {
		now := time.Now()
		statuses := s.RemoteConfig.Statuses()
		if len(statuses) > 0 {
			h.RemoteConfig = make(map[string]*nsStatus, len(statuses))
			for ns, st := range statuses {
				h.RemoteConfig[ns] = &nsStatus{
					OK:           st.OK,
					FetchedAt:    st.FetchedAt,
					FetchedAtAgo: int((now.Sub(st.FetchedAt) + 500*time.Millisecond) / time.Second),
					Error:        st.Error,
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	writeJSON(w, http.StatusOK, buildSnapshot(s.Store.Snapshot(), s.Store.ActiveSignals(now), s.Store.RecentActivity(state.ActivityLogSize), now, s.stalenessFor, s.started))
}

func (s *Server) handleHouse(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildHouseResponse(s.Store.House(), time.Now()))
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	devices := s.Store.Devices()
	out := make(map[string]DeviceResponse, len(devices))
	for id, d := range devices {
		out[id] = buildDeviceResponse(d, now, s.stalenessFor(d.Class), true)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleActivity(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	writeJSON(w, http.StatusOK, buildActivityStateResponse(s.Store.ActiveSignals(now), s.Store.RecentActivity(state.ActivityLogSize), now))
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
	writeJSON(w, http.StatusOK, buildDeviceResponse(d, time.Now(), s.stalenessFor(d.Class), true))
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
	type jwksMetrics struct {
		Fetches     uint64 `json:"jwks_fetches_total"`
		FetchErrors uint64 `json:"jwks_fetch_errors_total"`
		KidMisses   uint64 `json:"jwks_kid_misses_total"`
		Rotations   uint64 `json:"jwks_rotations_total"`
		StaleServed uint64 `json:"jwks_stale_served_total"`
		KeyCount    int    `json:"jwks_key_count"`
	}
	type metrics struct {
		StartedAgo       int          `json:"started_ago"`
		DeviceCount      int          `json:"device_count"`
		CanonicalEvents  uint64       `json:"canonical_events_total"`
		DerivedEvents    uint64       `json:"derived_events_total"`
		InfluxQueued     uint64       `json:"influx_writes_queued,omitempty"`
		InfluxFailure    uint64       `json:"influx_writes_failure,omitempty"`
		PublisherDropped uint64       `json:"mqtt_publishes_dropped_total"`
		MQTTReconnects   uint64       `json:"mqtt_reconnects_total"`
		HeapAllocBytes   uint64       `json:"heap_alloc_bytes"`
		HeapSysBytes     uint64       `json:"heap_sys_bytes"`
		GCCycles         uint32       `json:"gc_cycles_total"`
		LastGCPauseMS    float64      `json:"last_gc_pause_ms"`
		RecentLogEvents  int          `json:"recent_log_events"`
		RecentLogBytes   int64        `json:"recent_log_size_bytes"`
		JWKS             *jwksMetrics `json:"jwks,omitempty"`
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m := metrics{
		StartedAgo:      int((time.Since(s.started) + 500*time.Millisecond) / time.Second),
		DeviceCount:     len(s.Store.Devices()),
		CanonicalEvents: atomic.LoadUint64(&s.canonicalCount),
		DerivedEvents:   atomic.LoadUint64(&s.derivedCount),
		HeapAllocBytes:  ms.HeapAlloc,
		HeapSysBytes:    ms.HeapSys,
		GCCycles:        ms.NumGC,
		LastGCPauseMS:   float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6,
	}
	if s.Influx != nil && s.Influx.Enabled {
		m.InfluxQueued, m.InfluxFailure = s.Influx.Stats()
	}
	if s.Publisher != nil {
		m.PublisherDropped = s.Publisher.Dropped()
	}
	if s.MQTT != nil {
		m.MQTTReconnects = s.MQTT.Reconnects()
	}
	if s.Log != nil {
		m.RecentLogEvents, m.RecentLogBytes = s.Log.Stats()
	}
	if s.verifier != nil {
		vm := s.verifier.Metrics()
		m.JWKS = &jwksMetrics{
			Fetches:     vm.Fetches,
			FetchErrors: vm.FetchErrors,
			KidMisses:   vm.KidMisses,
			Rotations:   vm.Rotations,
			StaleServed: vm.StaleServed,
			KeyCount:    vm.KeyCount,
		}
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleConfigDevices(w http.ResponseWriter, _ *http.Request) {
	profiles := s.Store.Profiles()
	out := make(map[string]DeviceProfileResponse, len(profiles))
	for id, p := range profiles {
		out[id] = buildDeviceProfileResponse(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleConfigDevice(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/config/devices/"):]
	if id == "" {
		s.handleConfigDevices(w, r)
		return
	}
	profiles := s.Store.Profiles()
	p, ok := profiles[id]
	if !ok {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, buildDeviceProfileResponse(p))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
