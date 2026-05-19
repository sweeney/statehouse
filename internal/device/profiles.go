package device

import (
	"sort"
	"strings"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/energy"
	"github.com/sweeney/statehouse/internal/model"
)

// Known device class identifiers. New classes can be added without
// touching this file by extending config.DeviceClasses, but the engine
// only understands these canonical V1 classes for state-machine
// behaviour.
const (
	ClassShortBurst = "short_burst_power_device"
	ClassCyclePower = "cycle_power_device"
	ClassContinuous = "continuous_power_device"
	ClassMedia      = "media_power_device"
	// ClassBinaryState covers devices whose telemetry is a direct
	// ON/OFF state rather than a power reading: a boiler relay, a
	// contact sensor, a motion sensor, a smart switch reporting
	// state without power. Activity derives from State, not PowerW.
	ClassBinaryState = "binary_state_device"
	// ClassEnvironmentalSensor covers measurement-only devices (climate,
	// air quality, illuminance, Zigbee TH sensors) that have no
	// behavioural state. Activity stays at "reporting" once the engine
	// has seen at least one reading. No cycles, no hysteresis, no
	// occupancy contribution.
	ClassEnvironmentalSensor = "environmental_sensor"
	// ClassUPSSensor covers uninterruptible power supply devices. Like
	// ClassEnvironmentalSensor they are measurement-only (no cycles, no
	// hysteresis, no occupancy contribution), but carry UPS-specific
	// fields (on_battery, low_battery, battery_runtime_mins).
	ClassUPSSensor = "ups_sensor"
	// ClassEnergyMeter covers whole-home electricity meters and IHD
	// devices. Measurement-only: reports cumulative kWh, import/export,
	// and instantaneous power. No cycles, no occupancy contribution.
	ClassEnergyMeter  = "energy_meter"
	ClassUnclassified = "unclassified"
)

// IsPassiveSensor reports whether class is a measurement-only sensor
// class that has no active/idle state machine — currently
// ClassEnvironmentalSensor, ClassUPSSensor, and ClassEnergyMeter.
func IsPassiveSensor(class string) bool {
	return class == ClassEnvironmentalSensor || class == ClassUPSSensor || class == ClassEnergyMeter
}

// Profile is the resolved per-device configuration used at runtime.
type Profile struct {
	ID           string
	Class        string
	DisplayName  string
	Location     string
	Thresholds   config.Thresholds
	Strategy     energy.Strategy
	Unclassified bool
}

// Resolver provides classification + threshold lookup for devices.
// Overrides are keyed by canonical "scheme:primary" or
// "scheme:display" — adapter-agnostic so a single overrides block
// covers Z2M, Tasmota, Shelly, raw-topic, etc.
type Resolver struct {
	classes map[string]config.DeviceClassConfig
	// byPrimary keys configured overrides by "scheme:primary".
	byPrimary map[string]config.DeviceConfig
	// byDisplay keys configured overrides by "scheme:display" (lowercased display).
	byDisplay   map[string]config.DeviceConfig
	idByPrimary map[string]string
	idByDisplay map[string]string
}

// NewResolver builds a resolver from the loaded config. Device entries
// in config.Devices are normalised by config.Load() so this resolver
// sees canonical Scheme/Primary/Display fields even when the YAML
// used the legacy ieee_address/friendly_name shorthand.
func NewResolver(cfg config.Config) *Resolver {
	r := &Resolver{
		classes:     cfg.DeviceClasses,
		byPrimary:   make(map[string]config.DeviceConfig),
		byDisplay:   make(map[string]config.DeviceConfig),
		idByPrimary: make(map[string]string),
		idByDisplay: make(map[string]string),
	}
	for id, d := range cfg.Devices {
		if d.Scheme != "" && d.Primary != "" {
			k := d.Scheme + ":" + strings.ToLower(d.Primary)
			r.byPrimary[k] = d
			r.idByPrimary[k] = id
		}
		if d.Scheme != "" && d.Display != "" {
			k := d.Scheme + ":" + strings.ToLower(d.Display)
			r.byDisplay[k] = d
			r.idByDisplay[k] = id
		}
	}
	return r
}

