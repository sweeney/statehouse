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

## Capturing fixtures

`cmd/fixture-capture` is a small CLI that subscribes to an MQTT broker
and writes each received message as one JSON line, in the exact shape
`internal/testdata/fixtures/*.jsonl` expects. Use it to record real
Zigbee2MQTT traffic — including across deliberate broker restarts —
for use as regression fixtures.

```
go build ./cmd/fixture-capture
./fixture-capture \
  -broker tcp://192.168.1.10:1883 \
  -topics "zigbee2mqtt/#" \
  -output internal/testdata/fixtures/my_capture.jsonl
```

It uses paho's auto-reconnect and emits synthetic marker records on
disconnect/reconnect so the timing of broker outages is preserved in
the fixture:

```
{"ts":"...","topic":"_capture/connection_lost","payload":{"error":"..."}}
{"ts":"...","topic":"_capture/reconnected","payload":{"downtime_ms":4123}}
```

Replay can ignore topics under `_capture/` or, in reconnect-specific
tests, assert on them. Output is flushed and fsynced after every line
so SIGKILL doesn't lose data.

Flags: `-broker`, `-client-id`, `-username`, `-password`, `-topics`
(comma-separated), `-output` (`-` for stdout), `-duration` (0 = until
SIGINT), `-qos`, `-mark-reconnects`.

## Architecture

The engine is protocol-agnostic. It accepts canonical `DeviceIdentity`
records (`Scheme` + `Primary` + `Display`) and protocol-normalised
`Reading` values; it does not know anything about Zigbee2MQTT,
Tasmota, Shelly or any other source. Protocol-specific behaviour lives
behind the `internal/adapter.Adapter` interface — one adapter per
source. To add a new source (e.g. Tasmota or Shelly), write an
adapter; the engine, store, energy code, and HTTP/MQTT outputs all
stay untouched.

Available adapters today:

- `internal/adapter/zigbee2mqtt` — Z2M bridge/devices + per-device
  payloads + availability.

## Layout

- `cmd/house-state-engine` — daemon entrypoint.
- `cmd/fixture-capture` — MQTT-to-JSONL fixture recorder.
- `internal/adapter` — protocol-agnostic Adapter interface.
- `internal/adapter/zigbee2mqtt` — Z2M adapter.
- `internal/config` — YAML config + defaults.
- `internal/model` — canonical data types (Reading, Device, Event,
  Snapshot, House). Pointer fields keep the absent-vs-zero
  distinction.
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
