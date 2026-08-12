package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `statehouse_devices` was deleted from the config service once every service read
// its own per-site namespace. The constant that named it survived as the fallback for
// a config naming no namespace, so the default now points at a document that does not
// exist — and every layer of what follows is individually correct:
//
//	the fetch 404s -> applyDevices fails open and keeps the last snapshot, empty at
//	startup -> /healthz reports status "ok", since a failing namespace is only a
//	nested ok:false -> every endpoint honestly reports zero devices
//
// Nothing anywhere says "you did not name a devices namespace". The service looks
// healthy and serves nothing, which is the failure the site id check already exists
// to prevent for the other half of the same block.

func TestValidateRejectsUnnamedDevicesNamespace(t *testing.T) {
	cfg := Default()
	cfg.Site.ID = "home"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for a site naming no devices namespace; want an error")
	}
	if !strings.Contains(err.Error(), "devices_namespace") {
		t.Errorf("error %q does not name the key to add, so it cannot be acted on", err)
	}
}

func TestValidateAcceptsANamedDevicesNamespace(t *testing.T) {
	cfg := Default()
	cfg.Site = SiteConfig{ID: "home", DevicesNamespace: "devices_home"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// Load must not supply a namespace of its own. A default is a guess at which document
// holds this site's devices, and the only guess available now names a deleted one.
func TestLoadDoesNotDefaultTheDevicesNamespace(t *testing.T) {
	cfg, err := Load(writeConfig(t, "site:\n  id: home\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Site.DevicesNamespace; got != "" {
		t.Errorf("DevicesNamespace = %q, want empty: Load must not guess", got)
	}
}

// The scalar spelling still parses — it was kept so a deployed config could not be
// broken by a binary shipping ahead of a config edit — but it names no namespace, so
// it is no longer a startable config on its own. Parsing and sufficiency are separate
// questions and this pins both.
func TestLegacyScalarSiteParsesButIsNoLongerSufficient(t *testing.T) {
	cfg, err := Load(writeConfig(t, "site: home\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Site.ID != "home" {
		t.Errorf("Site.ID = %q, want home: the scalar must still parse", cfg.Site.ID)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() accepted a scalar site naming no devices namespace")
	}
}

// ApplyRemote defaulted the namespace a second time, independently of Load. Removing
// one without the other would leave the behaviour intact and every test green — the
// shape of bug this migration has hit repeatedly — so the fallback must be gone from
// here too, and an unnamed namespace must produce no request at all rather than a
// request for a deleted document.
func TestApplyRemoteRequestsNoDevicesNamespaceWhenUnnamed(t *testing.T) {
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, strings.TrimPrefix(r.URL.Path, "/api/v1/config/"))
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{BaseURL: srv.URL, Tokens: &staticTokenSource{token: "t"}, HTTPClient: srv.Client()}
	cfg := Default()
	cfg.Site = SiteConfig{ID: "home"} // no namespace named
	f.ApplyRemote(context.Background(), &cfg)

	for _, ns := range asked {
		if ns == "statehouse_devices" {
			t.Errorf("requested the deleted namespace; asked for %v", asked)
		}
		if ns == "" {
			t.Errorf("requested an empty namespace; asked for %v", asked)
		}
	}
}
