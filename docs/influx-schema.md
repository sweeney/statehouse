# InfluxDB schema

Statehouse writes time-series data to InfluxDB as events occur. All points use the precise event timestamp. Writes are async and fire-and-forget — errors are logged and counted but don't affect the engine.

## Common tags

Most per-device measurements carry these tags:

| Tag | Example |
|---|---|
| `device_id` | `climate_basement`, `officeav` |
| `class` | `environmental_sensor`, `energy_meter`, `ups_sensor` |
| `location` | `basement`, `office`, `garden` |

---

## Measurements

### `device_power`

Emitted whenever a device reports power draw.

| Field | Type |
|---|---|
| `power_w` | float |
| `voltage_v` | float |
| `energy_kwh` | float |

### `device_environment`

Emitted for any environmental reading. Only fields present in the event are written.

| Field | Type |
|---|---|
| `temperature_c` | float |
| `humidity_pct` | float |
| `pressure_hpa` | float |
| `wind_speed_ms` | float |
| `wind_dir_deg` | float |
| `rainfall_mm` | float |
| `illuminance_lux` | float |
| `uv_index` | float |

### `device_battery`

| Field | Type |
|---|---|
| `battery_pct` | float |

### `device_ups`

| Field | Type |
|---|---|
| `battery_runtime_mins` | float |
| `on_battery` | bool |
| `low_battery` | bool |

### `device_alarm`

Emitted for safety alarms (smoke/heat detectors, class `fire_alarm`). Latched binary signals; written on each report that carries the field. A missing field is never written as `false` (partial per-cluster payloads are common).

| Field | Type |
|---|---|
| `smoke` | bool |
| `tamper` | bool |

### `device_radio`

| Field | Type |
|---|---|
| `rssi_dbm` | int |

### `house_electricity`

Whole-house electricity aggregation. Written on each meter tick (~every 30s). Uses a synthetic device so has no `device_id`/`class`/`location` tags — only `scope="whole_house"`.

| Field | Type | Notes |
|---|---|---|
| `gross_w` | float | Total consumption from meter |
| `monitored_w` | float | Sum of known monitored devices |
| `unmonitored_w` | float | gross − monitored |
| `coverage` | float | monitored / gross (0–1) |
| `today_kwh` | float | Authoritative meter import for the local day (meter resets at local midnight). Absent until a meter reading is seen. |
| `week_kwh` | float | Authoritative meter import for the local week |
| `month_kwh` | float | Authoritative meter import for the local month |
| `session_gross_kwh` | float | Service-lifetime integration of gross power; resets on service restart (a function of uptime, not a true house total) |
| `session_monitored_kwh` | float | Service-lifetime integration of monitored power |
| `session_unmonitored_kwh` | float | Service-lifetime integration of unmonitored power |
| `stale_device_count` | float | Devices excluded due to stale readings |

---

## Derived measurements

These are written when the engine emits higher-level events, not raw sensor data.

### `device_activity`

Written when a device transitions between activity states (e.g. `unknown` → `reporting`, `idle` → `active`).

Tags: `device_id`, `class`

| Field | Type |
|---|---|
| `from` | string |
| `to` | string |

### `appliance_cycle`

Written when an appliance cycle completes (e.g. washing machine finishes). Dropped if the cycle has no evidence. Tags: `device_id`, `class`, `location`.

Fields come from the cycle's evidence map — whatever scalar values (int, float, bool, string) were recorded. Typically includes `duration_seconds`, `selected_energy_kwh`, `integrated_kwh`.

### `house_state`

Written when the whole-house state changes. The state dimensions are stored as tags so they can be filtered/grouped easily.

Tags: `occupancy`, `activity`, `mode`

| Field | Type |
|---|---|
| `occupancy_confidence` | float (0–1) |
| `activity_confidence` | float (0–1) |
| `mode_confidence` | float (0–1) |

---

## Active devices (as of June 2026)

| Device | Class | Location | Measurements |
|---|---|---|---|
| `climate_basement` | environmental_sensor | basement | environment, battery, activity |
| `climate_firstfloor` | environmental_sensor | first_floor | environment, battery, activity |
| `climate_weatherstation` | environmental_sensor | garden | environment, radio |
| `glowsensorth1` | environmental_sensor | network_cabinet | environment, battery, radio |
| `electricity_meter` | energy_meter | house | power |
| `officeav` | media_power_device | office | power |
| `network-ups` | ups_sensor | network_cabinet | power, ups, battery |
| `office-ups` | ups_sensor | office | power, ups, battery |
| `_` (synthetic) | — | — | house_electricity |
