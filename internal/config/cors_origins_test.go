package config

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sweeney/statehouse/internal/origin"
)

// loadWithHTTP loads a config carrying the given http block, plus the site
// block every config needs, so each test body can be about allowed_origins
// alone.
func loadWithHTTP(t *testing.T, httpBlock string) Config {
	t.Helper()
	body := "site:\n  id: home\n  devices_namespace: devices_home\n" + httpBlock
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// validConfig is a Config that passes Validate, for tests that then break one
// thing about it.
func validConfig() Config {
	c := Default()
	c.Site = SiteConfig{ID: "home", DevicesNamespace: "devices_home"}
	return c
}

// The allowlist is read from the local YAML file.
func TestLoadParsesAllowedOrigins(t *testing.T) {
	cfg := loadWithHTTP(t, `
http:
  listen: :8383
  allowed_origins:
    - "https://*.swee.net"
    - "http://localhost:*"
`)
	want := []string{"https://*.swee.net", "http://localhost:*"}
	if len(cfg.HTTP.AllowedOrigins) != len(want) {
		t.Fatalf("AllowedOrigins = %v, want %v", cfg.HTTP.AllowedOrigins, want)
	}
	for i, w := range want {
		if cfg.HTTP.AllowedOrigins[i] != w {
			t.Errorf("AllowedOrigins[%d] = %q, want %q", i, cfg.HTTP.AllowedOrigins[i], w)
		}
	}
}

// A config that says nothing about origins gets none. CORS is off until asked
// for, so deploying this version against an unedited config changes nothing.
func TestAllowedOriginsDefaultsToEmpty(t *testing.T) {
	cfg := loadWithHTTP(t, "http:\n  listen: :8383\n")
	if len(cfg.HTTP.AllowedOrigins) != 0 {
		t.Errorf("AllowedOrigins = %v, want empty", cfg.HTTP.AllowedOrigins)
	}
	if len(Default().HTTP.AllowedOrigins) != 0 {
		t.Errorf("Default().HTTP.AllowedOrigins = %v, want empty", Default().HTTP.AllowedOrigins)
	}
}

// A typo in an origin is a typo in a security control, and it fails silently in
// both directions: the operator believes an origin is allowlisted when it is
// not, or believes one is excluded when the entry that was meant to exclude it
// never parsed. Refusing to start is the same trade this service already makes
// for an unset site and an unnamed devices namespace.
func TestValidateRejectsAMalformedOrigin(t *testing.T) {
	for _, bad := range []string{
		"app.swee.net",          // no scheme
		"*.swee.net",            // no scheme, wildcard form
		"https://app.swee.net/", // a URL, not an origin
		"https://*",             // allow-everything, ambiguously spelled
		"https://*.net",         // a wildcard needs a parent of two or more labels
		"ftp://app.swee.net",    // not a browser origin
	} {
		cfg := validConfig()
		cfg.HTTP.AllowedOrigins = []string{bad}
		err := cfg.Validate()
		if err == nil {
			t.Errorf("Validate() = nil for allowed_origins entry %q, want a rejection", bad)
			continue
		}
		if !strings.Contains(err.Error(), "allowed_origins") {
			t.Errorf("error for %q does not mention allowed_origins: %v", bad, err)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Errorf("error for %q does not name the offending entry: %v", bad, err)
		}
	}
}

// Every form the README and the example config offer must survive Validate.
func TestValidateAcceptsDocumentedOrigins(t *testing.T) {
	cfg := validConfig()
	cfg.HTTP.AllowedOrigins = []string{
		"https://*.swee.net",
		"http://localhost:*",
		"http://127.0.0.1:*",
		"https://app.swee.net",
		"*",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// No origins is a valid config, not an omission to complain about.
func TestValidateAcceptsNoOrigins(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// The shipped example config is where an operator discovers the option exists,
// so a typo there is a typo everybody copies.
//
// The entries are commented out deliberately — "http://localhost:*" is
// meaningful on every deployment and would otherwise be inherited by anyone
// starting from this file — which means the usual "load it and validate" check
// would pass vacuously. So both halves are asserted separately: that the file
// documents the option at all, and that the forms it offers actually compile.
func TestExampleConfigDocumentsAllowedOrigins(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "allowed_origins") {
		t.Error("the example config never mentions allowed_origins: the option is undiscoverable")
	}

	// The commented entries are the ones an operator uncomments, so they are
	// the ones that have to be valid. Pull them out of the commented
	// allowed_origins block — and only that block — and put them through the
	// real compiler.
	var offered []string
	inBlock := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			inBlock = false
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if strings.HasPrefix(line, "allowed_origins:") {
			inBlock = true
			continue
		}
		rest, isEntry := strings.CutPrefix(line, "- ")
		if !inBlock || !isEntry {
			continue
		}
		// Drop any trailing "# development"-style comment, then the quotes.
		if i := strings.Index(rest, " #"); i >= 0 {
			rest = rest[:i]
		}
		offered = append(offered, strings.Trim(strings.TrimSpace(rest), `"`))
	}
	if len(offered) == 0 {
		t.Fatal("found no commented allowed_origins entries in the example config")
	}
	if _, err := origin.Compile(offered); err != nil {
		t.Errorf("the example config offers entries that do not compile: %v (entries: %v)",
			err, offered)
	}
}

// And the example must still be a config the service will start with, which
// with the allowlist commented out means an empty one.
func TestExampleConfigValidates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse example config: %v", err)
	}
	if len(cfg.HTTP.AllowedOrigins) != 0 {
		t.Errorf("the example config ships a live allowlist (%v): CORS is meant to be opt-in, "+
			"and http://localhost:* would be inherited by anyone copying the file",
			cfg.HTTP.AllowedOrigins)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the example config does not validate: %v", err)
	}
}

// The allowlist is deliberately local-only. Remote config tunes behaviour, but
// widening which browser origins may read the API from a namespace this service
// merely fetches would put a security control on the far side of a network
// call — and a namespace that fails to fetch is skipped non-fatally, so a
// remote *tightening* would be silently ignored too. Both directions argue for
// keeping it in the file the operator edits.
func TestAllowedOriginsIsNotRemotelyOverridable(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"http": map[string]any{"allowed_origins": []string{"https://evil.example.com"}},
	})
	f := newTestFetcher(t, mux)

	cfg := validConfig()
	cfg.HTTP.AllowedOrigins = []string{"https://app.swee.net"}
	f.ApplyRemote(context.Background(), &cfg)

	if len(cfg.HTTP.AllowedOrigins) != 1 || cfg.HTTP.AllowedOrigins[0] != "https://app.swee.net" {
		t.Errorf("AllowedOrigins = %v, want the local value untouched by remote config",
			cfg.HTTP.AllowedOrigins)
	}
}
