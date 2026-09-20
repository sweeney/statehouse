package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The devices namespace is now named by config rather than hardcoded, so a site can
// have its own. This is the change that makes the per-site split reachable at all —
// publishing devices_home did nothing while every service read a fixed name.
func TestApplyRemoteFetchesTheConfiguredDevicesNamespace(t *testing.T) {
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Path[len("/api/v1/config/"):]
		asked = append(asked, ns)
		if ns == "devices_home" {
			_, _ = w.Write([]byte(`{"bigfridge":{"class":"continuous_power_device","room":"groundfloor.kitchen"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{BaseURL: srv.URL, Tokens: &staticTokenSource{token: "t"}, HTTPClient: srv.Client()}
	cfg := Default()
	cfg.Site = SiteConfig{ID: "home", DevicesNamespace: "devices_home"}
	f.ApplyRemote(context.Background(), &cfg)

	var sawConfigured, sawHardcoded bool
	for _, ns := range asked {
		switch ns {
		case "devices_home":
			sawConfigured = true
		case "statehouse_devices":
			sawHardcoded = true
		}
	}
	if !sawConfigured {
		t.Errorf("never fetched the configured namespace; asked for %v", asked)
	}
	if sawHardcoded {
		t.Errorf("still fetching the hardcoded namespace; asked for %v", asked)
	}
	if got := cfg.Devices["bigfridge"].Room; got != "groundfloor.kitchen" {
		t.Errorf("device room = %q, want groundfloor.kitchen", got)
	}
}

// The remote-config status block on /healthz is keyed by namespace name, so the
// devices entry follows config: `statehouse_devices` today, `devices_home` once the
// site names one. That is an observable rename on a monitored endpoint.
//
// Keyed by name rather than by a fixed label deliberately: the sibling entries are
// namespace names too, and a health block that reported a namespace it was not
// actually reading would be worse than one whose key moves when the namespace does.
func TestHealthStatusIsKeyedByTheNamespaceActuallyRead(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, tc := range []struct{ configured, wantKey string }{
		{"devices_home", "devices_home"},
		{"devices_annexe", "devices_annexe"},
	} {
		f := &Fetcher{BaseURL: srv.URL, Tokens: &staticTokenSource{token: "t"}, HTTPClient: srv.Client()}
		cfg := Default()
		cfg.Site = SiteConfig{ID: "home", DevicesNamespace: tc.configured}
		f.ApplyRemote(context.Background(), &cfg)

		if _, ok := f.Statuses()[tc.wantKey]; !ok {
			t.Errorf("configured %q: /healthz has no %q entry; keys were %v",
				tc.configured, tc.wantKey, keysOf(f.Statuses()))
		}
	}
}

func keysOf(m map[string]NamespaceStatus) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Every record in the live devices namespace declares `floor` alongside `room`, and the
// document carries keys statehouse has no use for (`coordinate`, `environment_fields`,
// `note`). The fetch is a plain json.Unmarshal, so an unmapped field is dropped in
// silence — which is exactly how `floor` went missing from every statehouse response
// while countinghouse and greenhouse both relayed it.
func TestDevicesNamespaceCarriesFloor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path[len("/api/v1/config/"):] != "devices_home" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		// Shaped like the real document, unused keys included.
		_, _ = w.Write([]byte(`{
			"basement-dishwasher": {
				"class": "cycle_power_device",
				"display_name": "Dishwasher (Basement)",
				"room": "basement.kitchen",
				"floor": "basement",
				"coordinate": [8.241, 3.211],
				"ieee_address": "0x20a716fffed182ed"
			},
			"electricity_meter": {
				"class": "energy_meter",
				"room": "basement.hallway",
				"floor": "basement",
				"covers": "house",
				"scheme": "meter",
				"primary": "7C9EBDF6E56C"
			}
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{BaseURL: srv.URL, Tokens: &staticTokenSource{token: "t"}, HTTPClient: srv.Client()}
	cfg := Default()
	cfg.Site = SiteConfig{ID: "home", DevicesNamespace: "devices_home"}
	f.ApplyRemote(context.Background(), &cfg)

	if got := cfg.Devices["basement-dishwasher"].Floor; got != "basement" {
		t.Errorf("dishwasher floor = %q, want basement", got)
	}
	if got := cfg.Devices["electricity_meter"].Floor; got != "basement" {
		t.Errorf("meter floor = %q, want basement", got)
	}
}

// A device the namespace gives no `floor` has an UNKNOWN floor. Deriving one by
// splitting the room id on its first dot would be a second implementation of the
// floorplan's taxonomy, living here and disagreeing the moment a room id is spelled
// unexpectedly. Both sibling services refuse the same derivation for the same reason.
func TestUndeclaredFloorStaysEmptyRatherThanBeingDerivedFromTheRoom(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path[len("/api/v1/config/"):] == "devices_home" {
			_, _ = w.Write([]byte(`{"bigfridge":{"class":"continuous_power_device","room":"groundfloor.kitchen"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{BaseURL: srv.URL, Tokens: &staticTokenSource{token: "t"}, HTTPClient: srv.Client()}
	cfg := Default()
	cfg.Site = SiteConfig{ID: "home", DevicesNamespace: "devices_home"}
	f.ApplyRemote(context.Background(), &cfg)

	if got := cfg.Devices["bigfridge"].Floor; got != "" {
		t.Errorf("floor = %q, want empty: the namespace declared none", got)
	}
}
