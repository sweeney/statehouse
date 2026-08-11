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
