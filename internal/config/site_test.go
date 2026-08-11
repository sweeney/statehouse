package config

import (
	"os"
	"path/filepath"
	"testing"
)

// statehouse must know its own site id. Today it does not: it fetches a fixed
// namespace name. The site supplies the Influx `site` tag value and, once devices are
// split per site, selects which devices namespace to fetch.
func TestSiteIsReadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("site: home\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Site.ID != "home" {
		t.Errorf("Site = %q, want %q", cfg.Site, "home")
	}
}

// There is no default site. Guessing one would write a tag asserting which property
// the readings came from, which is exactly the kind of fact that must be declared.
func TestSiteHasNoDefault(t *testing.T) {
	if got := Default().Site.ID; got != "" {
		t.Errorf("Default().Site.ID = %q, want empty", got)
	}
}
