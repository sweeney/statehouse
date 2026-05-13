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
	Timestamp int64   `json:"timestamp"`
	RSSI      int     `json:"rssi_dbm"`
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
	r := model.Reading{
		Timestamp:      ts,
		TemperatureC:   p.TemperatureC,
		HumidityPct:    p.HumidityPct,
		PressureHPa:    p.PressureMB,
		WindSpeedMS:    p.WindAvgMS,
		WindDirDeg:     p.WindDirDeg,
		RainfallMM:     p.Rain1MinMM,
		IlluminanceLux: p.IlluminanceLux,
		UVIndex:        p.UVIndex,
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
	r := model.Reading{Timestamp: ts, RSSI: &p.RSSI}
	id := a.identity(location)
	a.engine.EnsureDiscovered(id, topic)
	a.engine.IngestReading(id, topic, r)
}

func (a *Adapter) identity(location string) model.DeviceIdentity {
	return model.DeviceIdentity{Scheme: SchemeName, Primary: location, Display: location}
}

// parseClimateTopic extracts (location, subtype) from
// {base}/{location}/{subtype...}.
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
	return rest[:slash], rest[slash+1:]
}
