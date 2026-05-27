package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// staticTokenSource satisfies TokenSource for tests.
type staticTokenSource struct{ token string }

func (s *staticTokenSource) Token(_ context.Context) (string, error) { return s.token, nil }

// errorTokenSource always returns an error.
type errorTokenSource struct{}

func (e *errorTokenSource) Token(_ context.Context) (string, error) {
	return "", fmt.Errorf("token unavailable")
}

func newTestFetcher(t *testing.T, mux *http.ServeMux) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Fetcher{
		BaseURL:    srv.URL,
		Tokens:     &staticTokenSource{token: "test-token"},
		HTTPClient: srv.Client(),
	}
}

func serveNamespace(mux *http.ServeMux, ns string, v any) {
	mux.HandleFunc("/api/v1/config/"+ns, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	})
}

// --- classes ---

func TestFetcher_ApplyClasses(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{
		"media_power_device": map[string]any{
			"name_hints": []string{"tv", "av"},
			"default_thresholds": map[string]any{
				"idle_below_w":           10,
				"active_above_w":         30,
				"active_sustained_for":   "5s",
				"inactive_sustained_for": "2m",
			},
			"energy_strategy": "integration",
		},
	})
	serveNamespace(mux, "statehouse_devices", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})

	cfg := Default()
	cfg.DeviceClasses = map[string]DeviceClassConfig{
		"media_power_device": {EnergyStrategy: "counter"}, // will be overwritten
		"cycle_power_device": {EnergyStrategy: "counter"}, // local-only, preserved
	}

	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	media := cfg.DeviceClasses["media_power_device"]
	if media.EnergyStrategy != "integration" {
		t.Errorf("energy_strategy: got %q, want %q", media.EnergyStrategy, "integration")
	}
	idle := *media.DefaultThresholds.IdleBelowW
	if idle != 10 {
		t.Errorf("idle_below_w: got %v, want 10", idle)
	}
	if _, ok := cfg.DeviceClasses["cycle_power_device"]; !ok {
		t.Error("local-only class cycle_power_device was removed")
	}
}

// --- devices ---

func TestFetcher_ApplyDevices(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "statehouse_devices", map[string]any{
		"washingmachine": map[string]any{
			"ieee_address": "0xaabbccddeeff0011",
			"class":        "cycle_power_device",
			"display_name": "Washing Machine",
			"location":     "utility",
		},
	})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})

	cfg := Default()
	cfg.Devices = map[string]DeviceConfig{
		"localdevice": {Class: "binary_state_device"}, // local-only, preserved
	}

	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	wm, ok := cfg.Devices["washingmachine"]
	if !ok {
		t.Fatal("washingmachine missing after apply")
	}
	if wm.Class != "cycle_power_device" {
		t.Errorf("class: got %q, want %q", wm.Class, "cycle_power_device")
	}
	// IEEE address should be normalised to scheme=zigbee, primary=address.
	if wm.Scheme != "zigbee" {
		t.Errorf("scheme: got %q, want %q", wm.Scheme, "zigbee")
	}
	if wm.Primary != "0xaabbccddeeff0011" {
		t.Errorf("primary: got %q, want %q", wm.Primary, "0xaabbccddeeff0011")
	}
	if _, ok := cfg.Devices["localdevice"]; !ok {
		t.Error("local-only device was removed")
	}
}

func TestFetcher_RemoteDeviceOverridesLocal(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "statehouse_devices", map[string]any{
		"kettle": map[string]any{
			"ieee_address": "0xremote",
			"class":        "short_burst_power_device",
			"display_name": "Remote Kettle",
			"location":     "kitchen",
		},
	})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})

	cfg := Default()
	cfg.Devices = map[string]DeviceConfig{
		"kettle": {IEEEAddress: "0xlocal", Class: "short_burst_power_device", DisplayName: "Local Kettle"},
	}

	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	if cfg.Devices["kettle"].Primary != "0xremote" {
		t.Errorf("remote did not override local device: got %q", cfg.Devices["kettle"].Primary)
	}
}

