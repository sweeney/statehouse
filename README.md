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

The config must declare which property this instance serves:

```yaml
site:
  id: home
  devices_namespace: devices_home
```

`devices_namespace` names the config namespace holding this site's devices, so adding
a second property is a config edit rather than a code change.

**Both halves are required and neither has a default.** `statehouse_devices` — the
single shared namespace every service read before devices were split per site — was
deleted once each service read its own, so the fallback that used to exist now names a
document that is not there. A failed devices fetch is silent: it fails open onto an
empty snapshot, `/healthz` still reports `ok`, and every endpoint honestly serves zero
devices. Refusing to start is the louder failure and the cheaper one.

The older `site: home` scalar still parses as `site: {id: home}`, but it names no
namespace, so it is no longer a startable config on its own.

**This can deploy before or after the namespace is published, in either order.** An
unedited config reads the shared namespace exactly as before, so there is no window
where the binary and the config disagree — unlike the two changes before it in this
migration, both of which had a sequencing hazard.

One observable consequence: the remote-config block on `/healthz` is keyed by
namespace name, so its devices entry reports `statehouse_devices` today and
`devices_home` once the site names one. Keyed by name rather than a fixed label
deliberately — the sibling entries are namespace names too, and a health block naming
a namespace it was not actually reading would be worse than one whose key moves when
the namespace does.

There is no default, and **statehouse refuses to start without it**. `site` is written
as a tag on every Influx point, which is what keeps `device_id` unambiguous once a
second property reports into the same bucket — a second site already exists in the
`sites` namespace, so this is not hypothetical. It is `site` and not `location`
because `location` is already in the data meaning a room.

Starting without it would write untagged points that look identical to the ambiguous
history the tag exists to prevent, and nothing would surface the mistake until the
data was already unrecoverable. A refused start is the cheaper failure.

> **Upgrading an existing deployment:** add `site:` to the host's config *before*
> deploying this version, or the service will not come back up on restart.

Points no longer carry a `location` tag. It was write-only — both read services
decoded it into a field neither ever read, and rooms resolve at read time from the
floorplan instead. Old points keep their stale tag and are simply unread, so there is
no backfill to get wrong and a room rename never touches stored data.

Points written before this tag existed carry no `site` at all. A consumer filtering on
`site == "home"` would silently exclude all of that history, so consumers treat an
absent tag as the primary site rather than filtering it away.

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

## CORS

Browser clients are supported, and they are **off by default**. `http.allowed_origins`
names the origins permitted to read the API cross-origin; an empty or absent list
means none, which is exactly how the service behaved before CORS existed.

```yaml
http:
  allowed_origins:
    - "https://*.swee.net"
    - "http://localhost:*"   # development
```

Accepted forms:

| Entry | Matches |
|---|---|
| `https://app.swee.net` | that origin exactly |
| `https://*.swee.net` | any subdomain of `swee.net`, at any depth — **not** `swee.net` itself |
| `http://localhost:*` | any port on that host, including none |
| `https://*.swee.net:*` | the two wildcards combine |
| `*` | every origin, as a flat wildcard |

A malformed entry **refuses the start**, rather than being skipped. A skipped entry
fails silently in both directions: an origin the operator believes is allowlisted is
not, or one they believe is excluded was never parsed — and neither is visible from
the server side. That is the same trade this service already makes for an unset `site`.

The apex is not implied by its wildcard. `https://*.swee.net` admits `app.swee.net`
and not `swee.net`; listing the apex is one more line. It also does not admit
`evil-swee.net`, which is the suffix-matching bug that a bare "ends with swee.net"
check would have — worth stating because it is the failure that looks like it works.

### What it does and does not protect

A CORS allowlist is **not access control**. It tells a browser which pages may read a
response with JavaScript. It constrains nothing else: `curl`, a server-side client and
anything that is not a browser ignore it entirely, and the API is exactly as reachable
to them with the list empty as with it wide open. The control protecting this API is
the **Bearer token**.

What the allowlist does buy is that a page on some other origin cannot spend a token
the browser already holds. That is worth having — it is why the list is an allowlist
rather than the flat wildcard the sister services send — but it is a narrowing of blast
radius, not a boundary.

