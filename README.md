# Statehouse: a state engine for the home

V1 Go daemon that turns MQTT telemetry into a canonical
in-memory house/device state and derived MQTT events. See
`PLAN.md`-style requirements in `docs/` once they exist; the runtime
implements the V1 scope described in the originating spec.

## Build

```
go build ./cmd/statehouse
go test ./...
```

## Run

```
statehouse -config config/config.example.yaml
```

Endpoints:

- `GET /healthz`
- `GET /state`
- `GET /state/house`
- `GET /state/devices`
- `GET /state/devices/{id}`
- `GET /state/activity` — active signals and recent activity log.
- `GET /events/recent?limit=100`
- `GET /metrics`
- `GET /config/devices` — resolved profile (class, thresholds, strategy) for every known device.
- `GET /config/devices/{id}` — resolved profile for one device.

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
- `internal/adapter/boiler` — [sweeney/boiler-sensor](https://github.com/sweeney/boiler-sensor)
  CH/HW relay events + lifecycle. Off by default; enable in config.
- `internal/adapter/ups` — Network UPS Tools (NUT) devices publishing
  aggregated state to `ups/{upsname}/state`. Off by default.
- `internal/adapter/climate` — weather stations publishing per-location
  observations to `{base}/{location}/observation`. Off by default.
- `internal/adapter/meter` — Glow/SMETS2 smart meters publishing to
  `energy/{serial}/SENSOR/electricitymeter`. Off by default.
- `internal/adapter/intercom` — Asterisk-via-MQTT phone system. Tracks
  in-flight calls as activity signals (`intercom_ringing`,
  `intercom_answered`, `intercom_hungup`). Off by default.

Device classes today:

- `short_burst_power_device` — kettles, toasters, microwaves.
- `cycle_power_device` — dishwasher, washing machine, dryer.
- `continuous_power_device` — fridge, freezer, dehumidifier.
- `media_power_device` — TV, AV, speakers.
- `binary_state_device` — boiler relays, contact sensors, motion
  sensors, switches that report ON/OFF without power. Activity
  derives from `Reading.State` not power; cycles record duration
  but no energy.
- `environmental_sensor` — measurement-only climate/air-quality/illuminance
  devices. No cycle, no activity machine — `Activity` stays at `reporting`
  while the device transmits. Temp / humidity / battery flow into the device
  record and into Influx as `device_environment` / `device_battery`.
- `ups_sensor` — UPS devices. Measurement-only like `environmental_sensor`
  but carries UPS-specific fields: `on_battery`, `low_battery`,
  `battery_runtime_mins`.
- `energy_meter` — whole-home electricity meters and IHD devices. Reports
  cumulative kWh and instantaneous power. No cycles, no occupancy
  contribution.

## Layout

- `cmd/statehouse` — daemon entrypoint.
- `cmd/fixture-capture` — MQTT-to-JSONL fixture recorder.
- `internal/adapter` — protocol-agnostic Adapter interface.
- `internal/adapter/zigbee2mqtt` — Z2M adapter.
- `internal/adapter/boiler` — sweeney/boiler-sensor adapter.
- `internal/adapter/ups` — NUT UPS adapter.
- `internal/adapter/climate` — weather station adapter.
- `internal/adapter/meter` — Glow/SMETS2 smart meter adapter.
- `internal/adapter/intercom` — Asterisk-via-MQTT intercom adapter.
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

## Remote config

Device classification, per-device overrides, and behaviour tuning are
managed through a remote config service rather than the local YAML file.
On startup the daemon fetches three namespaces from the URL set in
`remote_config.base_url` (authenticated via the identity service):

- `statehouse_devices` — per-device overrides (class, thresholds,
  `energy_strategy`, `display_name`, `location`).
- `statehouse_classes` — device class definitions (name hints, default
  thresholds, energy strategy).
- `statehouse_behaviour` — runtime tuning (energy, availability, house,
  adapter config).

Remote values win over local config on overlap. A namespace that fails
to fetch is skipped non-fatally; the local config value is preserved.
The `/healthz` endpoint reports the fetch status of each namespace.

To update device config (e.g. override a device's energy strategy),
edit the remote config service — not the local YAML.

## Notes

- The engine refuses to integrate power across an interval larger
  than `energy.max_integration_gap` (default 30m). The cycle records
  this without smearing watts across the gap.
- Counter-based energy is preferred for `short_burst_power_device` and
  `cycle_power_device`; integration is preferred for
  `continuous_power_device` and `media_power_device`.
- If counter-reported and integrated energy disagree by more than
  `energy.divergence_warning_pct` (default 20%), an
  `energy_divergence_warning` derived event is emitted.
- Each device accumulates all-time extremes in memory, exposed as the
  `lifetime` block on the device API responses: peak power draw, plus
  min/max temperature and humidity, each with the timestamp it occurred.
  A device only carries the extremes for measurements it actually reports
  (a plug gets `max_power_w`; a climate sensor gets the temperature and
  humidity extremes). These are not persisted — they reset on restart —
  and are intended for UI use such as a current-vs-peak power dial.
- Offline availability is debounced (default 30s) so Z2M restart
  flicker does not produce alarms.
- Device identity is the IEEE address; friendly-name renames keep the
  underlying state.
