package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level service configuration loaded from YAML.
type Config struct {
	Site          SiteConfig                   `yaml:"site"`
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

// SiteConfig identifies the property this instance serves and where that property's
// configuration lives.
//
// It is a block rather than a bare id so that adding a second property is a config
// edit rather than a code change: each site names its own devices namespace, which
// is what makes the namespaces per-site rather than one shared document.
type SiteConfig struct {
	// ID matches an entry in the `sites` namespace and supplies the Influx `site`
	// tag value. There is no default: guessing would write a tag asserting which
	// property the readings came from.
	ID string `yaml:"id"`

	// DevicesNamespace is the config namespace holding this site's devices.
	//
	// There is no default. The shared pre-migration namespace it used to fall back
	// to was deleted once every service read its own, so the only guess available
	// names a document that does not exist — and a failed devices fetch is silent:
	// the service starts, reports healthy and serves nothing. It is also not derived
	// from ID, because deriving it would turn a typo in the id into that same silent
	// 404. Both facts are stated so they can be checked against each other.
	DevicesNamespace string `yaml:"devices_namespace"`
}

// UnmarshalYAML accepts either the block form or the bare id it replaced:
//
//	site: home
//	site:
//	  id: home
//	  devices_namespace: devices_home
//
// The deployed config uses the scalar and deploy.sh ships only the binary, so a
// parser that rejected it would take the service down the moment it shipped —
// before anyone could edit the host's config.
func (s *SiteConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var id string
	if err := unmarshal(&id); err == nil {
		s.ID = id
		return nil
	}
	type raw SiteConfig
	var r raw
	if err := unmarshal(&r); err != nil {
		return err
	}
	*s = SiteConfig(r)
	return nil
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
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"`
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
	DivergenceWarningPct float64       `yaml:"divergence_warning_pct"  json:"divergence_warning_pct"`
	MaxIntegrationGap    time.Duration `yaml:"max_integration_gap"     json:"-"`
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
	Thresholds  *Thresholds `yaml:"thresholds"       json:"thresholds,omitempty"`

	// Room is the floorplan room id this device sits in.
	Room string `yaml:"room" json:"room,omitempty"`
	// Covers is what its readings describe when that is not its own room: "house",
	// or another room id. Absent means it covers the room it sits in.
	Covers string `yaml:"covers" json:"covers,omitempty"`
	// Location is the deprecated free-text place. Still decoded because the devices
	// namespace and its consumers migrate on separate schedules.
	Location string `yaml:"location" json:"location,omitempty"`

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

// Validate reports whether the config is complete enough to run a service.
//
// It is deliberately separate from Load: Load parses and normalises, and is used by
// tools and tests that never start a service, while Validate is the gate a running
// service passes through.
//
// Both halves of the site block are errors rather than warnings, for the same reason.
//
// An unset id means the Influx `site` tag is missing, producing exactly the ambiguous
// untagged history the tag exists to prevent, which no later work can recover.
//
// An unnamed devices namespace fails even more quietly: the fetch 404s, applyDevices
// fails open onto an empty snapshot, /healthz still reports "ok", and every endpoint
// honestly serves zero devices. A service that looks healthy and serves nothing is
// worse than one that refuses to start.
func (c Config) Validate() error {
	if c.Site.ID == "" {
		return fmt.Errorf("site is not set: add a site block naming the property this " +
			"instance serves, e.g.\n\n  site:\n    id: <id>\n    devices_namespace: " +
			"devices_<id>\n\nwhere id matches an entry in the sites namespace. There is " +
			"no default because guessing would tag readings with the wrong property")
	}
	if c.Site.DevicesNamespace == "" {
		return fmt.Errorf("site.devices_namespace is not set: name the config namespace "+
			"holding this site's devices, e.g.\n\n  site:\n    id: %s\n    "+
			"devices_namespace: devices_%s\n\nThere is no default: the shared namespace "+
			"this used to fall back to has been deleted, and a failed devices fetch is "+
			"silent — the service would start, report healthy and serve no devices at all",
			c.Site.ID, c.Site.ID)
	}
	return nil
}

// CoverageHouse is the sentinel meaning a device's readings describe the whole
// property rather than the room it sits in.
//
// It is also a legacy `location` value. That field carried two different facts —
// usually a place, but `house` was always a scope — which is the conflation this
// migration exists to remove. Resolving it as a room would publish `house` as a room
// id, which the floorplan taxonomy forbids outright: it is a reserved series key.
const CoverageHouse = "house"

// Place returns the room this device sits in: its Room when the devices namespace has
// been republished, otherwise its deprecated Location — unless that Location is the
// `house` sentinel, which names no room at all.
func (d DeviceConfig) Place() string {
	if d.Room != "" {
		return d.Room
	}
	if d.Location == CoverageHouse {
		return ""
	}
	return d.Location
}

// Coverage returns what this device's readings describe when that is not its own
// room, resolving the legacy `location: house` spelling to the same answer as an
// explicit `covers`.
//
// This is the one place the two spellings are reconciled. Profiles are built with the
// resolved value, so everything downstream reads a Covers that is already correct
// rather than each reader having to remember the legacy form — which is how this
// codebase has repeatedly ended up resolving a value in some paths and not others.
//
// The legacy spelling is honoured even once Room is set. A namespace mid-migration
// may publish `room` before it publishes `covers`, and dropping the coverage fact in
// that window would make republishing order load-bearing with nothing enforcing it.
func (d DeviceConfig) Coverage() string {
	if d.Covers != "" {
		return d.Covers
	}
	if d.Location == CoverageHouse {
		return CoverageHouse
	}
	return ""
}
