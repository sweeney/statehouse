# `firealarm_office` — Zigbee Smoke Detector MQTT Event Report

Live capture of all MQTT events emitted by the office smoke detector via
`zigbee2mqtt`, including a full **alarm lifecycle** (`alarm_1` false → true →
false) triggered during the session. A follow-up **45-minute idle-cadence study**
across three identical units (`firealarm_office`, `firealarm_network`,
`firealarm_utility`) is in §7.

- **Captured:** 2026-06-11, alarm lifecycle ~10:38–10:56; cadence study ~11:30–12:16 (host local time, **UTC+1**)
- **Broker:** `192.168.1.200:1883` (Mosquitto)
- **Tool:** `mosquitto_sub` subscribed to `zigbee2mqtt/#` then narrowed to the device topic
- **Bridge:** `zigbee2mqtt` online; Zigbee channel 11, PAN id 6754, `last_seen` format = `epoch` (ms)

> **Timestamps:** the `HH:MM:SS` prefix on each line is the **host local clock (UTC+1)**.
> The `last_seen` field inside each payload is the device's own report time as a
> **Unix epoch in milliseconds, UTC**. They agree: e.g. `last_seen:1781171471893`
> = `09:51:11.893 UTC` = `10:51:11` local.

---

## 1. Device identity

The detailed alarm-lifecycle capture (§4–§6) is for **`firealarm_office`**. Two
more identical units were later added and included in the cadence study (§7).

| Property | Value |
|---|---|
| Friendly name | **`firealarm_office`** (note spelling: "fire**a**larm") |
| IEEE address | `0xb0e8e8fffe66d945` |
| Model | **HS1SA-E-PLUS** |
| Vendor | **HEIMAN** |
| Type | Zigbee photoelectric **smoke detector** |
| Power source | **Battery** |
| z2m definition | *Automatically generated* (no built-in definition — attributes inferred) |

**All three units (same model — HEIMAN HS1SA-E-PLUS, battery):**

| Friendly name | IEEE address |
|---|---|
| `firealarm_office` | `0xb0e8e8fffe66d945` |
| `firealarm_network` | `0xb0e8e8fffe66e767` |
| `firealarm_utility` | `0xcc36bbfffed90f31` |

---

## 2. Exposed attributes

From the `zigbee2mqtt/bridge/devices` payload. `access`: `1`=published/readable,
`2`=settable, `5`=readable+reportable.

| Property | Type | Access | Notes |
|---|---|---|---|
| `alarm_1` | binary | 1 | **Primary smoke/heat alarm** — the key signal |
| `alarm_2` | binary | 1 | Secondary alarm channel (stayed `false`) |
| `tamper` | binary | 1 | Tamper switch (stayed `false`) |
| `battery_low` | binary | 1 | Low-battery flag |
| `battery` | numeric | 5 | Battery percentage |
| `temperature` | numeric | 5 | Onboard temperature sensor, °C |
| `linkquality` | numeric | 1 | Zigbee LQI, 0–255 |
| `warning` | composite | 2 | **Settable** siren control (see below) |

**`warning` composite (settable — siren/strobe control, not observed in this session):**

| Sub-feature | Type | Values |
|---|---|---|
| `mode` | enum | `stop`, `burglar`, `fire`, `emergency`, `police_panic`, `fire_panic`, `emergency_panic` |
| `level` | enum | `low`, `medium`, `high`, `very_high` |
| `strobe_level` | enum | `low`, `medium`, `high`, `very_high` |
| `strobe` | binary | — |
| `strobe_duty_cycle` | numeric | — |
| `duration` | numeric | seconds |

---

## 3. MQTT topics

| Topic | Direction | Purpose |
|---|---|---|
| `zigbee2mqtt/firealarm_office` | device → | State object (all attributes above). **This is what to watch.** |
| `zigbee2mqtt/firealarm_office/availability` | z2m → | `{"state":"online"\|"offline"}` — z2m availability tracking (retained), **not** a device data report |
| `zigbee2mqtt/firealarm_office/set` | → device | Send `warning` commands here to drive the siren |

---

## 4. Event lifecycle summary

```
alarm_1:  false ───────────────► true ──(latched, re-broadcast)──► false
                10:51:11                                         10:55:39
                (rising edge)                                    (falling edge)
                └──────────────── ~4 min 28 s on MQTT ───────────────┘
```

| Transition | Local time | `last_seen` (epoch ms, UTC) | Trigger |
|---|---|---|---|
| `alarm_1` **false → true** | **10:51:11** | `1781171471893` | Real smoke/heat stimulus |
| `alarm_1` **true → false** | **10:55:39** | `1781171739626` | Detector self-reset after clear |

