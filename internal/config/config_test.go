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

// writeTempFile writes raw bytes to a temp file with the given name suffix
// and returns the path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// TestAdapterIsEnabled_DefaultMatrix verifies that zigbee2mqtt defaults to
// enabled and every other adapter defaults to disabled when no explicit
// enabled flag is set.
func TestAdapterIsEnabled_DefaultMatrix(t *testing.T) {
	cfg := Default()
	adapters := cfg.Adapters

	if !adapters.Zigbee2MQTT.IsEnabled() {
		t.Error("Zigbee2MQTT.IsEnabled() should default to true")
	}
	if adapters.Boiler.IsEnabled() {
		t.Error("Boiler.IsEnabled() should default to false")
	}
	if adapters.UPS.IsEnabled() {
		t.Error("UPS.IsEnabled() should default to false")
	}
	if adapters.Climate.IsEnabled() {
		t.Error("Climate.IsEnabled() should default to false")
	}
	if adapters.Meter.IsEnabled() {
		t.Error("Meter.IsEnabled() should default to false")
	}
}

// TestLoadFillsAdapterBaseTopicDefaults verifies that Load sets each adapter's
// BaseTopic to its documented default when the YAML does not specify one.
func TestLoadFillsAdapterBaseTopicDefaults(t *testing.T) {
	// Minimal YAML — just a broker so Load succeeds.
	yaml := `
mqtt:
  broker: "tcp://localhost:1883"
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	type want struct {
		name string
		got  string
		exp  string
	}
	checks := []want{
		{"Zigbee2MQTT", cfg.Adapters.Zigbee2MQTT.BaseTopic, "zigbee2mqtt"},
		{"Boiler", cfg.Adapters.Boiler.BaseTopic, "energy/boiler/sensor"},
		{"UPS", cfg.Adapters.UPS.BaseTopic, "ups"},
		{"Climate", cfg.Adapters.Climate.BaseTopic, "climate"},
		{"Meter", cfg.Adapters.Meter.BaseTopic, "energy"},
	}
	for _, c := range checks {
		if c.got != c.exp {
			t.Errorf("%s.BaseTopic: expected %q, got %q", c.name, c.exp, c.got)
		}
	}
}

// TestLoadInfluxTokenFile_TrimsTrailingWhitespace verifies that Load reads a
// token_file, strips trailing newlines, and exposes the clean token.
func TestLoadInfluxTokenFile_TrimsTrailingWhitespace(t *testing.T) {
	tokenPath := writeTempFile(t, "influx.token", "abc123\n")

	yaml := `
influx:
  enabled: true
  url: "http://influx.example.com:8086"
  org: myorg
  bucket: mybucket
  token_file: "` + tokenPath + `"
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Influx.Token != "abc123" {
		t.Errorf("expected Token=%q after trimming newline, got %q", "abc123", cfg.Influx.Token)
	}
}

// TestLoadInlineTokenBeatsTokenFile verifies that when both token and
// token_file are set in YAML, the inline token takes precedence.
func TestLoadInlineTokenBeatsTokenFile(t *testing.T) {
	tokenPath := writeTempFile(t, "influx.token", "from_file\n")

	yaml := `
influx:
  enabled: true
  url: "http://influx.example.com:8086"
  org: myorg
  bucket: mybucket
  token: "inline_token"
  token_file: "` + tokenPath + `"
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Influx.Token != "inline_token" {
		t.Errorf("expected inline token to win, got %q", cfg.Influx.Token)
	}
}

// TestHouseConfig_Location_EmptyReturnsUTC pins the empty-string branch.
func TestHouseConfig_Location_EmptyReturnsUTC(t *testing.T) {
	if loc := (HouseConfig{}).Location(); loc != time.UTC {
		t.Errorf("empty Timezone must return UTC, got %v", loc)
	}
}

// TestHouseConfig_Location_ValidReturnsLoaded pins the happy path.
func TestHouseConfig_Location_ValidReturnsLoaded(t *testing.T) {
	cfg := HouseConfig{Timezone: "Europe/London"}
	loc := cfg.Location()
	if loc == nil || loc.String() != "Europe/London" {
		t.Errorf("valid timezone must resolve, got %v", loc)
	}
}

// TestHouseConfig_Location_InvalidFallsBackToUTC pins the hand-crafted-config
// fallback (production configs go through Load() which rejects bad tz).
func TestHouseConfig_Location_InvalidFallsBackToUTC(t *testing.T) {
	cfg := HouseConfig{Timezone: "Not/A/Real/Zone"}
	if loc := cfg.Location(); loc != time.UTC {
		t.Errorf("invalid timezone must fall back to UTC, got %v", loc)
	}
}

// TestLoadHouseTimezoneInvalidReturnsError verifies that Load() rejects a
// typo'd timezone with a clear error rather than silently degrading to UTC
// at first Tick — the operator sees the diagnostic at startup.
func TestLoadHouseTimezoneInvalidReturnsError(t *testing.T) {
	yaml := `
house:
  timezone: "Europe/Lonon"
`
	path := writeTempYAML(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid house.timezone, got nil")
	}
	if !contains(err.Error(), "Europe/Lonon") {
		t.Errorf("expected error to name the bad timezone, got %q", err.Error())
	}
}

// TestLoadHouseTimezoneValidIsAccepted verifies that a valid timezone passes
// validation and is preserved on the loaded config.
func TestLoadHouseTimezoneValidIsAccepted(t *testing.T) {
	yaml := `
house:
  timezone: "America/Los_Angeles"
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for valid timezone: %v", err)
	}
	if cfg.House.Timezone != "America/Los_Angeles" {
		t.Errorf("expected timezone preserved, got %q", cfg.House.Timezone)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestLoadExplicitSchemeBeatsLegacyShorthand verifies that when a device has
// both scheme/primary set AND legacy ieee_address/ieee fields, the explicit
// scheme and primary fields are preserved.
func TestLoadExplicitSchemeBeatsLegacyShorthand(t *testing.T) {
	yaml := `
devices:
  my_device:
    scheme: tasmota
    primary: "explicit_primary"
    ieee_address: "0xlegacy"
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	dev, ok := cfg.Devices["my_device"]
	if !ok {
		t.Fatal("expected device 'my_device'")
	}
	if dev.Scheme != "tasmota" {
		t.Errorf("expected scheme=tasmota, got %q", dev.Scheme)
	}
	if dev.Primary != "explicit_primary" {
		t.Errorf("expected primary=explicit_primary, got %q", dev.Primary)
	}
}
