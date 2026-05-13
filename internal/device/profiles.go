package device

import (
	"strings"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/energy"
)

// Known device class identifiers. New classes can be added without
// touching this file by extending config.DeviceClasses, but the engine
// only understands the four canonical V1 classes for state-machine
// behaviour.
const (
	ClassShortBurst     = "short_burst_power_device"
	ClassCyclePower     = "cycle_power_device"
	ClassContinuous     = "continuous_power_device"
	ClassMedia          = "media_power_device"
	ClassUnclassified   = "unclassified"
)

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
type Resolver struct {
	classes map[string]config.DeviceClassConfig
	// byIEEE keys configured device overrides by IEEE address.
	byIEEE map[string]config.DeviceConfig
	// byName keys configured overrides by friendly name (lowercased).
	byName map[string]config.DeviceConfig
	// idByIEEE maps IEEE -> configured device id (the YAML map key).
	idByIEEE map[string]string
	idByName map[string]string
}

// NewResolver builds a resolver from the loaded config.
func NewResolver(cfg config.Config) *Resolver {
	r := &Resolver{
		classes:  cfg.DeviceClasses,
		byIEEE:   make(map[string]config.DeviceConfig),
		byName:   make(map[string]config.DeviceConfig),
		idByIEEE: make(map[string]string),
		idByName: make(map[string]string),
	}
	for id, d := range cfg.Devices {
		if d.IEEEAddress != "" {
			r.byIEEE[strings.ToLower(d.IEEEAddress)] = d
			r.idByIEEE[strings.ToLower(d.IEEEAddress)] = id
		}
		if d.FriendlyName != "" {
			r.byName[strings.ToLower(d.FriendlyName)] = d
			r.idByName[strings.ToLower(d.FriendlyName)] = id
		}
	}
	return r
}

// Resolve produces a Profile for a discovered device. If a configured
// override matches by IEEE or friendly name it wins. Otherwise the
// resolver attempts name-hint classification, falling back to
// unclassified.
func (r *Resolver) Resolve(ieee, friendlyName string) Profile {
	p := Profile{
		Class:        ClassUnclassified,
		DisplayName:  friendlyName,
		Unclassified: true,
	}
	// 1. Explicit override by IEEE address.
	if d, ok := r.byIEEE[strings.ToLower(ieee)]; ok && ieee != "" {
		p = profileFromOverride(d, r.classes)
		if id, ok := r.idByIEEE[strings.ToLower(ieee)]; ok {
			p.ID = id
		}
		if p.DisplayName == "" {
			p.DisplayName = friendlyName
		}
		return p
	}
	// 2. Override by friendly name.
	if d, ok := r.byName[strings.ToLower(friendlyName)]; ok && friendlyName != "" {
		p = profileFromOverride(d, r.classes)
		if id, ok := r.idByName[strings.ToLower(friendlyName)]; ok {
			p.ID = id
		}
		if p.DisplayName == "" {
			p.DisplayName = friendlyName
		}
		return p
	}
	// 3. Name-hint classification.
	if class, ok := r.classifyByHints(friendlyName); ok {
		p.Class = class
		p.Unclassified = false
		p.Thresholds = r.classes[class].DefaultThresholds
		p.Strategy = strategyFor(r.classes[class].EnergyStrategy)
	}
	return p
}

// ConfiguredID returns the engine-facing device id from a configured
// override; empty if not configured.
func (r *Resolver) ConfiguredID(ieee, friendlyName string) string {
	if id, ok := r.idByIEEE[strings.ToLower(ieee)]; ok && ieee != "" {
		return id
	}
	if id, ok := r.idByName[strings.ToLower(friendlyName)]; ok && friendlyName != "" {
		return id
	}
	return ""
}

func (r *Resolver) classifyByHints(name string) (string, bool) {
	n := strings.ToLower(name)
	if n == "" {
		return "", false
	}
	for class, cfg := range r.classes {
		for _, hint := range cfg.NameHints {
			if hint == "" {
				continue
			}
			if strings.Contains(n, strings.ToLower(hint)) {
				return class, true
			}
		}
	}
	return "", false
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
	return p
}

func mergeThresholds(base, override config.Thresholds) config.Thresholds {
	out := base
	if override.IdleBelowW != 0 {
		out.IdleBelowW = override.IdleBelowW
	}
	if override.ActiveAboveW != 0 {
		out.ActiveAboveW = override.ActiveAboveW
	}
	if override.ActiveSustainedFor != 0 {
		out.ActiveSustainedFor = override.ActiveSustainedFor
	}
	if override.InactiveSustainedFor != 0 {
		out.InactiveSustainedFor = override.InactiveSustainedFor
	}
	if override.CompressorAboveW != 0 {
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
