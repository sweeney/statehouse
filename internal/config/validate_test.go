package config

import (
	"strings"
	"testing"
)

// The site tag exists to keep device ids unambiguous once a second property
// reports into the same bucket. An unset site writes untagged points that
// silently look exactly like the ambiguity the tag prevents, and no amount of
// later work can recover which property that history came from.
//
// So an unset site is a startup error, not a warning. Validate is separate
// from Load because Load parses and normalises — tools and tests load configs
// they never intend to run — while Validate is the gate a running service
// passes through.

func TestValidateRejectsUnsetSite(t *testing.T) {
	cfg := Default()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for a config with no site; want an error")
	}
	if !strings.Contains(err.Error(), "site") {
		t.Errorf("error %q does not mention the site key, so it cannot be acted on", err)
	}
}

func TestValidateAcceptsConfiguredSite(t *testing.T) {
	cfg := Default()
	cfg.Site = "home"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for a configured site", err)
	}
}

// Load stays permissive: it parses whatever it is given, so callers that only
// want to inspect a config are unaffected by the startup policy.
func TestLoadDoesNotEnforceSite(t *testing.T) {
	path := writeTempYAML(t, "mqtt:\n  broker: tcp://localhost:1883\n")
	if _, err := Load(path); err != nil {
		t.Errorf("Load() = %v, want nil; site enforcement belongs to Validate", err)
	}
}
