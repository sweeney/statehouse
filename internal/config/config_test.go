package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultNonNil checks that Default() returns a usable config with
// sensible values so that callers that never call Load() still work.
func TestDefaultNonNil(t *testing.T) {
	cfg := Default()

	if cfg.Availability.OfflineDebounce <= 0 {
		t.Errorf("expected OfflineDebounce > 0, got %v", cfg.Availability.OfflineDebounce)
	}
	if cfg.MQTT.Broker == "" {
		t.Error("expected non-empty default MQTT broker")
	}
	if cfg.MQTT.PublishPrefix == "" {
		t.Error("expected non-empty default publish prefix")
	}
	if cfg.HTTP.Listen == "" {
		t.Error("expected non-empty default HTTP listen address")
	}
	if cfg.Energy.MaxIntegrationGap <= 0 {
		t.Errorf("expected MaxIntegrationGap > 0, got %v", cfg.Energy.MaxIntegrationGap)
	}
	if cfg.House.QuietAfter <= 0 {
		t.Errorf("expected QuietAfter > 0, got %v", cfg.House.QuietAfter)
	}
	if cfg.House.EmptyAfter <= 0 {
		t.Errorf("expected EmptyAfter > 0, got %v", cfg.House.EmptyAfter)
	}
}

// TestLoadBasicYAML verifies that Load reads a YAML file, merges it on
// top of defaults, and exposes the values correctly.
func TestLoadBasicYAML(t *testing.T) {
	yaml := `
mqtt:
  broker: "tcp://mqtt.example.com:1883"
  client_id: "test-client"
device_classes:
  kettle:
    energy_strategy: integration
    default_thresholds:
      idle_below_w: 3.0
      active_above_w: 100.0
devices:
  my_kettle:
    scheme: zigbee
    primary: "0xaabbccdd"
    display: "Kitchen Kettle"
    class: kettle
    location: kitchen
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Broker URL
	if cfg.MQTT.Broker != "tcp://mqtt.example.com:1883" {
		t.Errorf("unexpected broker %q", cfg.MQTT.Broker)
	}
	if cfg.MQTT.ClientID != "test-client" {
		t.Errorf("unexpected client_id %q", cfg.MQTT.ClientID)
	}

	// Device class
	dc, ok := cfg.DeviceClasses["kettle"]
	if !ok {
		t.Fatal("expected device class 'kettle'")
	}
	if dc.EnergyStrategy != "integration" {
		t.Errorf("unexpected energy_strategy %q", dc.EnergyStrategy)
	}
	if dc.DefaultThresholds.IdleBelowW == nil || *dc.DefaultThresholds.IdleBelowW != 3.0 {
		t.Errorf("unexpected idle_below_w %v", dc.DefaultThresholds.IdleBelowW)
	}

	// Device entry
	dev, ok := cfg.Devices["my_kettle"]
	if !ok {
		t.Fatal("expected device 'my_kettle'")
	}
	if dev.Scheme != "zigbee" {
		t.Errorf("unexpected scheme %q", dev.Scheme)
	}
	if dev.Primary != "0xaabbccdd" {
		t.Errorf("unexpected primary %q", dev.Primary)
	}
	if dev.Display != "Kitchen Kettle" {
		t.Errorf("unexpected display %q", dev.Display)
	}
	if dev.Location != "kitchen" {
		t.Errorf("unexpected location %q", dev.Location)
	}

	// Defaults still populated
	if cfg.Availability.OfflineDebounce != 30*time.Second {
		t.Errorf("expected default OfflineDebounce=30s, got %v", cfg.Availability.OfflineDebounce)
	}
}

// TestLoadLegacyZigbeeShorthand verifies that the old ieee_address /
// friendly_name fields are normalised to scheme/primary/display by Load.
func TestLoadLegacyZigbeeShorthand(t *testing.T) {
	yaml := `
devices:
  toaster:
    ieee_address: "0x1234567890abcdef"
    friendly_name: "Kitchen Toaster"
    class: short_burst_power_device
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	dev, ok := cfg.Devices["toaster"]
	if !ok {
		t.Fatal("expected device 'toaster'")
	}
	if dev.Scheme != "zigbee" {
		t.Errorf("expected scheme=zigbee after normalisation, got %q", dev.Scheme)
	}
	if dev.Primary != "0x1234567890abcdef" {
		t.Errorf("expected primary=ieee_address after normalisation, got %q", dev.Primary)
	}
	if dev.Display != "Kitchen Toaster" {
		t.Errorf("expected display=friendly_name after normalisation, got %q", dev.Display)
	}
}

// TestLoadMissingTokenFileReturnsError ensures that specifying a
// token_file that doesn't exist causes Load to return an error rather
// than silently leaving the token blank.
func TestLoadMissingTokenFileReturnsError(t *testing.T) {
	yaml := `
influx:
  enabled: true
  url: "http://influx.example.com:8086"
  org: myorg
  bucket: mybucket
  token_file: "/nonexistent/path/to/token"
`
	path := writeTempYAML(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing token_file, got nil")
	}
}

// TestLoadMissingFile returns an error when the config path doesn't exist.
func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file, got nil")
	}
}

// writeTempYAML writes content to a temp file and returns the path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}
	return path
}
