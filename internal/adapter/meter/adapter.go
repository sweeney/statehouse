// Package meter is the adapter for Glow/SMETS2 smart meters publishing
// to:
//
//	energy/{serial}/SENSOR/electricitymeter
//
// It also handles Glow TH Bluetooth sensors which piggyback on the same
// hub and publish to:
//
//	energy/{hub_serial}/SENSOR/glowsensorth1/{sensor_serial}
//
// Only import energy is tracked (no generation/export capability).
// Power is converted from kW to W. One device is registered per serial
// with scheme="meter".
package meter

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/sweeney/statehouse/internal/adapter/timeutil"
	"github.com/sweeney/statehouse/internal/adapter/validate"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

const SchemeName = "meter"

// Adapter implements adapter.Adapter for Glow smart meter MQTT traffic.
type Adapter struct {
	engine *state.Engine
	base   string
	logger *slog.Logger
}

// New returns an Adapter watching the given base topic prefix
// (typically "energy"). Logger may be nil.
func New(engine *state.Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = "energy"
	}
	return &Adapter{engine: engine, base: base, logger: logger}
}

func (a *Adapter) Name() string { return SchemeName }

// Subscriptions covers the electricity meter and Glow TH sensor topics.
// The uppercase SENSOR distinguishes Glow topics from the boiler adapter
// which uses lowercase sensor.
func (a *Adapter) Subscriptions() []string {
	return []string{
		a.base + "/+/SENSOR/electricitymeter",
		a.base + "/+/SENSOR/glowsensorth1/+",
	}
}

type meterPayload struct {
	ElectricityMeter struct {
		Timestamp string `json:"timestamp"`
		Energy    struct {
			Import struct {
				Cumulative *float64 `json:"cumulative"`
			} `json:"import"`
		} `json:"energy"`
		Power struct {
			Value *float64 `json:"value"` // kW
		} `json:"power"`
	} `json:"electricitymeter"`
}

type glowSensorMeasurement struct {
	Value *float64 `json:"value"`
}

type glowSensorEntry struct {
	Timestamp   string                 `json:"timestamp"`
	Temperature glowSensorMeasurement  `json:"temperature"`
	Humidity    glowSensorMeasurement  `json:"humidity"`
	Battery     *glowSensorMeasurement `json:"battery"`
	RSSI        *glowSensorMeasurement `json:"rssi"`
}

type glowSensorPayload struct {
	GlowSensorTH1 map[string]glowSensorEntry `json:"glowsensorth1"`
}

func (a *Adapter) HandleMessage(topic string, payload []byte, _ bool) {
	if a == nil || a.engine == nil || len(payload) == 0 {
		return
	}
	if serial := serialFromTopic(a.base, topic); serial != "" {
		a.handleMeter(topic, payload, serial)
		return
	}
	if serial := glowSensorSerial(a.base, topic); serial != "" {
		a.handleGlowSensor(topic, payload, serial)
		return
	}
}

func (a *Adapter) handleMeter(topic string, payload []byte, serial string) {
	var p meterPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		if a.logger != nil {
			a.logger.Debug("meter payload parse failed", "topic", topic, "error", err)
		}
		return
	}

	now := time.Now().UTC()
	ts := now
	if raw := p.ElectricityMeter.Timestamp; raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			ts = timeutil.Sanitise(t, now)
		}
	}

	r := model.Reading{Timestamp: ts}
	if v := p.ElectricityMeter.Energy.Import.Cumulative; v != nil && validate.FiniteInRange(*v, 0, 1e9) {
		r.EnergyKWh = v
	}
	if v := p.ElectricityMeter.Power.Value; v != nil {
		pw := *v * 1000
		if validate.FiniteInRange(pw, -50_000, 200_000) {
			r.PowerW = &pw
		}
	}

	id := model.DeviceIdentity{Scheme: SchemeName, Primary: serial, Display: serial}
	a.engine.EnsureDiscovered(id, topic)
	a.engine.IngestReading(id, topic, r)
}

func (a *Adapter) handleGlowSensor(topic string, payload []byte, sensorSerial string) {
	var p glowSensorPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		if a.logger != nil {
			a.logger.Debug("glow sensor payload parse failed", "topic", topic, "error", err)
		}
		return
	}
	entry, ok := p.GlowSensorTH1[sensorSerial]
	if !ok {
		return
	}

	now := time.Now().UTC()
	ts := now
	if entry.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
			ts = timeutil.Sanitise(t, now)
		}
	}

	r := model.Reading{Timestamp: ts}
	if entry.Temperature.Value != nil && validate.FiniteInRange(*entry.Temperature.Value, -50, 80) {
		r.TemperatureC = entry.Temperature.Value
	}
	if entry.Humidity.Value != nil && validate.FiniteInRange(*entry.Humidity.Value, 0, 100) {
		r.HumidityPct = entry.Humidity.Value
	}
	if entry.Battery != nil && entry.Battery.Value != nil && validate.FiniteInRange(*entry.Battery.Value, 0, 100) {
		r.Battery = entry.Battery.Value
	}
	if entry.RSSI != nil && entry.RSSI.Value != nil && validate.FiniteInRange(*entry.RSSI.Value, -150, 0) {
		v := int(*entry.RSSI.Value)
		r.RSSI = &v
	}

	id := model.DeviceIdentity{Scheme: SchemeName, Primary: sensorSerial, Display: sensorSerial}
	a.engine.EnsureDiscovered(id, topic)
	a.engine.IngestReading(id, topic, r)
}

// serialFromTopic extracts the device serial from
// {base}/{serial}/SENSOR/electricitymeter.
// Returns "" if the serial does not match the expected hex format.
func serialFromTopic(base, topic string) string {
	prefix := base + "/"
	if !strings.HasPrefix(topic, prefix) {
		return ""
	}
	rest := topic[len(prefix):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] != "SENSOR/electricitymeter" {
		return ""
	}
	serial := parts[0]
	if !validate.HexIdentifier(serial) {
		return ""
	}
	return serial
}

// glowSensorSerial extracts the sensor serial from
// {base}/{hub_serial}/SENSOR/glowsensorth1/{sensor_serial}.
// Returns "" if the sensor serial does not match the expected hex format.
func glowSensorSerial(base, topic string) string {
	prefix := base + "/"
	if !strings.HasPrefix(topic, prefix) {
		return ""
	}
	rest := topic[len(prefix):]
	// rest = {hub_serial}/SENSOR/glowsensorth1/{sensor_serial}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[1] != "SENSOR" || parts[2] != "glowsensorth1" || parts[3] == "" {
		return ""
	}
	serial := parts[3]
	if !validate.HexIdentifier(serial) {
		return ""
	}
	return serial
}
