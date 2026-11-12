# Remote config

statehouse has two sources of configuration, and every setting has exactly
one of them.

| Source | Holds |
|---|---|
| The local YAML file (`-config`, e.g. `/etc/statehouse/config.yaml`) | What the host needs to start and to reach everything else: `site`, `mqtt`, `identity`, `remote_config`, `http`, `auth`, `recent_log`, `influx`. See `config/config.example.yaml`. |
| The config service (`remote_config.base_url`) | Everything else, in three namespaces (below). See the examples in `config/remote/`. |

The local file cannot set a remote setting. If it has an `adapters`, `energy`,
`availability`, `house`, `device_classes` or `devices` section, statehouse
refuses to start and names the namespace each one belongs in. Those sections
used to be read locally and then silently replaced by the remote copy, so an
edit to the local file could have no effect at all.

## The namespaces

| Namespace | Sets | Example |
|---|---|---|
| `statehouse_behaviour` | `adapters`, `energy`, `availability`, `house` | `config/remote/statehouse_behaviour.json` |
| `statehouse_classes` | device classes | `config/remote/statehouse_classes.json` |
| the site's devices namespace, named by `site.devices_namespace` (e.g. `devices_home`) | devices | `config/remote/devices_example.json` |

The examples are tested (`internal/config/example_remote_test.go`): they apply
through the real fetcher, and carry no key statehouse would ignore.

## When it is read

- **At startup**, all three namespaces are fetched (30 s budget, token
  included). If any fetch fails, or the local file has no `remote_config` or
  `identity` block, statehouse exits non-zero. systemd restarts it until the
  config service is reachable. There is no local mode, and no cached copy:
  **statehouse cannot start or restart while the config service or identity
  is unreachable.** A running statehouse is unaffected by an outage; a
  restart during one (crash, deploy, host reboot) waits for it to end, and
  MQTT state, Influx writes and the HTTP API stop until then.
- **On SIGHUP** (`systemctl kill -s HUP statehouse`), all three are fetched
  again and applied to the running engine. If any fetch fails, or a document
  is refused, the running config is kept and the failure is logged.
- A `statehouse_behaviour` document whose `house.timezone` is not a tz
  database name is refused (at startup: statehouse exits; on SIGHUP: the
  running config is kept), rather than falling back to UTC.
- **Restart, not SIGHUP,** for: switching an adapter on or off, or changing its
  `base_topic` (adapters are built once, at startup), and any change to the
  local file.
- `/healthz` reports each namespace's last fetch under `remote_config`.

## How each namespace is applied

