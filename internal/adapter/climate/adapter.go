// Package climate is the adapter for weather stations publishing to
// a topic hierarchy:
//
//	{base}/{location}/observation  — full sensor reading (primary)
//	{base}/{location}/device/status — device health, RSSI
//	{base}/{location}/status       — hub health
//	{base}/{location}/wind/rapid   — high-frequency wind samples (ignored)
//
// One logical device per location is registered with scheme="climate"
// and Primary=location.
package climate

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

const SchemeName = "climate"

// Adapter implements adapter.Adapter for weather station MQTT traffic.
type Adapter struct {
	engine *state.Engine
	base   string
	logger *slog.Logger
}

// New returns an Adapter for the given base topic prefix (typically
// "climate"). Logger may be nil.
func New(engine *state.Engine, base string, logger *slog.Logger) *Adapter {
	if base == "" {
		base = "climate"
	}
	return &Adapter{engine: engine, base: base, logger: logger}
}

func (a *Adapter) Name() string { return SchemeName }

func (a *Adapter) Subscriptions() []string {
	return []string{a.base + "/#"}
}

type observationPayload struct {
	Timestamp      int64    `json:"timestamp"` // unix seconds
	TemperatureC   *float64 `json:"temperature_c"`
	HumidityPct    *float64 `json:"humidity_pct"`
	PressureMB     *float64 `json:"pressure_mb"` // mb == hPa
	WindAvgMS      *float64 `json:"wind_avg_ms"`
	WindDirDeg     *float64 `json:"wind_direction_deg"`
	Rain1MinMM     *float64 `json:"rain_1min_mm"`
	IlluminanceLux *float64 `json:"illuminance_lux"`
	UVIndex        *float64 `json:"uv_index"`
}

type deviceStatusPayload struct {
	Timestamp int64 `json:"timestamp"`
	RSSI      *int  `json:"rssi_dbm"`
}

func (a *Adapter) HandleMessage(topic string, payload []byte, _ bool) {
	if a == nil || a.engine == nil || len(payload) == 0 {
		return
	}
	location, subtype := parseClimateTopic(a.base, topic)
	if location == "" {
		return
	}
	switch subtype {
	case "observation":
		a.handleObservation(location, topic, payload)
	case "device/status":
		a.handleDeviceStatus(location, topic, payload)
	}
}

func (a *Adapter) handleObservation(location, topic string, payload []byte) {
	var p observationPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		if a.logger != nil {
			a.logger.Debug("climate/observation parse failed", "topic", topic, "error", err)
		}
		return
	}
	now := time.Now().UTC()
	ts := now
	if p.Timestamp != 0 {
		ts = timeutil.UnixSeconds(p.Timestamp, now)
	}
	r := model.Reading{Timestamp: ts}
	if p.TemperatureC != nil {
		if validate.FiniteInRange(*p.TemperatureC, -50, 80) {
			r.TemperatureC = p.TemperatureC
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "temperature_c", "value", *p.TemperatureC, "topic", topic)
		}
	}
	if p.HumidityPct != nil {
		if validate.FiniteInRange(*p.HumidityPct, 0, 100) {
			r.HumidityPct = p.HumidityPct
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "humidity_pct", "value", *p.HumidityPct, "topic", topic)
		}
	}
	if p.PressureMB != nil {
		if validate.FiniteInRange(*p.PressureMB, 800, 1100) {
			r.PressureHPa = p.PressureMB
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "pressure_mb", "value", *p.PressureMB, "topic", topic)
		}
	}
	if p.WindAvgMS != nil {
		if validate.FiniteInRange(*p.WindAvgMS, 0, 120) {
			r.WindSpeedMS = p.WindAvgMS
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "wind_avg_ms", "value", *p.WindAvgMS, "topic", topic)
		}
	}
	if p.WindDirDeg != nil {
		if validate.FiniteInRange(*p.WindDirDeg, 0, 360) {
			r.WindDirDeg = p.WindDirDeg
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "wind_direction_deg", "value", *p.WindDirDeg, "topic", topic)
		}
	}
	if p.Rain1MinMM != nil {
		if validate.FiniteInRange(*p.Rain1MinMM, 0, 1000) {
			r.RainfallMM = p.Rain1MinMM
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "rain_1min_mm", "value", *p.Rain1MinMM, "topic", topic)
		}
	}
	if p.IlluminanceLux != nil {
		r.IlluminanceLux = p.IlluminanceLux
	}
	if p.UVIndex != nil {
		if validate.FiniteInRange(*p.UVIndex, 0, 20) {
			r.UVIndex = p.UVIndex
		} else if a.logger != nil {
			a.logger.Warn("adapter: rejected out-of-range field", "field", "uv_index", "value", *p.UVIndex, "topic", topic)
		}
	}
	id := a.identity(location)
	a.engine.EnsureDiscovered(id, topic)
	a.engine.IngestReading(id, topic, r)
}

func (a *Adapter) handleDeviceStatus(location, topic string, payload []byte) {
	var p deviceStatusPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		if a.logger != nil {
			a.logger.Debug("climate/device/status parse failed", "topic", topic, "error", err)
		}
		return
	}
	now := time.Now().UTC()
	ts := now
	if p.Timestamp != 0 {
		ts = timeutil.UnixSeconds(p.Timestamp, now)
	}
	r := model.Reading{Timestamp: ts, RSSI: p.RSSI}
	id := a.identity(location)
	a.engine.EnsureDiscovered(id, topic)
	a.engine.IngestReading(id, topic, r)
}

func (a *Adapter) identity(location string) model.DeviceIdentity {
	return model.DeviceIdentity{Scheme: SchemeName, Primary: location, Display: location}
}

// parseClimateTopic extracts (location, subtype) from
// {base}/{location}/{subtype...}.
// Returns ("","") if the location does not match the expected identifier format.
func parseClimateTopic(base, topic string) (location, subtype string) {
	prefix := base + "/"
	if !strings.HasPrefix(topic, prefix) {
		return "", ""
	}
	rest := topic[len(prefix):]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "" // no subtype
	}
	loc := rest[:slash]
	if !validate.Identifier(loc) {
		return "", ""
	}
	return loc, rest[slash+1:]
}
