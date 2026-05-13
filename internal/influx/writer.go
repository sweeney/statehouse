package influx

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// pointWriter is the minimal subset of influxdb-client-go's
// api.WriteAPI that this package depends on. Defining it locally
// means tests can substitute a hand-written fake without standing up
// an Influx instance, and we never accidentally couple to API surface
// we don't actually use.
type pointWriter interface {
	WritePoint(p *write.Point)
	Flush()
	Errors() <-chan error
}

// Writer publishes selected measurements and lifecycle summaries to
// InfluxDB. Failure to write must not stop the engine, so all errors
// are logged and counted rather than returned to callers.
type Writer struct {
	Enabled bool
	Store   *state.Store
	Logger  *slog.Logger

	client    influxdb2.Client
	api       pointWriter
	failures  uint64
	successes uint64
	mu        sync.Mutex
}

// Config carries the runtime connection parameters.
type Config struct {
	URL    string
	Org    string
	Bucket string
	Token  string
}

// New constructs a Writer. If enabled is false the writer becomes a
// silent no-op.
func New(enabled bool, cfg Config, store *state.Store, logger *slog.Logger) *Writer {
	w := &Writer{Enabled: enabled, Store: store, Logger: logger}
	if !enabled {
		return w
	}
	if cfg.URL == "" || cfg.Bucket == "" || cfg.Token == "" {
		if logger != nil {
			logger.Warn("influx disabled: missing url/bucket/token")
		}
		w.Enabled = false
		return w
	}
	w.client = influxdb2.NewClient(cfg.URL, cfg.Token)
	w.api = w.client.WriteAPI(cfg.Org, cfg.Bucket)
	errs := w.api.Errors()
	go func() {
		for err := range errs {
			atomic.AddUint64(&w.failures, 1)
			if logger != nil {
				logger.Warn("influx async write error", "error", err)
			}
		}
	}()
	return w
}

// NewWithAPI constructs a Writer with a caller-supplied pointWriter.
// This is the seam tests use to inject FakeWriteAPI; production code
// should use New. The Writer takes no ownership of api — Close() will
// flush it but won't close any underlying network client.
func NewWithAPI(api pointWriter, store *state.Store, logger *slog.Logger) *Writer {
	return &Writer{
		Enabled: true,
		Store:   store,
		Logger:  logger,
		api:     api,
	}
}

// Close flushes pending writes and disconnects.
func (w *Writer) Close() {
	if w == nil || !w.Enabled || w.api == nil {
		return
	}
	w.api.Flush()
	if w.client != nil {
		w.client.Close()
	}
}

// OnCanonicalEvent implements state.CanonicalSink. We write power,
// voltage, energy, temperature and humidity samples to focused
// measurements; everything else is dropped.
func (w *Writer) OnCanonicalEvent(ev model.CanonicalEvent) {
	if w == nil || !w.Enabled {
		return
	}
	d, ok := w.Store.Get(ev.DeviceID)
	if !ok {
		return
	}
	tags := map[string]string{
		"device_id": ev.DeviceID,
		"class":     d.Class,
	}
	if d.Location != "" {
		tags["location"] = d.Location
	}
	var p *write.Point
	switch ev.Attribute {
	case "power_w":
		if v, ok := ev.Value.(float64); ok {
			p = write.NewPoint("device_power", tags, map[string]any{"power_w": v}, ev.Timestamp)
		}
	case "voltage_v":
		if v, ok := ev.Value.(float64); ok {
			p = write.NewPoint("device_power", tags, map[string]any{"voltage_v": v}, ev.Timestamp)
		}
	case "energy_kwh":
		if v, ok := ev.Value.(float64); ok {
			p = write.NewPoint("device_power", tags, map[string]any{"energy_kwh": v}, ev.Timestamp)
		}
	case "temperature_c":
		if v, ok := ev.Value.(float64); ok {
			p = write.NewPoint("device_environment", tags, map[string]any{"temperature_c": v}, ev.Timestamp)
		}
	case "humidity_pct":
		if v, ok := ev.Value.(float64); ok {
			p = write.NewPoint("device_environment", tags, map[string]any{"humidity_pct": v}, ev.Timestamp)
		}
	}
	if p != nil {
		w.api.WritePoint(p)
		atomic.AddUint64(&w.successes, 1)
	}
}

// OnDerivedEvent implements state.EventSink. We record cycle
// completions, activity transitions, and house state transitions.
func (w *Writer) OnDerivedEvent(ev model.DerivedEvent) {
	if w == nil || !w.Enabled {
		return
	}
	switch ev.Type {
	case model.EvtCycleFinished, model.EvtContinuousCycleFinished:
		fields := evidenceAsFields(ev.Evidence)
		if len(fields) == 0 {
			return
		}
		tags := map[string]string{"device_id": ev.DeviceID, "class": ev.DeviceClass}
		if d, ok := w.Store.Get(ev.DeviceID); ok && d.Location != "" {
			tags["location"] = d.Location
		}
		p := write.NewPoint("appliance_cycle", tags, fields, ev.Timestamp)
		w.api.WritePoint(p)
	case model.EvtDeviceActivityChanged:
		from, _ := ev.Evidence["from"].(string)
		to, _ := ev.Evidence["to"].(string)
		tags := map[string]string{"device_id": ev.DeviceID, "class": ev.DeviceClass}
		fields := map[string]any{"from": from, "to": to}
		p := write.NewPoint("device_activity", tags, fields, ev.Timestamp)
		w.api.WritePoint(p)
	case model.EvtHouseStateChanged:
		stateStr, _ := ev.Evidence["state"].(string)
		confidence, _ := ev.Evidence["confidence"].(float64)
		tags := map[string]string{"state": stateStr}
		fields := map[string]any{"confidence": confidence}
		p := write.NewPoint("house_state", tags, fields, ev.Timestamp)
		w.api.WritePoint(p)
	}
}

// Stats returns success/failure counts; useful for /healthz and /metrics.
func (w *Writer) Stats() (success, failure uint64) {
	return atomic.LoadUint64(&w.successes), atomic.LoadUint64(&w.failures)
}

// Ping is a best-effort connectivity check.
func (w *Writer) Ping(ctx context.Context) bool {
	if w == nil || !w.Enabled || w.client == nil {
		return false
	}
	ok, err := w.client.Ping(ctx)
	return ok && err == nil
}

func evidenceAsFields(ev map[string]any) map[string]any {
	if len(ev) == 0 {
		return nil
	}
	out := make(map[string]any, len(ev))
	for k, v := range ev {
		switch v.(type) {
		case int, int32, int64, float32, float64, bool, string:
			out[k] = v
		}
	}
	return out
}