It is deliberately **local YAML only**, not remote config. Remote config tunes
behaviour, but a namespace that fails to fetch is skipped non-fatally: a remote
widening would be an unreviewed grant, and a remote tightening would be silently
dropped the first time the fetch failed. The setting that says which pages may spend a
token belongs in the file the operator edits.

No `Access-Control-Allow-Credentials` is ever sent. The API authenticates by Bearer
token, never by cookie, so there is nothing ambient for a cross-origin page to ride on.

### Where the layer sits

The wrapper is above the mux — above auth, above every route — because three things
are impossible anywhere else:

- **Preflights must bypass auth.** A browser never attaches credentials to an
  `OPTIONS` request; that is specified behaviour, not a client bug, so a preflight
  that reaches the auth middleware can never be satisfied. They are answered here,
  with `204`.
- **Error responses need the headers too.** Without `Access-Control-Allow-Origin` on a
  `401` the browser refuses to expose the response at all, so a client cannot tell
  "your token expired" from "the service is down". Setting the headers on the way in
  means every response carries them — including the mux's own `404`, which no handler
  of ours ever sees.
- **`/healthz` is not behind auth**, so a layer inside the auth path could never
  cover it.

`Vary: Origin` is sent on every response whose `Access-Control-Allow-Origin` depends on
the request origin — including the ones where no origin matched, since the response
*would* have differed for another. Cloudflare sits in front of this service, so that
one is load-bearing.

`Timing-Allow-Origin` tracks `Access-Control-Allow-Origin` exactly. It is a separate
opt-in that CORS does not imply: without it a cross-origin consumer's
`PerformanceResourceTiming` entry has every phase (DNS, TCP, TLS, TTFB) and both
transfer sizes zeroed, leaving only total duration — so a dashboard cannot tell a slow
query from a slow network. countinghouse and greenhouse both send it as a flat
wildcard; statehouse sends it to the origins it already answers, so the two headers
cannot disagree about who is trusted.

`Access-Control-Expose-Headers: WWW-Authenticate` lets browser JS read the challenge on
a `401`. Only a short safelist is readable cross-origin by default, so without it the
realm is invisible to exactly the client that needs it.

`Access-Control-Max-Age: 86400` matches what `config.swee.net` and `id.swee.net`
already send. Browsers cap it well below that, so agreeing costs nothing.

### Two routes that are not like the others

`GET /openapi.json` always answers `Access-Control-Allow-Origin: *`, whatever the
allowlist says — this is the header `spec.go` used to set by hand, now expressed as
route policy so there is one place to reason about it. The route is unauthenticated, so
the spec is already world-readable by anything that is not a browser; narrowing it to
the allowlist would protect nothing while breaking Swagger UI, Redoc,
`editor.swagger.io` and every codegen tool that fetches a spec from an arbitrary origin.

`GET /healthz` is **not** given that treatment, though it is also unauthenticated. The
spec is a static document describing what the API is; `/healthz` reports live
operational detail about one deployment — version, uptime, goroutine count, which
remote namespaces are failing and the error text saying why. A wildcard would let any
page anyone happens to visit read that. It is a fingerprint of a specific house, so it
stays on the allowlist.

`GET /metrics` stays behind auth and inherits the allowlist like every other route. It
carries counters and runtime stats, no secrets, and a browser ops dashboard is a
legitimate consumer — so there is no reason to make it the one authenticated route a
browser cannot reach.

### Upgrading

Deploying this version against an unedited config changes nothing: no origins means no
CORS, and `/openapi.json` keeps the wildcard it already had. Browser access is opt-in
per deployment via `http.allowed_origins`.

One behaviour does change for every deployment: `OPTIONS` on any path now returns `204`
from the CORS layer rather than reaching a route. For an allowlisted origin it carries
the preflight headers; otherwise it carries none and the browser refuses the real
request, which is the specified way to say no. Nothing served a useful `OPTIONS` before
— authenticated routes answered `401` — so no client loses anything.

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
- `internal/origin` — browser-origin allowlist: compiling and matching.
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