// Resolve produces a Profile for a discovered device. If a configured
// override matches by (scheme, primary) or (scheme, display) it wins.
// Otherwise the resolver attempts name-hint classification against
// the Display, falling back to unclassified.
func (r *Resolver) Resolve(identity model.DeviceIdentity) Profile {
	p := Profile{
		Class:        ClassUnclassified,
		DisplayName:  identity.Display,
		Unclassified: true,
	}
	// 1. Explicit override by scheme:primary.
	if d, ok := r.byPrimary[primaryKey(identity)]; ok {
		p = profileFromOverride(d, r.classes)
		if id, ok := r.idByPrimary[primaryKey(identity)]; ok {
			p.ID = id
		}
		if p.DisplayName == "" {
			p.DisplayName = identity.Display
		}
		return p
	}
	// 2. Override by scheme:display.
	if d, ok := r.byDisplay[displayKey(identity)]; ok {
		p = profileFromOverride(d, r.classes)
		if id, ok := r.idByDisplay[displayKey(identity)]; ok {
			p.ID = id
		}
		if p.DisplayName == "" {
			p.DisplayName = identity.Display
		}
		return p
	}
	// 3. Name-hint classification against display name.
	if class, ok := r.classifyByHints(identity.Display); ok {
		p.Class = class
		p.Unclassified = false
		p.Thresholds = r.classes[class].DefaultThresholds
		p.Strategy = strategyFor(r.classes[class].EnergyStrategy)
	}
	return p
}

// ConfiguredID returns the engine-facing device id from a configured
// override; empty if not configured.
func (r *Resolver) ConfiguredID(identity model.DeviceIdentity) string {
	if id, ok := r.idByPrimary[primaryKey(identity)]; ok {
		return id
	}
	if id, ok := r.idByDisplay[displayKey(identity)]; ok {
		return id
	}
	return ""
}

func primaryKey(d model.DeviceIdentity) string {
	if d.Scheme == "" || d.Primary == "" {
		return ""
	}
	return d.Scheme + ":" + strings.ToLower(d.Primary)
}

func displayKey(d model.DeviceIdentity) string {
	if d.Scheme == "" || d.Display == "" {
		return ""
	}
	return d.Scheme + ":" + strings.ToLower(d.Display)
}

func (r *Resolver) classifyByHints(name string) (string, bool) {
	n := strings.ToLower(name)
	if n == "" {
		return "", false
	}
	type match struct {
		class   string
		hintLen int
	}
	var matches []match
	for class, cfg := range r.classes {
		for _, hint := range cfg.NameHints {
			if hint == "" {
				continue
			}
			if strings.Contains(n, strings.ToLower(hint)) {
				matches = append(matches, match{class: class, hintLen: len(hint)})
				break // one match per class is enough
			}
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].hintLen != matches[j].hintLen {
			return matches[i].hintLen > matches[j].hintLen // longer hint wins
		}
		return matches[i].class < matches[j].class // lexicographic tiebreaker
	})
	return matches[0].class, true
}

func profileFromOverride(d config.DeviceConfig, classes map[string]config.DeviceClassConfig) Profile {
	p := Profile{
		Class:       d.Class,
		DisplayName: d.DisplayName,
		Location:    d.Location,
	}
	if cls, ok := classes[d.Class]; ok {
		p.Thresholds = cls.DefaultThresholds
		p.Strategy = strategyFor(cls.EnergyStrategy)
	}
	if d.Thresholds != nil {
		p.Thresholds = mergeThresholds(p.Thresholds, *d.Thresholds)
	}
	// Per-device strategy wins over the class default. This exists to
	// handle devices whose hardware counter ticks at a resolution too
	// coarse for their typical cycle size (e.g. 100 Wh increments on a
	// device that completes cycles in 20–30 Wh). Those devices reliably
	// raise stale_counter every cycle; the fix is to opt that specific
	// device into integration without changing the class for all others.
	if d.EnergyStrategy != "" {
		p.Strategy = strategyFor(d.EnergyStrategy)
	}
	return p
}

func mergeThresholds(base, override config.Thresholds) config.Thresholds {
	out := base
	if override.IdleBelowW != nil {
		out.IdleBelowW = override.IdleBelowW
	}
	if override.ActiveAboveW != nil {
		out.ActiveAboveW = override.ActiveAboveW
	}
	if override.ActiveSustainedFor != nil {
		out.ActiveSustainedFor = override.ActiveSustainedFor
	}
	if override.InactiveSustainedFor != nil {
		out.InactiveSustainedFor = override.InactiveSustainedFor
	}
	if override.CompressorAboveW != nil {
		out.CompressorAboveW = override.CompressorAboveW
	}
	return out
}

func strategyFor(s string) energy.Strategy {
	switch strings.ToLower(s) {
	case "counter":
		return energy.StrategyCounter
	case "integration":
		return energy.StrategyIntegration
	}
	// Sensible default: counter where present (long appliances), but
	// for unset strategies we still let the engine integrate; the
	// engine picks SelectedKWh from whichever path has data.
	return energy.StrategyIntegration
}