// --- behaviour ---

func TestFetcher_ApplyBehaviour(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "statehouse_devices", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"energy": map[string]any{
			"divergence_warning_pct": 15,
			"max_integration_gap":    "45m",
		},
		"availability": map[string]any{
			"offline_debounce": "60s",
		},
		"house": map[string]any{
			"quiet_after": "20m",
			"empty_after": "4h",
		},
	})

	cfg := Default()
	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	if cfg.Energy.DivergenceWarningPct != 15 {
		t.Errorf("divergence_warning_pct: got %v, want 15", cfg.Energy.DivergenceWarningPct)
	}
	if cfg.Energy.MaxIntegrationGap != 45*time.Minute {
		t.Errorf("max_integration_gap: got %v, want 45m", cfg.Energy.MaxIntegrationGap)
	}
	if cfg.Availability.OfflineDebounce != 60*time.Second {
		t.Errorf("offline_debounce: got %v, want 60s", cfg.Availability.OfflineDebounce)
	}
	if cfg.House.QuietAfter != 20*time.Minute {
		t.Errorf("quiet_after: got %v, want 20m", cfg.House.QuietAfter)
	}
}

func TestFetcher_BehaviourAbsentSectionsPreserveLocal(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "statehouse_devices", map[string]any{})
	// behaviour has only energy; availability/house/adapters absent.
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"energy": map[string]any{"divergence_warning_pct": 25},
	})

	cfg := Default()
	localDebounce := 45 * time.Second
	cfg.Availability.OfflineDebounce = localDebounce

	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	if cfg.Availability.OfflineDebounce != localDebounce {
		t.Errorf("availability was overwritten; got %v, want %v", cfg.Availability.OfflineDebounce, localDebounce)
	}
}

// --- failure / fail-open ---

func TestFetcher_TokenFailurePreservesLocalConfig(t *testing.T) {
	cfg := Default()
	cfg.Devices = map[string]DeviceConfig{"local": {Class: "binary_state_device"}}

	f := &Fetcher{
		BaseURL: "http://127.0.0.1:1",
		Tokens:  &errorTokenSource{},
	}
	f.ApplyRemote(context.Background(), &cfg)

	if _, ok := cfg.Devices["local"]; !ok {
		t.Error("local device was removed after token failure")
	}
}

func TestFetcher_NamespaceUnavailablePreservesOtherSections(t *testing.T) {
	mux := http.NewServeMux()
	// classes returns 503; devices and behaviour are healthy.
	mux.HandleFunc("/api/v1/config/statehouse_classes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	serveNamespace(mux, "statehouse_devices", map[string]any{
		"remotedevice": map[string]any{"class": "binary_state_device"},
	})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"energy": map[string]any{"divergence_warning_pct": 30},
	})

	cfg := Default()
	cfg.DeviceClasses = map[string]DeviceClassConfig{
		"cycle_power_device": {EnergyStrategy: "counter"},
	}

	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	// local classes preserved
	if _, ok := cfg.DeviceClasses["cycle_power_device"]; !ok {
		t.Error("local classes removed after namespace failure")
	}
	// remote devices still applied
	if _, ok := cfg.Devices["remotedevice"]; !ok {
		t.Error("remote devices not applied despite classes failure")
	}
	// remote behaviour still applied
	if cfg.Energy.DivergenceWarningPct != 30 {
		t.Errorf("behaviour not applied: got %v, want 30", cfg.Energy.DivergenceWarningPct)
	}
}

func TestFetcher_BearerTokenSentInRequest(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	for _, ns := range []string{"statehouse_classes", "statehouse_devices", "statehouse_behaviour"} {
		ns := ns
		mux.HandleFunc("/api/v1/config/"+ns, func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{})
		})
	}

	cfg := Default()
	newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer test-token")
	}
}