- **`statehouse_behaviour`**: each top-level section that is present
  (`adapters`, `energy`, `availability`, `house`) **replaces that section
  whole**. A section that is absent keeps statehouse's built-in defaults.
  Within a present section, a key you leave out does not fall back to its
  default: write every key you mean to set (see #86).
- **`statehouse_classes`**: each class entry is applied by name. A class
  missing from the namespace has no name hints or default thresholds.
- **devices**: each entry is applied by id, and checked with the shared
  `github.com/sweeney/common/devices` schema. Problems are logged as
  `remote config: devices problem`; one bad field costs that field, not the
  whole document.

## `statehouse_behaviour`

### `adapters`

Adapters translate protocol-specific MQTT traffic into engine calls. Each has
`enabled` and `base_topic`. A `base_topic` left out takes the default below.
An adapter left out of the block is off, except `zigbee2mqtt`, which is on
unless `enabled` is `false`.

| Adapter | Default `base_topic` | Subscribes to |
|---|---|---|
| `zigbee2mqtt` | `zigbee2mqtt` | `{base}/#` |
| `boiler` | `energy/boiler/sensor` | `{base}/events`, `{base}/system` (sweeney/boiler-sensor CH/HW relay state) |
| `ups` | `ups` | `{base}/+/state` (NUT via MQTT) |
| `climate` | `climate` | `{base}/#` (weather station observations) |
| `meter` | `energy` | `{base}/+/SENSOR/...` (Glow/SMETS2 smart meter) |
| `intercom` | `asterisk` | `{base}/#` (Asterisk via MQTT; in-flight calls as activity signals) |
| `tanktemp` | `energy/hot_water` | `{base}/+/state`, `{base}/status` (sweeney/tanktemp cylinder probes; one device per probe, scheme `tanktemp`, primary `<cylinder id>.<position>`; the cylinder and probe entries must be in the devices namespace first) |

### `energy`

| Key | Default | Meaning |
|---|---|---|
| `divergence_warning_pct` | `20` | Counter and integrated kWh for a cycle diverging by more than this raises `energy_divergence_warning`. |
| `max_integration_gap` | `30m` | The engine refuses to integrate power across a longer gap. |

### `availability`

| Key | Default | Meaning |
|---|---|---|
| `offline_debounce` | `30s` | How long a device must stay offline before it is reported offline. |

### `house`

| Key | Default | Meaning |
|---|---|---|
| `quiet_after` | `30m` | No activity for this long: the house is quiet. |
| `empty_after` | `6h` | Quiet this long with no sign of presence: empty. |
| `sleeping_after` | `2h` | Occupied and quiet this long: sleeping. `0` never enters sleeping. |
| `timezone` | UTC | tz database name (e.g. `Europe/London`, or `Local` for the host's zone) for the mode dimension's night/day hours. A name that does not resolve refuses the whole document. |

## `statehouse_classes`

Each class has optional `name_hints` (substrings of a device's name that
classify an unconfigured device), `default_thresholds`, `energy_strategy`
(`counter` or `integration`) and `staleness_seconds`.

- `short_burst_power_device`: kettles, toasters, microwaves.
- `cycle_power_device`: dishwashers, washing machines, dryers.
- `continuous_power_device`: fridges, freezers, dehumidifiers. Integration
  strategy, because compressor cycles are too short for a 10 Wh counter to
  tick.
- `media_power_device`: TVs, AV receivers, speakers.
- `binary_state_device`: state-only devices such as boiler relays and contact
  sensors. The sustained-for windows debounce short blips; a source that
  already debounces upstream (like boiler-sensor) can use `0s`.
- `environmental_sensor`: climate, air-quality and illuminance sensors.
  Measurement-only: no cycles; activity stays "reporting" while the device
  transmits.
- `ups_sensor`: measurement-only, with UPS fields (on battery, low battery,
  runtime).
- `energy_meter`: whole-home meters and IHDs. No cycles, no occupancy
  contribution.
- `fire_alarm`: smoke/heat detectors. Measurement-only, like
  `environmental_sensor`, with its own class only so safety alarms tag
  distinctly. Never a `binary_state_device`: that would feed occupancy and
  debounce the latched alarm re-broadcast.
- `appliance_probe`: temperature probes inside another appliance (fridge,
  freezer, hot water cylinder). Measurement-only and never ambient.

## Devices

Placement uses the floorplan vocabulary (`docs/floorplan-vocabulary.md`):

- `room`: the floorplan room id the device sits in, `<floor>.<room>`.
- `floor`: the floor it sits on, declared alongside `room`, never derived.
- `covers`: what its readings describe when that is not its own room: `house`
  for whole-house devices (the boiler channels, the electricity meter), or
  another room id. `house` is never a room.

Leave a field out when it is unknown. `location` is deprecated: it is still
decoded and read as the room when `room` is absent; don't use it in new
entries.

Identity is `scheme` + `primary`. Zigbee devices may use the legacy
`ieee_address` (and `friendly_name`) shorthand, which becomes scheme `zigbee`.
The meter adapter recognises the Glow meter whether or not it is listed;
listing it gives it a display name and a stable id.

### Per-device `energy_strategy`

Some Zigbee plugs report energy counters in coarse steps (10 Wh or 100 Wh).
When a device's cycle is smaller than a step, the counter never moves and the
engine raises `stale_counter` on every cycle. The signs: `warnings:
["stale_counter"]`, `cycle.energy.reported_kwh_delta == 0`, and
`cycle.energy.integrated_kwh > 0`. The fix is `"energy_strategy":
"integration"` on that device only; others in its class keep the class
default. Don't raise the stale-counter threshold instead: that would hide
genuinely broken counters.

```json
"office_heater": {
  "ieee_address": "0x00158d0000000004",
  "class": "cycle_power_device",
  "display_name": "Office heater",
  "room": "first.office",
  "floor": "first",
  "energy_strategy": "integration"
}
```
