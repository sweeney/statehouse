package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sweeney/common/devices"
)

// The example config is what people copy to write their own, so its devices
// must be in the vocabulary statehouse now teaches: room, floor and covers.
// devices.Check flags the deprecated location field, a room that is not a
// "<floor>.<room>" id, a floor that disagrees with its room, and a covers
// that names neither "house" nor a room.
func TestExampleConfigDevicesUseFloorplanVocabulary(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse example config: %v", err)
	}
	if len(cfg.Devices) == 0 {
		t.Fatal("the example config lists no devices; this test would pass vacuously")
	}
	for _, p := range devices.Check(cfg.Devices) {
		t.Errorf("example config: %s", p)
	}
}
