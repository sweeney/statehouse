# A hot water service, from statehouse's side

Design notes for a planned service, not yet built, that models the house's hot
water. It reads the cylinder temperature probes, holds the state of each tank
and of the system as a whole, and publishes on MQTT:

- a **snapshot**: hot water available (litres), baths available, showers
  available, and how those were worked out;
- **draw events**: "a draw of about 50 l just happened".

Statehouse is expected to be its main consumer. It would hold the snapshot in
its own state and use draws as an activity signal for house occupancy. This
document records what the service has to provide for statehouse to do that
well, and what statehouse will need to change. It was written before either
side existed, so treat it as a checklist and a record of the reasoning, not a
specification. The service's own `docs/WIRE.md` will be the normative contract
once it exists, as tanktemp's is today.

## What already exists

Statehouse already reads the cylinder probes directly. The `tanktemp` adapter
(`internal/adapter/tanktemp/adapter.go`) consumes sweeney/tanktemp:

- `energy/hot_water/{device_id}/state`: retained, every 30 s, one message per
  cylinder listing every probe bottom first, with `temp_c` or `null`;
- `energy/hot_water/status`: STARTUP, HEARTBEAT, SHUTDOWN, and the LWT.

Each probe is an engine device (scheme `tanktemp`, primary
`<cylinder id>.<position>`, class `appliance_probe`, `part_of` the cylinder).
The cylinder itself is a `telemetry: false` entry in the devices namespace
(see `config/remote/devices_example.json`) and is never an engine device.
Probes are passive sensors, so they do not drive occupancy
(`isOccupancyRelevant` in `internal/state/house.go`).

The boiler adapter separately reports the boiler's hot water relay (scheme
`boiler`, primary `hw`), so statehouse knows when the cylinder is being
reheated.

**Open decision:** does the new service sit *on top of* tanktemp (statehouse
keeps reading the raw probes and adds the derived view) or *replace* it as
statehouse's source of probe readings? The working assumption here is on top.

## 1. Topics: do not publish under `energy/hot_water`

The tanktemp adapter subscribes to exactly `energy/hot_water/+/state` and
`energy/hot_water/status`, and it assumes one publisher per prefix:

- `energy/hot_water/system/state` would be parsed as a cylinder called
  `system`;
- a second `energy/hot_water/status` would let the new service's LWT mark
  every tanktemp probe offline (`handleStatus` marks every known probe offline
  on any offline status).

Give the service its own prefix, for example:

| Topic | Retained | Content |
|---|---|---|
| `energy/hot_water_model/state` | yes | system snapshot, including per-tank state |
| `energy/hot_water_model/events` | **no** | draw events |
| `energy/hot_water_model/status` | yes | lifecycle and LWT, tanktemp's shape |

The name is a placeholder; the requirement is a prefix of its own.

## 2. Snapshots and events behave differently

| | Snapshot | Draw events |
|---|---|---|
| Retained | Yes, so a restarting statehouse gets current state at once | **No.** A retained draw would be replayed on every statehouse restart and read as new activity |
| Time | Measurement time, plus `clock_synced` | `started_at` (estimated), `ended_at`, `detected_at` |
| Dedup | By timestamp, like tanktemp's replay check (`newSweep`) | By a stable `draw_id`: QoS 1 can deliver a message twice |

Carry over tanktemp's wire conventions, which the existing adapter already
relies on:

- every key is always present;
- `null` means unknown, never `0`;
- a `schema_version` field;
- `clock_synced`, because a Pi has no RTC. Statehouse refuses a retained
  message that is unsynced or older than its staleness window, and re-stamps a
  live one (`sweepTime` in the tanktemp adapter). The new service has to give
  it the means to make the same check.

Include `last_draw` (time and volume) in the retained snapshot. Statehouse
holds state in memory only, so without it a restart forgets that someone
showered ten minutes ago, and the occupancy windows (`quiet_after`,
`empty_after`) restart from nothing.

## 3. Draw events and occupancy

### The statehouse pattern they fit

Draws map onto the pattern the intercom adapter uses for calls
(`internal/adapter/intercom/adapter.go`):

1. `IngestSignal` an `ActivitySignal` when the draw starts, with a safety-net
   `ExpiresAt` so a lost end event cannot leave a zombie signal;
2. `RecordActivity` an `ActivityRecord` so `/state` can say "last draw 7
   minutes ago";
3. on the end event, `UpdateActivity` with the final volume and
   `ClearSignal`.

### What the service needs to publish