**Active duration (MQTT):** `1781171739626 − 1781171471893 = 267 733 ms ≈ **4 min 27.7 s***.
The alarm persisted **~80 s after the local sounder was silenced** before the
Zigbee state reset.

> **Note:** an earlier plain **test-button press produced _no_ MQTT message at all** —
> on this HEIMAN model the test button only drives the local buzzer and does not
> transmit `alarm_1` over Zigbee. Only a real detection event set `alarm_1: true`.

---

## 5. Full raw event log

All payloads are on topic `zigbee2mqtt/firealarm_office` unless noted. Where two
near-identical lines share a timestamp, they are the device's normal Zigbee
**attribute double-send** (differ only by `linkquality`/`last_seen` by a few ms) —
one logical event, not two.

### 5.1 Availability (retained, at subscribe)
```
10:38:19  zigbee2mqtt/firealarm_office/availability  {"state":"online"}
```

### 5.2 Idle reports — baseline state
```
10:44:45  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171085889,"linkquality":152,"tamper":false,"temperature":22.43}
10:44:45  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171085913,"linkquality":148,"tamper":false,"temperature":22.43}
10:44:49  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171089933,"linkquality":152,"tamper":false,"temperature":22.43}
10:44:49  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171089941,"linkquality":152,"tamper":false,"temperature":22.43}
10:44:51  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171091956,"linkquality":148,"tamper":false,"temperature":22.43}
10:45:11  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171111845,"linkquality":148,"tamper":false,"temperature":22.43}
10:45:12  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171112956,"linkquality":148,"tamper":false,"temperature":22.43}
10:45:53  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171153955,"linkquality":148,"tamper":false,"temperature":22.43}
```

### 5.3 Temperature change (first substantive change: 22.43 → 23.57 °C)
```
10:48:10  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171290659,"linkquality":148,"tamper":false,"temperature":23.57}
```

### 5.4 ALARM RAISED — `alarm_1` false → true  ⚠️
```
10:51:11  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171471893,"linkquality":160,"tamper":false,"temperature":23.57}
10:51:11  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171471902,"linkquality":156,"tamper":false,"temperature":23.57}
```

### 5.5 Alarm latched — repeated `alarm_1: true` re-broadcasts
```
10:51:18  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171478007,"linkquality":160,"tamper":false,"temperature":23.57}
10:51:20  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171480762,"linkquality":156,"tamper":false,"temperature":23.57}
10:51:31  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171491397,"linkquality":156,"tamper":false,"temperature":23.57}
10:53:13  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171593378,"linkquality":156,"tamper":false,"temperature":23.57}
10:53:29  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171609801,"linkquality":156,"tamper":false,"temperature":23.57}
10:53:34  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171614967,"linkquality":156,"tamper":false,"temperature":23.57}
10:54:19  {"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171659274,"linkquality":156,"tamper":false,"temperature":23.57}
```
*(Local sounder was silenced manually somewhere in this window; MQTT continued to report `true`.)*

### 5.6 ALARM CLEARED — `alarm_1` true → false  ✅
```
10:55:39  {"alarm_1":false,"alarm_2":false,"battery":100,"battery_low":false,"last_seen":1781171739626,"linkquality":160,"tamper":false,"temperature":23.57}
```

---

## 6. Attribute observations over the session

### Battery
- `battery`: **100 %** constant; `battery_low`: **false** throughout. Healthy.

### Temperature (onboard sensor, °C)
| Local time | `temperature` |
|---|---|
| 10:44:45 – 10:45:53 | `22.43` |
| 10:48:10 onward | `23.57` |

A single ~**+1.1 °C** step (ambient drift); reported only on change, not on a fixed interval.

### Link quality (Zigbee LQI, 0–255)
- Ranged **148 – 160** across the session (jittered every report). Healthy mid-range; no dropouts. Excluded from "substantive change" detection because it changes on nearly every packet.

### Alarm channels / tamper
- `alarm_2`: **false** the entire session (secondary channel never engaged).
- `tamper`: **false** the entire session.
- `alarm_1`: the only alarm channel that fired — see §5.4–5.6.

---

## 7. Idle reporting cadence (measured — 45-min raw capture, all 3 units)

**Goal:** how often does a unit emit on MQTT *outside* an alarm? **Method:** raw,
un-deduplicated `mosquitto_sub` on each device's data topic for 45 min (~11:30–12:16),
timestamped; multi-sends (a report re-transmitted within 10 s) collapsed into one
logical report; intervals computed from the device-side `last_seen` (epoch ms).

### Results

| Unit | Logical reports / 45 min | Interval min / median / max | Temp behaviour in window |
|---|---|---|---|
| `firealarm_utility` | **6** | 3.85 / 5.34 / 9.89 min | falling steadily 23.57 → 18.06 °C |
| `firealarm_office` | **2** | — / 11.54 / — min (n=1 interval) | stable 22.8 °C |
| `firealarm_network` | **0** | n/a — **silent for the full 45 min** | (no reports) |

Battery `100 %` and `battery_low false` on every report; no alarm/tamper during the study.

### `firealarm_utility` — logical report timeline
```
11:33:01  temp=23.57C  lq=160
11:38:22  temp=22.43C  lq=128   (+5.34 min)
11:42:12  temp=21.30C  lq=132   (+3.85 min)
11:46:36  temp=20.21C  lq=132   (+4.40 min)
11:52:38  temp=19.14C  lq=132   (+6.04 min)
12:02:32  temp=18.06C  lq=132   (+9.89 min)
```

### `firealarm_office` — logical report timeline
```
11:47:58  temp=22.8C  lq=160
11:59:30  temp=22.8C  lq=156    (+11.54 min)
```
(Plus ~16 min of silence before the first report and ~17 min after the last,
within the 45-min window.)

### Interpretation — reporting is change-driven, **not** a fixed heartbeat

The decisive signal is `firealarm_utility`'s temperature column: it drops almost
exactly **~1.1 °C per report** (23.57 → 22.43 → 21.30 → 20.21 → 19.14 → 18.06).
That is a **reportable-change threshold on the temperature attribute (~1 °C)** acting
as the dominant trigger. Consequently:

- **Temp actively changing → frequent reports.** `utility` (cooling ~5.5 °C over the
  window) reported every **~4–10 min**, paced by how fast temperature crossed each ~1 °C step.
- **Temp stable → sparse reports.** `office` (flat 22.8 °C) emitted only a slow
  keep-alive-style report (~11.5 min apart, with 16+ min silences).
- **Nothing changing → no reports.** `network` produced **zero** publishes in 45 min.

**Takeaway:** there is **no short fixed heartbeat**. Expect long, irregular gaps —
**10 min to 45+ min (and likely much longer)** — for a thermally stable detector, and
near-continuous reports only while ambient temperature is moving. Confirms the
">30-min reporting" suspicion: it is real and is a normal consequence of the
change-based reporting model, not a fault.

> **Caveats:** small samples (`office` n=1 interval, `network` n=0). Treat the
> per-unit interval numbers as indicative, not definitive. A unit being silent does
> **not** mean it is offline (see §7.1). If you need a guaranteed periodic check-in,
> configure z2m **active availability** polling for these devices rather than relying
> on spontaneous reports.

### 7.1 Following up the silent unit (`firealarm_network`)

`firealarm_network` (`0xb0e8e8fffe66e767`) emitted **nothing** in the 45-min window,
which raised "is it even working?". Investigation:

**Bridge metadata is identical across all three — it does not distinguish a chatty
unit from a silent one:**

| Field | `firealarm_office` | `firealarm_network` | `firealarm_utility` |
|---|---|---|---|
| `interview_completed` | True | **True** | True |
| `type` | EndDevice | **EndDevice** | EndDevice |
| `network_address` | 61130 | **62455 (0xf3f7)** | 30798 |
| `supported` | False | **False** | False |
| `availability` | online | **online** | online |
| `last_seen` (bridge record) | **None** | **None** | **None** |

Two traps to avoid:

- **`last_seen` in `bridge/devices` is `None` for ALL three** (even the units actively
  reporting) — in this z2m setup it is **not** a "have we heard from it" signal. Don't
  use it to judge liveness. (The per-message `last_seen` *inside* each published payload
  is the real report time; that only exists once the device actually reports.)
- **`availability: online` is weak evidence.** These devices use z2m **passive**
  availability (timeout **1500 min ≈ 25 h**). A recently-added unit reads `online`
  until ~25 h of total silence, regardless of current reachability.

**Decisive live test — physically stimulate, then watch.** The test button does *not*
help (it never transmits, see §8.1). Instead, warm the sensor (cupped hands / breath)
or move it, and watch its topic (test button never transmits — §8 #1).
`firealarm_network` responded within seconds with a fresh report:

```json
13:05:41  zigbee2mqtt/firealarm_network  {"battery":100,"last_seen":1781179541031,"linkquality":120,"temperature":21.3}
```

**Conclusion:** the unit is healthy, joined, and reachable — it was silent only because
it was thermally stable (nothing to report) **and** it has the weakest radio link of the
three (`linkquality` ~**120** vs office/utility ~**128–160**). Lower LQI + a stable
environment = the fewest change-triggered reports. A mains-powered Zigbee **router
(repeater) nearer to it** would raise its LQI and reliability.

---

## 8. Key findings & gotchas

1. **Test button is local-only.** A plain test-button press sounded the buzzer but
   emitted **nothing** on MQTT. Only a genuine smoke/heat detection drove
   `alarm_1: true`. Don't rely on the test button to validate the MQTT/automation path.
2. **Silencing ≠ clearing.** The MQTT `alarm_1` state stayed `true` for ~80 s
   *after* the audible alarm was silenced, resetting only when the detector itself
   cleared. **Automations should trust `alarm_1`, not the sounder.**
3. **Alarm is latched and re-broadcast.** While active, the device re-sends
   `alarm_1: true` every few seconds. For edge-triggered logic, detect the
   `false→true` rising edge; for "all clear", detect the `true→false` falling edge.
4. **Multi-send is normal.** A single logical report usually arrives as **two or
   three** near-identical publishes within ~1–2 s, differing only in
   `linkquality`/`last_seen` (e.g. office `11:59:30/31/32`, utility `11:33:01` ×3).
   Collapse publishes within ~10 s into one event.
5. **Partial / per-cluster payloads exist.** Some publishes carry only a subset of
   keys — e.g. `{"battery":…,"temperature":…,"linkquality":…}` with **no `alarm_*`
   or `tamper`** (the power-config / temperature clusters reporting on their own,
   separate from the IAS alarm cluster). **Automations must key off `alarm_1`
   explicitly and tolerate its absence**, not assume every message is full state.
6. **`availability` is z2m-generated** (not a device report) and **weak evidence of
   liveness** here: these units use *passive* availability (timeout ~25 h), so a
   recently-added unit reads `online` regardless of current reachability. Use it for
   alarm-state? Never — only `alarm_1`.
7. **`bridge/devices.last_seen` is `None` for all units** in this setup — not a
   liveness signal. The real report time is the per-message `last_seen` in the payload.
8. **Idle reporting is change-driven, not periodic** (see §7). A thermally stable
   unit can be silent for 30–45+ min; **silence ≠ offline.** To verify a quiet unit,
   physically warm/move it and watch its topic (see §7.1) — the test button won't do
   it. For guaranteed check-ins, enable z2m active availability polling.
9. **Link quality varies per unit.** Observed `linkquality`: office/utility ~128–160,
   `network` ~120 (weakest). Lower LQI + a stable environment ⇒ the fewest reports.
   A mains-powered Zigbee router near a weak unit improves both LQI and reliability.

---

## 9. Reusable monitoring commands

> Swap `firealarm_office` for `firealarm_network` / `firealarm_utility` as needed.

**Watch all three units at once (raw, timestamped):**
```sh
mosquitto_sub -h 192.168.1.200 -v \
  -t 'zigbee2mqtt/firealarm_office' \
  -t 'zigbee2mqtt/firealarm_network' \
  -t 'zigbee2mqtt/firealarm_utility' \
  | while IFS= read -r l; do printf '%s %s\n' "$(date '+%H:%M:%S')" "$l"; done
```

**Reachability test for a silent unit** — run this, then go **warm/move** the
detector; a fresh report within seconds proves it's alive (test button won't work):
```sh
mosquitto_sub -h 192.168.1.200 -v -t 'zigbee2mqtt/firealarm_network'
```

**Watch every state publish (raw):**
```sh
mosquitto_sub -h 192.168.1.200 -v -t 'zigbee2mqtt/firealarm_office'
```

**Watch only substantive changes (ignore `last_seen`/`linkquality` jitter), timestamped:**
```sh
mosquitto_sub -h 192.168.1.200 -t 'zigbee2mqtt/firealarm_office' | while IFS= read -r l; do
  key=$(printf '%s' "$l" | sed -E 's/"last_seen":[0-9]+,?//; s/"linkquality":[0-9]+,?//')
  if [ "$key" != "$prev" ]; then prev="$key"; printf '%s %s\n' "$(date '+%H:%M:%S')" "$l"; fi
done
```

**Alarm rising edge (fire detected):**
```sh
mosquitto_sub -h 192.168.1.200 -t 'zigbee2mqtt/firealarm_office' | while IFS= read -r l; do
  a1=0; case "$l" in *'"alarm_1":true'*) a1=1;; esac
  [ "$a1" = 1 ] && [ "${p:-0}" = 0 ] && printf '%s ALARM RAISED %s\n' "$(date '+%H:%M:%S')" "$l"
  p=$a1
done
```

**Alarm falling edge (cleared):**
```sh
mosquitto_sub -h 192.168.1.200 -t 'zigbee2mqtt/firealarm_office' | while IFS= read -r l; do
  a1=0; case "$l" in *'"alarm_1":true'*) a1=1;; esac
  [ "${p:-}" = 1 ] && [ "$a1" = 0 ] && printf '%s ALARM CLEARED %s\n' "$(date '+%H:%M:%S')" "$l"
  p=$a1
done
```

**Trigger the siren (settable `warning` composite):**
```sh
mosquitto_pub -h 192.168.1.200 -t 'zigbee2mqtt/firealarm_office/set' \
  -m '{"warning":{"mode":"fire","level":"very_high","strobe":true,"duration":10}}'
```
