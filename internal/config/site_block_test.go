package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The site block groups the facts about which property this instance serves, and
// which namespace holds its devices — so adding a second site is a config edit
// rather than a code change.
func TestSiteBlockCarriesIDAndDevicesNamespace(t *testing.T) {
	cfg, err := Load(writeConfig(t, "site:\n  id: home\n  devices_namespace: devices_home\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Site.ID != "home" {
		t.Errorf("Site.ID = %q, want home", cfg.Site.ID)
	}
	if cfg.Site.DevicesNamespace != "devices_home" {
		t.Errorf("Site.DevicesNamespace = %q, want devices_home", cfg.Site.DevicesNamespace)
	}
}

// The deployed config says `site: home`, and deploy.sh ships only the binary — so a
// binary that could not parse the scalar form would take the service down the moment
// it shipped, before anyone edited the host's config. The old spelling keeps working.
func TestLegacyScalarSiteStillParses(t *testing.T) {
	cfg, err := Load(writeConfig(t, "site: home\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Site.ID != "home" {
		t.Errorf("Site.ID = %q, want home", cfg.Site.ID)
	}
	if cfg.Site.DevicesNamespace != "statehouse_devices" {
		t.Errorf("DevicesNamespace = %q, want the pre-migration default", cfg.Site.DevicesNamespace)
	}
}

// A site block with no namespace keeps reading the namespace it always read.
func TestDevicesNamespaceDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "site:\n  id: home\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Site.DevicesNamespace != "statehouse_devices" {
		t.Errorf("DevicesNamespace = %q, want statehouse_devices", cfg.Site.DevicesNamespace)
	}
}

// Still refuses to start without a site, in either spelling.
func TestValidateStillRequiresASite(t *testing.T) {
	cfg, err := Load(writeConfig(t, "http:\n  listen: :8080\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() accepted a config with no site")
	}
}