- **Start, update and end, not one event afterwards.** Detecting a draw from
  temperature lags: sweeps are 30 s apart and cold water enters at the bottom.
  If the service only reports when a ten-minute shower has finished,
  occupancy hears about it late. Publish `draw_started` once reasonably sure,
  optionally `draw_updated`, then `draw_ended` with the final volume, all with
  the same `draw_id`.
- **An estimated start time, separate from the detection time.** Statehouse
  uses the signal's `Since` as the activity time for the occupancy windows, so
  it should be when the draw began, not when the service noticed it.
- **A confidence in [0,1], and false-positive rejection in the service.**
  Things that look like a draw:
  - standing heat loss;
  - mixing and stratification changes during reheat;
  - a secondary-return pump on a timer (this one looks most like a draw);
  - the legionella cycle.

  The service is best placed to reject these: it has the full temperature
  profile, and it can also subscribe to the boiler `hw` relay.

  **This matters more than it looks:** `DeriveHouseState` ignores
  `ActivitySignal.Confidence`. Every active signal counts as one unit of
  presence. A false draw while the house is empty flips it to occupied. So a
  confidence threshold has to be applied before `IngestSignal`, in the
  adapter, until house state learns to weigh confidence.
- **A classification as a hint, with its own confidence.** For example
  `kind: shower | bath | tap | unknown`, plus volume, duration and an
  estimated flow rate. Statehouse then decides which kinds count (a 2 l tap
  run is weaker evidence than a bath).
- **No invented location.** Draws cannot be tied to a room without knowing
  the outlets, so they are house-wide (empty `Location` on the signal).

## 4. The snapshot

- **Define "available".** It could mean litres above a usable temperature
  (say 40 °C), or mixed-equivalent litres at a delivery temperature. These
  give very different numbers. Publish the basic quantities: usable energy
  (kWh above the cold inlet) and litres at a stated reference temperature.
  Publish baths and showers as derived values, **with their assumptions in the
  payload**: litres per bath, shower flow × minutes, delivery temperature,
  cold inlet temperature. Statehouse can then show them or recompute them,
  instead of relaying numbers nobody can check.
- **Quality.** With a probe dead the estimate gets worse. Publish a
  `quality` (`ok | degraded | unknown`) and which probes were used. Statehouse
  cannot carry per-reading quality yet: tanktemp's `suspect` flag is logged
  and dropped (`TODO(#82)` in the tanktemp adapter). Carrying quality from
  this service needs #82 done first, or a field of its own.
- **Per tank and per system.** Use cylinder ids that match tanktemp's
  `device_id` and the devices namespace (`hot_water_cylinder`). Statehouse
  relays placement from the devices namespace and never derives it
  (`docs/floorplan-vocabulary.md`), so the service should not invent its own
  ids for the same tanks.
- **A system state useful downstream**, for example
  `heating | idle | drawing`, and a time-to-full estimate while heating.
- **Publish on change and on a heartbeat**, so statehouse can tell a stale
  snapshot from a quiet one.

## 5. Work needed in statehouse

None of this exists yet.

- **A new adapter**, with its own scheme (e.g. `hotwater`), in
  `internal/adapter/`, wired in `cmd` like the others. Switching it on goes in
  `statehouse_behaviour.adapters`, which is remote-only and needs a restart,
  not SIGHUP (`docs/remote-config.md`). Document its default `base_topic` in
  the adapters table there and in the README.
- **How the snapshot is modelled.** `model.Reading` has nothing for litres,
  kWh stored, or bath/shower counts. Options:
  - new `Reading` fields on the cylinder, which would stop it being
    `telemetry: false`;
  - a separate `hot_water_system` device with a class of its own.

  Either way it touches `docs/influx-schema.md`, the HTTP DTOs,
  `internal/httpapi/openapi.yaml`, and the `documentedSchemas` map in
  `internal/httpapi/spec_fields_test.go` (see `CLAUDE.md`).
- **Occupancy policy** in the adapter and `internal/state/house.go`: the TTL
  of a draw signal, the minimum confidence and volume that make a draw a
  signal at all, which `kind`s count, and whether draws feed the activity
  dimension, occupancy, or both.

## 6. Contract and tests

Make the service's `docs/WIRE.md` normative, as tanktemp's is, and ship golden
payload fixtures with it: snapshot, draw start and end, degraded probe,
unsynced clock, LWT. Statehouse's adapter tests can use them as
`internal/adapter/tanktemp/testdata` does for tanktemp.
