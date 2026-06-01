package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level service configuration loaded from YAML.
type Config struct {
	MQTT          MQTTConfig                   `yaml:"mqtt"`
	HTTP          HTTPConfig                   `yaml:"http"`
	RecentLog     RecentLogConfig              `yaml:"recent_log"`
	Influx        InfluxConfig                 `yaml:"influx"`
	Energy        EnergyConfig                 `yaml:"energy"`
	Availability  AvailabilityConfig           `yaml:"availability"`
	House         HouseConfig                  `yaml:"house"`
	Adapters      AdaptersConfig               `yaml:"adapters"`
	DeviceClasses map[string]DeviceClassConfig `yaml:"device_classes"`
	Devices       map[string]DeviceConfig      `yaml:"devices"`
	Identity      IdentityConfig               `yaml:"identity"`
	RemoteConfig  RemoteConfigConfig           `yaml:"remote_config"`
}

// IdentityConfig holds credentials for the identity service used to
// obtain access tokens for service-to-service calls.
type IdentityConfig struct {
	BaseURL      string `yaml:"base_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// RemoteConfigConfig holds the address of the remote config service.
type RemoteConfigConfig struct {
	BaseURL string `yaml:"base_url"`
}

// MQTTConfig describes broker connectivity. Per-adapter subscription
// topics are now owned by the adapter blocks below, not by MQTT.
type MQTTConfig struct {
	Broker   string `yaml:"broker"`
	ClientID string `yaml:"client_id"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// PublishPrefix is prepended to derived MQTT topics. Defaults to "house".
	PublishPrefix string `yaml:"publish_prefix"`
}

// AdaptersConfig groups per-protocol adapter configuration. Adapters
// translate raw MQTT messages into engine calls that don't carry
// protocol vocabulary, so each protocol gets its own settings block.
type AdaptersConfig struct {
	Zigbee2MQTT Zigbee2MQTTConfig `yaml:"zigbee2mqtt" json:"zigbee2mqtt,omitempty"`
	Boiler      BoilerConfig      `yaml:"boiler"      json:"boiler,omitempty"`
	UPS         UPSConfig         `yaml:"ups"         json:"ups,omitempty"`
	Climate     ClimateConfig     `yaml:"climate"     json:"climate,omitempty"`
	Meter       MeterConfig       `yaml:"meter"       json:"meter,omitempty"`
	Intercom    IntercomConfig    `yaml:"intercom"    json:"intercom,omitempty"`
}

