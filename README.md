# house-state-engine

V1 Go daemon that turns Zigbee2MQTT telemetry into a canonical
in-memory house/device state and derived MQTT events. See
`PLAN.md`-style requirements in `docs/` once they exist; the runtime
implements the V1 scope described in the originating spec.

## Build

```
go build ./cmd/house-state-engine
go test ./...
```

## Run

```
house-state-engine -config config/config.example.yaml
```

Endpoints:

- `GET /healthz`
- `GET /state`
- `GET /state/house`
- `GET /state/devices`
- `GET /state/devices/{id}`
- `GET /events/recent?limit=100`
- `GET /metrics`

MQTT topics published under `house/`:

- `house/state/snapshot`        (retained)
- `house/state/house`           (retained)
- `house/state/devices/{id}`    (retained, per device)
- `house/events/derived`        (non-retained, one per derived event)

## Layout

- `cmd/house-state-engine` — daemon entrypoint.
- `internal/config` — YAML config + defaults.
- `internal/model` — canonical data types (Reading, Device, Event,
  Snapshot, House). Pointer fields keep the absent-vs-zero
  distinction.
- `internal/zigbee2mqtt` — Z2M topic + payload parsers.
- `internal/normalise` — reserved (currently merged into engine).
- `internal/device` — device profiles, classification, state machines.
- `internal/energy` — counter, power-time integration with gap clamp,
  strategy selection, divergence helper.
- `internal/state` — in-memory store, engine, whole-house derivation.
- `internal/history` — bounded JSONL recent-event log + sink adapter.
- `internal/mqtt` — broker client, Z2M subscriber, derived publisher.
- `internal/influx` — InfluxDB v2 writer (optional, fault-tolerant).
- `internal/httpapi` — HTTP JSON API.
- `internal/testutil` — fake clock, fixture loader.
- `internal/testdata/fixtures` — anonymised MQTT JSONL fixtures.

## Notes

- The engine refuses to integrate power across an interval larger
  than `energy.max_integration_gap` (default 30m). The cycle records
  this without smearing watts across the gap.
- Counter-based energy is preferred for `cycle_power_device` and
  `continuous_power_device`; integration is preferred for
  `short_burst_power_device` and `media_power_device`.
- If counter-reported and integrated energy disagree by more than
  `energy.divergence_warning_pct` (default 20%), an
  `energy_divergence_warning` derived event is emitted.
- Offline availability is debounced (default 30s) so Z2M restart
  flicker does not produce alarms.
- Device identity is the IEEE address; friendly-name renames keep the
  underlying state.
