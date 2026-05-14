// Package ups is the adapter for Network UPS Tools (NUT) devices
// publishing an aggregated state topic:
//
//	ups/{upsname}/state
//
// Each message carries a "variables" map of raw NUT metrics plus a
// pre-computed "computed" block with derived values. The adapter reads
// from "computed" where possible to avoid re-implementing NUT maths.
package ups

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/sweeney/statehouse/internal/adapter/timeutil"
	"github.com/sweeney/statehouse/internal/adapter/validate"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

const SchemeName = "ups"

// Adapter implements adapter.Adapter for NUT-via-MQTT UPS devices.
type Adapter struct {
	engine *state.Engine
	base   string
	logger *slog.Logger
}

// New returns an Adapter watching the given base topic prefix
// (typically "ups"). Logger may be nil.
func New(engine *state.Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = "ups"
	}
	return &Adapter{engine: engine, base: base, logger: logger}
}

func (a *Adapter) Name() string { return SchemeName }

// Subscriptions uses a single-level wildcard so we match
// ups/{upsname}/state for any UPS name without picking up the
// individual scalar topics (ups/{name}/battery/charge etc.).
func (a *Adapter) Subscriptions() []string {
	return []string{a.base + "/+/state"}
}

type upsComputed struct {
	LoadWatts                *float64 `json:"load_watts"`
	BatteryRuntimeMins       *float64 `json:"battery_runtime_mins"`
	OnBattery                *bool    `json:"on_battery"`
	LowBattery               *bool    `json:"low_battery"`
	InputVoltageDeviationPct *float64 `json:"input_voltage_deviation_pct"`
}

type upsPayload struct {
	Timestamp string            `json:"timestamp"`
	UPSName   string            `json:"ups_name"`
	Variables map[string]string `json:"variables"`
	Computed  *upsComputed      `json:"computed"`
}

func (a *Adapter) HandleMessage(topic string, payload []byte, _ bool) {
	if a == nil || a.engine == nil || len(payload) == 0 {
		return
	}
	upsName := upsNameFromTopic(a.base, topic)
	if upsName == "" {
		return
	}

	var p upsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		if a.logger != nil {
			a.logger.Debug("ups/state parse failed", "topic", topic, "error", err)
		}
		return
	}
	// The device identity is always derived from the MQTT topic (trusted).
	// A payload-supplied ups_name is informational only; if it disagrees with
	// the topic name, log a warning for diagnostics but never override the
	// topic-derived identity (see issue #5: spoofing / identity confusion).
	if p.UPSName != "" && p.UPSName != upsName {
		if a.logger != nil {
			a.logger.Warn("ups payload ups_name disagrees with topic; ignoring payload name",
				"topic_name", upsName, "payload_name", p.UPSName, "topic", topic)
		}
	}

	now := time.Now().UTC()
	ts := now
	if p.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, p.Timestamp); err == nil {
			ts = timeutil.Sanitise(t, now)
		}
	}

	r := model.Reading{Timestamp: ts}
	if p.Computed != nil {
		if p.Computed.LoadWatts != nil && validate.FiniteInRange(*p.Computed.LoadWatts, -50_000, 200_000) {
			r.PowerW = p.Computed.LoadWatts
		}
		if p.Computed.BatteryRuntimeMins != nil && validate.FiniteInRange(*p.Computed.BatteryRuntimeMins, 0, 100_000) {
			r.BatteryRuntimeMins = p.Computed.BatteryRuntimeMins
		}
		r.OnBattery = p.Computed.OnBattery
	}
	if v, ok := p.Variables["battery.charge"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && validate.FiniteInRange(f, 0, 100) {
			r.Battery = &f
		}
	}
	if v, ok := p.Variables["input.voltage"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && validate.FiniteInRange(f, 0, 600) {
			r.VoltageV = &f
		}
	}

	id := model.DeviceIdentity{Scheme: SchemeName, Primary: upsName, Display: upsName}
	a.engine.EnsureDiscovered(id, topic)
	a.engine.IngestReading(id, topic, r)
}

// upsNameFromTopic extracts the UPS name from base/upsname/state.
// Returns "" if the name does not match the expected identifier format.
func upsNameFromTopic(base, topic string) string {
	prefix := base + "/"
	if !strings.HasPrefix(topic, prefix) {
		return ""
	}
	rest := topic[len(prefix):]
	if !strings.HasSuffix(rest, "/state") {
		return ""
	}
	name := rest[:len(rest)-len("/state")]
	if strings.Contains(name, "/") {
		return "" // more segments than expected
	}
	if !validate.Identifier(name) {
		return ""
	}
	return name
}
