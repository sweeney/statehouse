package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level service configuration loaded from YAML.
type Config struct {
	MQTT          MQTTConfig                  `yaml:"mqtt"`
	HTTP          HTTPConfig                  `yaml:"http"`
	RecentLog     RecentLogConfig             `yaml:"recent_log"`
	Influx        InfluxConfig                `yaml:"influx"`
	Energy        EnergyConfig                `yaml:"energy"`
	Availability  AvailabilityConfig          `yaml:"availability"`
	House         HouseConfig                 `yaml:"house"`
	DeviceClasses map[string]DeviceClassConfig `yaml:"device_classes"`
	Devices       map[string]DeviceConfig      `yaml:"devices"`
}

// MQTTConfig describes broker connectivity and subscription set.
type MQTTConfig struct {
	Broker    string   `yaml:"broker"`
	ClientID  string   `yaml:"client_id"`
	Username  string   `yaml:"username"`
	Password  string   `yaml:"password"`
	Subscribe []string `yaml:"subscribe"`
	// PublishPrefix is prepended to derived MQTT topics. Defaults to "house".
	PublishPrefix string `yaml:"publish_prefix"`
	// Zigbee2MQTTBase is the base topic of the Z2M bridge. Defaults to "zigbee2mqtt".
	Zigbee2MQTTBase string `yaml:"zigbee2mqtt_base"`
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
	DivergenceWarningPct float64       `yaml:"divergence_warning_pct"`
	MaxIntegrationGap    time.Duration `yaml:"max_integration_gap"`
}

type AvailabilityConfig struct {
	OfflineDebounce time.Duration `yaml:"offline_debounce"`
}

type HouseConfig struct {
	// QuietAfter marks the house as quiet when no activity has occurred
	// for this long.
	QuietAfter time.Duration `yaml:"quiet_after"`
	// EmptyAfter marks the house as empty if quiet for this long and no
	// signals of presence have been seen.
	EmptyAfter time.Duration `yaml:"empty_after"`
}

// Thresholds describes the per-class activity detection thresholds.
type Thresholds struct {
	IdleBelowW         float64       `yaml:"idle_below_w"`
	ActiveAboveW       float64       `yaml:"active_above_w"`
	ActiveSustainedFor time.Duration `yaml:"active_sustained_for"`
	InactiveSustainedFor time.Duration `yaml:"inactive_sustained_for"`
	// CompressorAboveW is used by continuous_power_device. When set, an
	// active cycle begins when power exceeds this value.
	CompressorAboveW float64 `yaml:"compressor_above_w"`
}

// DeviceClassConfig describes one device class profile.
type DeviceClassConfig struct {
	NameHints         []string   `yaml:"name_hints"`
	DefaultThresholds Thresholds `yaml:"default_thresholds"`
	EnergyStrategy    string     `yaml:"energy_strategy"`
}

// DeviceConfig overrides classification for a specific known device.
type DeviceConfig struct {
	IEEEAddress string      `yaml:"ieee_address"`
	FriendlyName string     `yaml:"friendly_name"`
	Class       string      `yaml:"class"`
	DisplayName string      `yaml:"display_name"`
	Location    string      `yaml:"location"`
	Thresholds  *Thresholds `yaml:"thresholds"`
}

// Default returns a config populated with safe defaults; YAML values
// override these.
func Default() Config {
	return Config{
		MQTT: MQTTConfig{
			Broker:          "tcp://localhost:1883",
			ClientID:        "house-state-engine",
			Subscribe:       []string{"zigbee2mqtt/#"},
			PublishPrefix:   "house",
			Zigbee2MQTTBase: "zigbee2mqtt",
		},
		HTTP: HTTPConfig{Listen: ":8080"},
		RecentLog: RecentLogConfig{
			Path:           "/var/lib/house-state-engine/events.jsonl",
			RetentionHours: 72,
			MaxSizeMB:      256,
		},
		Energy: EnergyConfig{
			DivergenceWarningPct: 20,
			MaxIntegrationGap:    30 * time.Minute,
		},
		Availability: AvailabilityConfig{
			OfflineDebounce: 30 * time.Second,
		},
		House: HouseConfig{
			QuietAfter: 30 * time.Minute,
			EmptyAfter: 6 * time.Hour,
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
	if cfg.MQTT.Zigbee2MQTTBase == "" {
		cfg.MQTT.Zigbee2MQTTBase = "zigbee2mqtt"
	}
	return cfg, nil
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