// Zigbee2MQTTConfig configures the Zigbee2MQTT adapter.
type Zigbee2MQTTConfig struct {
	// Enabled defaults to true if the block is present at all. Set to
	// false to disable the adapter without removing the block.
	Enabled *bool `yaml:"enabled"    json:"enabled,omitempty"`
	// BaseTopic is the topic prefix of the Z2M bridge ("zigbee2mqtt").
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

// IsEnabled reports whether the adapter should be wired. Defaults to
// true when the Adapters block is absent — the simplest config
// (broker + nothing else) still gets a working Z2M adapter.
func (z Zigbee2MQTTConfig) IsEnabled() bool {
	if z.Enabled == nil {
		return true
	}
	return *z.Enabled
}

// BoilerConfig configures the sweeney/boiler-sensor adapter, which
// listens on energy/boiler/sensor/{events,system}. It is OFF by
// default — the boiler-sensor publisher isn't a generic protocol so
// users opt in by enabling it.
type BoilerConfig struct {
	Enabled *bool `yaml:"enabled"    json:"enabled,omitempty"`
	// BaseTopic is the topic prefix the publisher uses; defaults to
	// "energy/boiler/sensor". The adapter appends "/events" and
	// "/system" for its two subscriptions.
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

// IsEnabled reports whether the boiler adapter should be wired.
// Default is false — adapters that target a specific bespoke device
// shouldn't auto-enable.
func (b BoilerConfig) IsEnabled() bool {
	if b.Enabled == nil {
		return false
	}
	return *b.Enabled
}

// UPSConfig configures the NUT-via-MQTT UPS adapter.
type UPSConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (u UPSConfig) IsEnabled() bool {
	if u.Enabled == nil {
		return false
	}
	return *u.Enabled
}

// ClimateConfig configures the weather station adapter.
type ClimateConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (c ClimateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// MeterConfig configures the Glow/SMETS2 smart meter adapter.
type MeterConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (m MeterConfig) IsEnabled() bool {
	if m.Enabled == nil {
		return false
	}
	return *m.Enabled
}

// IntercomConfig configures the Intercom (Asterisk-via-MQTT) adapter.
// BaseTopic defaults to "asterisk".
type IntercomConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (i IntercomConfig) IsEnabled() bool {
	if i.Enabled == nil {
		return false
	}
	return *i.Enabled
}

type HTTPConfig struct {
	Listen string `yaml:"listen"`
}

type RecentLogConfig struct {
	Path           string `yaml:"path"`
	RetentionHours int    `yaml:"retention_hours"`
	MaxSizeMB      int    `yaml:"max_size_mb"`
}

type InfluxConfig struct {
	Enabled   bool   `yaml:"enabled"`
	URL       string `yaml:"url"`
	Org       string `yaml:"org"`
	Bucket    string `yaml:"bucket"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type EnergyConfig struct {
	DivergenceWarningPct float64           `yaml:"divergence_warning_pct"  json:"divergence_warning_pct"`
	MaxIntegrationGap    time.Duration     `yaml:"max_integration_gap"     json:"-"`
	Electricity          ElectricityConfig `yaml:"electricity"             json:"electricity,omitempty"`
}

// ElectricityConfig tunes the whole-house electricity aggregator. The
// idle/active split lets change-reporting plugs (Aqara-style: emit only
// when state changes) survive long quiet periods at 0W without being
// flagged stale, while devices reporting non-zero power are still
// expected to refresh frequently.
type ElectricityConfig struct {
	StalenessActive time.Duration `yaml:"staleness_active" json:"-"`
	StalenessIdle   time.Duration `yaml:"staleness_idle"   json:"-"`
	IdleBelowW      float64       `yaml:"idle_below_w"     json:"idle_below_w,omitempty"`
}

type AvailabilityConfig struct {
	OfflineDebounce time.Duration `yaml:"offline_debounce" json:"-"`
}

type HouseConfig struct {
	// QuietAfter marks the house as quiet when no activity has occurred
	// for this long.
	QuietAfter time.Duration `yaml:"quiet_after"    json:"-"`
	// EmptyAfter marks the house as empty if quiet for this long and no
	// signals of presence have been seen.
	EmptyAfter time.Duration `yaml:"empty_after"    json:"-"`
	// SleepingAfter is the sustained quiet duration beyond which the house
	// mode transitions to sleeping (when occupied).
	SleepingAfter time.Duration `yaml:"sleeping_after" json:"-"`
	// Timezone names a tz database location (e.g. "Europe/London") used to
	// classify the mode dimension's night/day hour window. Empty means UTC
	// (back-compat for existing configs); "Local" uses the host time zone.
	// Load() rejects values that time.LoadLocation cannot resolve (typo,
	// missing tzdata) with a clear error — operators see the diagnostic at
	// startup rather than discovering it via mis-bucketed mode readings.
	Timezone string `yaml:"timezone" json:"timezone,omitempty"`
}

// Location returns the time.Location implied by Timezone. Falls back to
// time.UTC on parse failure; production configs go through Load() which
// rejects invalid timezones up front, so this fallback only matters for
// hand-crafted HouseConfig values in tests.
func (h HouseConfig) Location() *time.Location {
	if h.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(h.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Thresholds describes the per-class activity detection thresholds.
// All fields are pointers so that an explicitly-set zero value is
// honoured and not silently overridden by the class default.
type Thresholds struct {
	IdleBelowW           *float64       `yaml:"idle_below_w"            json:"-"`
	ActiveAboveW         *float64       `yaml:"active_above_w"          json:"-"`
	ActiveSustainedFor   *time.Duration `yaml:"active_sustained_for"    json:"-"`
	InactiveSustainedFor *time.Duration `yaml:"inactive_sustained_for"  json:"-"`
	// CompressorAboveW is used by continuous_power_device. When set, an
	// active cycle begins when power exceeds this value.
	CompressorAboveW *float64 `yaml:"compressor_above_w" json:"-"`
}

// DeviceClassConfig describes one device class profile.
type DeviceClassConfig struct {
	NameHints         []string   `yaml:"name_hints"          json:"name_hints,omitempty"`
	DefaultThresholds Thresholds `yaml:"default_thresholds"  json:"default_thresholds,omitempty"`
	EnergyStrategy    string     `yaml:"energy_strategy"     json:"energy_strategy,omitempty"`
	// StalenessSeconds overrides the default class staleness threshold used
	// by the API DTO layer. When nil the class default is used.
	StalenessSeconds *int `yaml:"staleness_seconds" json:"staleness_seconds,omitempty"`
}

// DeviceConfig overrides classification for a specific known device.
// The canonical fields are Scheme + Primary (and Display). The legacy
// `ieee_address` / `friendly_name` fields are kept as Z2M-shorthand so
// existing YAML keeps working — Load() normalises them.
type DeviceConfig struct {
	// Canonical identity fields. Scheme names the adapter that owns
	// the device ("zigbee", "tasmota", "shelly", ...). Primary is the
	// adapter's stable identifier. Display is the human-readable name.
	Scheme  string `yaml:"scheme"   json:"scheme,omitempty"`
	Primary string `yaml:"primary"  json:"primary,omitempty"`
	Display string `yaml:"display"  json:"display,omitempty"`

	// Legacy Z2M shorthand. Load() converts these to scheme=zigbee +
	// primary=ieee_address / display=friendly_name.
	IEEEAddress  string `yaml:"ieee_address"   json:"ieee_address,omitempty"`
	FriendlyName string `yaml:"friendly_name"  json:"friendly_name,omitempty"`

	Class       string      `yaml:"class"            json:"class,omitempty"`
	DisplayName string      `yaml:"display_name"     json:"display_name,omitempty"`
	Location    string      `yaml:"location"         json:"location,omitempty"`
	Thresholds  *Thresholds `yaml:"thresholds"       json:"thresholds,omitempty"`

	// EnergyStrategy overrides the class-level energy_strategy for this
	// specific device. Use "integration" when the device's counter ticks
	// at too coarse a resolution for its typical cycle size (e.g. a
	// cycle_power_device whose plug reports in 100 Wh increments but
	// whose cycles complete in 20–30 Wh). Without this override such
	// devices raise a stale_counter warning every cycle because the
	// counter never ticks. See config.example.yaml for diagnosis steps.
	EnergyStrategy string `yaml:"energy_strategy" json:"energy_strategy,omitempty"`
}

// Default returns a config populated with safe defaults; YAML values
// override these.
func Default() Config {
	return Config{
		MQTT: MQTTConfig{
			Broker:        "tcp://localhost:1883",
			ClientID:      "statehouse",
			PublishPrefix: "house",
		},
		HTTP: HTTPConfig{Listen: ":8080"},
		RecentLog: RecentLogConfig{
			Path:           "/var/lib/statehouse/events.jsonl",
			RetentionHours: 72,
			MaxSizeMB:      256,
		},
		Adapters: AdaptersConfig{
			Zigbee2MQTT: Zigbee2MQTTConfig{BaseTopic: "zigbee2mqtt"},
		},
		Energy: EnergyConfig{
			DivergenceWarningPct: 20,
			MaxIntegrationGap:    30 * time.Minute,
			Electricity: ElectricityConfig{
				StalenessActive: 90 * time.Second,
				StalenessIdle:   30 * time.Minute,
				IdleBelowW:      5,
			},
		},
		Availability: AvailabilityConfig{
			OfflineDebounce: 30 * time.Second,
		},
		House: HouseConfig{
			QuietAfter:    30 * time.Minute,
			EmptyAfter:    6 * time.Hour,
			SleepingAfter: 2 * time.Hour,
		},
	}
}

// Load reads and parses YAML from path on top of the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Influx.Token == "" && cfg.Influx.TokenFile != "" {
		tok, err := os.ReadFile(cfg.Influx.TokenFile)
		if err != nil {
			return cfg, fmt.Errorf("read influx token: %w", err)
		}
		cfg.Influx.Token = string(trimTrailingNewline(tok))
	}
	if cfg.MQTT.PublishPrefix == "" {
		cfg.MQTT.PublishPrefix = "house"
	}
	if cfg.House.Timezone != "" {
		if _, err := time.LoadLocation(cfg.House.Timezone); err != nil {
			return cfg, fmt.Errorf("parse house.timezone %q: %w", cfg.House.Timezone, err)
		}
	}
	if cfg.Energy.Electricity.StalenessActive <= 0 {
		return cfg, fmt.Errorf("energy.electricity.staleness_active must be > 0; got %v", cfg.Energy.Electricity.StalenessActive)
	}
	if cfg.Energy.Electricity.StalenessIdle <= 0 {
		return cfg, fmt.Errorf("energy.electricity.staleness_idle must be > 0; got %v", cfg.Energy.Electricity.StalenessIdle)
	}
	if cfg.Energy.Electricity.IdleBelowW < 0 {
		return cfg, fmt.Errorf("energy.electricity.idle_below_w must be >= 0; got %v", cfg.Energy.Electricity.IdleBelowW)
	}
	if cfg.Adapters.Zigbee2MQTT.BaseTopic == "" {
		cfg.Adapters.Zigbee2MQTT.BaseTopic = "zigbee2mqtt"
	}
	if cfg.Adapters.Boiler.BaseTopic == "" {
		cfg.Adapters.Boiler.BaseTopic = "energy/boiler/sensor"
	}
	if cfg.Adapters.UPS.BaseTopic == "" {
		cfg.Adapters.UPS.BaseTopic = "ups"
	}
	if cfg.Adapters.Climate.BaseTopic == "" {
		cfg.Adapters.Climate.BaseTopic = "climate"
	}
	if cfg.Adapters.Meter.BaseTopic == "" {
		cfg.Adapters.Meter.BaseTopic = "energy"
	}
	if cfg.Adapters.Intercom.BaseTopic == "" {
		cfg.Adapters.Intercom.BaseTopic = "asterisk"
	}
	// Normalise legacy Z2M shorthand on device entries.
	normaliseDevices(cfg.Devices)
	return cfg, nil
}

// normaliseDevices converts legacy ieee_address/friendly_name shorthands
// into the canonical scheme/primary/display fields. Called by both Load
// (for local YAML) and the remote config fetcher.
func normaliseDevices(devices map[string]DeviceConfig) {
	for id, d := range devices {
		if d.Scheme == "" && (d.IEEEAddress != "" || d.FriendlyName != "") {
			d.Scheme = "zigbee"
		}
		if d.Primary == "" && d.IEEEAddress != "" {
			d.Primary = d.IEEEAddress
		}
		if d.Display == "" && d.FriendlyName != "" {
			d.Display = d.FriendlyName
		}
		devices[id] = d
	}
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
