# Climate sensors as an activity source — assessment and options

**Status:** assessment only. Nothing in this document is implemented.
**Question:** can indoor temperature/humidity sensors placed per-room on the
floorplan produce meaningful house events (a shower in the ensuite, a window
left open, cooking in the kitchen), and how would that signal processing differ
from everything statehouse does today?

**Short answer:** yes, and the useful version is cheaper than it looks — but it
cannot be built out of the idiom the engine currently uses, because that idiom
is level-triggered on an instantaneous magnitude and this signal lives in the
*rate of change of a derived quantity relative to a slow baseline*. The single
highest-value change is not a detector at all: it is converting relative
humidity to **absolute humidity** before doing anything else.

---

## 1. What statehouse does today

Worth stating precisely, because the gap is specific rather than general.

### 1.1 The detection idiom is level-triggered with hysteresis

Every activity decision in the engine has the same shape
(`internal/device/statemachine.go`):

> compare *this reading's* magnitude to a configured threshold; if it has been
> on the other side of that threshold continuously for `active_sustained_for`,
> transition.

`Runtime` holds exactly one pending sample — `candidate *candidateSample`
(`statemachine.go:75`) — which is a debounce latch, not a window. There is no
history, no derivative, no baseline. `Thresholds` is
`{idle_below_w, active_above_w, compressor_above_w, active_sustained_for,
inactive_sustained_for}` and every one of those is a watts comparison.

This is the right design for its job. A kettle draws 3 kW or it draws 0 W; the
signal is enormous relative to the noise, the threshold is stable for the life
of the appliance, and hysteresis is all you need to stop it flapping.

### 1.2 Climate sensors are deliberately inert

They are `environmental_sensor` (or `fire_alarm`, `ups_sensor`,
`energy_meter`), and `IsPassiveSensor` (`internal/device/profiles.go:59`)
switches them out of the machine in three separate places:

| Place | Effect |
|---|---|
| `statemachine.go:137` | Any reading sets `activity = reporting` once, then nothing. `out.Cycle = nil`. |
| `house.go:14` `isOccupancyRelevant` | Excluded from occupancy entirely. |
| `house.go:38` `isIdleDeviceState` | `reporting` is classed as idle, so it never counts toward the house activity dimension. |

So today a climate sensor's readings land in `Device.Latest`, ratchet
`Device.Lifetime` min/max, get written to Influx as `device_environment`, and
influence *nothing*. They are a data-recording path, not a state path.

### 1.3 There is no history anywhere in the process

This is the constraint that shapes every option below. Per device the engine
retains:

- `Latest` — the last value of each field.
- `Lifetime` — all-time min/max with timestamps, explicitly not persisted.

That is it. Influx is a fire-and-forget **write** path; nothing reads back from
it, and the service is required to start and run with Influx unavailable. The
engine has no notion of "the last 40 minutes of this sensor".

### 1.4 Two pathways already exist that this work would use

Both were built for other reasons and both fit.

**`ActivitySignal` / `ActivityRecord`** (`internal/model/signal.go`,
`activity_record.go`) — a first-class non-device presence assertion, built for
the intercom adapter. It already carries `Location` (a room/zone hint),
`Confidence`, `Since` **separate from** `Timestamp`, a TTL, and a `Meta` bag.
Signals feed `DeriveHouseState` directly and count toward occupancy and the
activity dimension. `ActivityRecord` persists in a 50-entry ring after the
activity ends.

That `Since`/`Timestamp` split matters more than it looks — see §7.1.

**`CanonicalSink`** (`internal/state/engine.go:31`) — every reading is fanned
out field-by-field as `CanonicalEvent`s before being applied to state
(`engine.go:206`), including `temperature_c` and `humidity_pct` with device id,
identity and timestamp. Anything registered here sees the complete climate
stream with no engine changes.

**And the intent is already recorded.** `DeriveHouseState`'s TODO
(`house.go:55`) lists the overlays this work is a prerequisite for:
`morning_routine`, `evening_wind_down`, `meal_preparation`, `laundry_day`,
`overnight_quiet`.

---

## 2. Why the current idiom cannot detect a shower

Set `humidity_above_pct: 75, sustained_for: 5m` on the ensuite sensor and it
will not work. Three independent reasons:

**Relative humidity is not a measure of moisture.** RH is moisture *relative to
what the air could hold at its current temperature*, and that capacity roughly
doubles every 11°C. Turn a radiator on and RH falls with no water going
anywhere. This confounds the exact two variables we are reading.

**The absolute threshold has no stable value.** An ensuite baseline swings
20+ RH points across the year. A threshold that catches a July shower fires
continuously through a damp November; one that is quiet in November never
triggers in July.

**High humidity is a state, not an event.** The interesting fact is not "it is
humid in the ensuite", it is "it became humid in the ensuite in eight minutes".
Level-triggering discards the derivative, which is the entire signal.

---

## 3. The change that does most of the work: absolute humidity

Convert `(T, RH)` to **absolute humidity** in g/m³ (or equivalently dew point)
via the Magnus formula. Roughly ten lines, no state, no history, no
configuration:

```
es(T)     = 6.112 · exp(17.62·T / (243.12 + T))      # saturation vapour pressure, hPa
e(T,RH)   = RH/100 · es(T)                           # actual vapour pressure, hPa
AH(T,RH)  = 216.74 · e / (273.15 + T)                # g/m³
Td(T,RH)  = 243.12·γ / (17.62 − γ),  γ = ln(RH/100) + 17.62·T/(243.12+T)
```

AH moves only when water is actually added to or removed from the air. What
that buys, on real numbers:

| Scenario | T | RH | **AH (g/m³)** | Dew pt |
|---|---|---|---|---|
| Ensuite baseline | 20.0 °C | 50 % | **8.62** | 9.3 °C |
| *Same air*, heated 3 °C — no moisture added | 23.0 °C | 41.6 % | **8.53** | 9.2 °C |
| Mid-shower | 22.0 °C | 85 % | **16.46** | 19.4 °C |
| Muggy summer day, indoors | 20.0 °C | 75 % | **12.93** | 15.4 °C |
| Outdoor, winter (from `climate_readings.jsonl`) | 7.7 °C | 92.8 % | **7.52** | 6.6 °C |

Read the rows in pairs:

- **Rows 1–2:** heating the room drops RH by 8.4 points for zero moisture
  change. AH barely moves. Any RH-based detector reads that as an event; an
  AH-based one correctly reads it as nothing.
- **Row 3 vs 4:** a shower and a muggy day are both "high RH", but the shower
  is 27 % further up in actual water content — and it gets there in minutes
  rather than hours.
- **Row 5:** outdoor air at 92.8 % RH holds *less* water than a 50 % RH living
  room. This is why differencing indoor against the weather station has to be
  done in AH — doing it in RH gives you the wrong sign for half the year.

We already have every input: `Reading.TemperatureC` and `Reading.HumidityPct`
are populated by both the Z2M adapter (`payload.go:28`) and the weather-station
adapter. This is a pure function of one reading and can be added on its own,
before any detector exists, purely to make the Influx/Grafana series honest.

---

## 4. Options for the detection maths

A ladder. Each rung subsumes the one before.

### Option A — Threshold + hysteresis on the raw value
Reuse `Thresholds` with humidity fields; add a state machine case beside
`stepBinary`.
**For:** no new machinery, no new memory, deterministic, works with the
existing fixture replay oracle unchanged.
**Against:** fails for all three reasons in §2. Would produce a detector that
demos convincingly and then misfires every winter.
**Verdict:** not viable on its own. Viable *after* §3, on AH rather than RH, as
a crude "is this room damp" flag — which is a genuinely useful and completely
different feature (see §6, condensation risk).

### Option B — Rate of change
Time-normalised derivative over a short window: `dAH/dt > X g/m³/min` sustained
for Y. A shower produces a rise that ordinary weather physically cannot.
**For:** this is the minimum that actually works. Immune to seasonal baseline
drift because a derivative discards the constant term.
**Against:** needs bounded per-sensor history (new). Very sensitive to the
sampling problem in §7.2 — must be per-unit-time, never per-sample.

### Option C — Slow baseline + relative excursion
Track a slow per-sensor baseline (EWMA, half-life ~6–24 h, or a rolling
median), flag `current − baseline > k·σ`. Combined with B this is effectively a
band-pass filter: fast signal measured against slow reference.
**For:** O(1) memory — two floats per sensor per metric. Handles seasonality
with no configuration. Adapts automatically to a sensor being moved or a room
changing use.
**Against:** a long enough event contaminates its own baseline (freeze baseline
updates while an excursion is open). Cold-start needs a warm-up period.
**Verdict:** B + C together is the recommended core.

### Option D — Episode segmentation and shape classification
Don't emit on threshold crossing — segment the excursion into an episode with
onset / peak / decay, then classify on shape features: rise rate, peak
amplitude, decay time constant, whether temperature co-moved and by how much.

This is where the distinctions the question actually asked for become
available:

| | AH rise | Rise time | T co-move | Decay |
|---|---|---|---|---|
| Shower | large | fast (5–10 min) | +2–3 °C | 30–60 min, faster with extractor |
| Bath | large | slow (15–25 min) | +2–4 °C | long plateau then slow |
| Cooking | large | medium | **+4–8 °C** | fast if extractor |
| Window opened (winter) | **negative** | fast | **negative** | until closed |
| Drying laundry | moderate | very slow | ~0 | hours |

**For:** produces genuinely explainable events with duration and confidence,
which is what success criterion #8 demands. Shower-vs-bath falls out of shape
rather than needing a separate mechanism.
**Against:** needs a real ring buffer (not just EWMAs), and needs labelled
ground truth to tune. This is the main reason for the data ask in §8.

### Option E — Cross-sensor corroboration
The largest accuracy win available, and it is nearly free because statehouse
*already has the corroborating signals*:

- **Boiler hot-water relay.** Already an adapter, already a
  `binary_state_device` (`scheme: boiler, primary: hw`). Ensuite AH excursion
  **and** HW demand in the same window is close to definitive for a shower or
  bath, and separates it from a humid day with near-certainty.
- **The outdoor weather station.** Already ingested (`climate_weatherstation`,
  garden). Subtracting outdoor AH removes the weather-driven common mode that
  otherwise lifts every indoor sensor at once.
- **The other indoor sensors.** A room-local excursion against the house median
  is a much stronger claim than a bare excursion. A whole-house rise is
  weather; one room rising alone is an event in that room.
- **Extractor fan / bathroom light**, if either is on a monitored plug.

**Verdict:** this is the argument for doing this inside statehouse rather than
as a standalone detector. A generic tool sees one humidity series. Statehouse
sees the humidity, the hot water, the outdoors and the rest of the house on one
clock.

### Option F — Formal change-point detection (CUSUM, BOCPD)
CUSUM on the AH series is a principled, fully deterministic, explainable
change-point detector. It is not machine learning and carries no model
artifact.
**Verdict:** worth keeping in view as a refinement of B if hand-tuned
thresholds prove brittle. Not needed for a first version.

### Option G — Machine learning / HMM over room states
**Verdict:** recommend against for now, and the reasons are the project's own.
"AI/LLM reasoning" and "long-term behavioural prediction" are explicit V1
non-goals; criterion #8 requires every inference be traceable to evidence; and
criterion #7 makes deterministic fixture replay *the* correctness oracle. An
HMM would survive #7 but a trained classifier introduces a versioned model
artifact that has to be shipped, replayed and explained. B+C+D+E gets most of
the accuracy with none of that cost. Revisit only if the deterministic version
demonstrably plateaus.

**Recommended maths: §3 (AH) + B + C, with D for shape and E for confidence.**

---

## 5. Options for where it lives

### P1 — Extend the device state machine
A new class (`climate_event_sensor`) or humidity thresholds on
`environmental_sensor`.
**For:** most idiomatic, config-driven, nothing new to learn.
**Against:** `IsPassiveSensor` is load-bearing in three places and passive
sensors are excluded from occupancy *on purpose*. Making climate sensors active
wholesale risks the house reading "occupied" whenever it is humid — the exact
false-positive that matters most. Also a poor structural fit: the machine is
built around one device's magnitude, and Options C/D/E all need cross-device
context it has no access to.

### P2 — A detector layer fed by canonical events *(recommended)*
A new `internal/climate` (or `internal/sense`) package registered via
`AddCanonicalSink`. It receives every `temperature_c`/`humidity_pct` event,
holds the ring buffers and baselines, and emits `ActivitySignal` +
`ActivityRecord` + `DerivedEvent` through the existing pathways.

**For:**
- Zero changes to the device state machine; no risk to any existing class.
- `ActivitySignal.Location` is already a room hint, `Confidence` is already
  there, and `Since`/`Timestamp` already separate onset from detection.
- Signals already feed `DeriveHouseState`, so a detected shower contributes to
  occupancy without touching house logic.
- Fully testable under the existing fixture-replay oracle.
- Cleanly opt-in and independently disableable, like every other adapter.

**Two things to check during implementation:**
1. `CanonicalEvent` carries `DeviceID` but **not** `Room`. The detector needs a
   room lookup — inject a `func(deviceID) string` over `store.Profiles()`
   rather than reaching into the store.
2. Calling `IngestSignal` from inside a canonical-sink callback re-enters the
   engine (`IngestSignal` → `RecomputeHouse` → `store.Devices()`). It looks
   safe today because `emitCanonicalForReading` runs before
   `store.withEntry` and releases `e.mu` before dispatching — but that is an
   ordering accident, not a guarantee. Prefer buffering the decision and
   emitting from `Tick()`, which already runs every 5 s and is the established
   home for time-driven transitions.

### P3 — First-class room state
Introduce `model.Room` and aggregate device state per floorplan room. The
question — sensors "placed throughout the house on the floor plan" — is
room-shaped, `Device.Room` / `Device.Covers` already exist, and rooms already
resolve at read time.
**Against:** "full room-level aggregation" is an explicit V1 non-goal, and this
is a large surface change (model, API, MQTT topics, OpenAPI spec).
**Verdict:** the right strategic direction, and P2 is the increment that earns
it. Room-scoped `ActivitySignal`s are the data P3 would aggregate; build them
first and let the shape of the room model be informed by real signals rather
than guessed at.

---

## 6. Options for history and state

The detector needs memory the engine currently does not have.

| | Memory | Survives restart | Enables |
|---|---|---|---|
| **(a) Bounded in-memory ring**, 6–24 h/sensor | ~1440 float pairs/sensor at 1/min ≈ tens of KB total | No | B, C, D |
| **(b) EWMA baselines only** | 2 floats/sensor/metric | No | B, C |
| **(c) Influx warm-start** on boot | as (a) | Yes | B, C, D |
| **(d) Baseline snapshot to disk** | tiny | Yes | B, C |

**Recommendation: (a) + (b), with (d) as a cheap refinement.**

Reject **(c) as a hard dependency** outright: statehouse must start and run with
Influx down, and nothing currently reads from it. Turning Influx into a
startup read dependency would invert one of the project's operational
guarantees for a marginal gain. It is defensible only as a strictly
best-effort warm-up that fails open to (a) — and even then it is the one
option that breaks deterministic fixture replay, because replay output would
depend on database contents. Probably not worth it.

The honest consequence of (a)/(b) is a **warm-up period after every restart**
during which baselines are unconverged. Handle it explicitly: emit signals with
reduced confidence, or suppress them entirely, until each sensor has N hours of
observations. Do not silently emit low-quality events — criterion #4 is
specifically about not fabricating continuity.

---

## 7. Things that will bite

### 7.1 Climate events are retrospective, and lag matters
Humidity peaks *after* the shower ends; the decay runs 30–90 minutes. Any
detector necessarily fires minutes late, and its confidence keeps improving for
an hour afterwards.

So this is **retrospective evidence, not real-time presence.** The signal must
be backdated: `Since` = estimated onset, `Timestamp` = detection. The model
already supports this — it is exactly why those are separate fields — but it
has to be done deliberately, and consumers need to know that a climate-derived
occupancy signal is a claim about the recent past.

(Related, and worth confirming: the question describes "a temperature and
humidity spike in the morning, which would indicate a shower or a bath in the
evening". I have read that as a morning spike indicating a morning shower. If
the intent was genuinely to infer an *evening* event from a *morning*
measurement, that is a different and much harder problem — pattern-over-days,
firmly in Option G territory — and worth saying so explicitly.)

### 7.2 The sampling is irregular and quantised
Zigbee TH sensors (SNZB-02 and similar) are change-reporting, not periodic.
Consequences:

- **All derivatives must be per-unit-time**, never per-sample.
- **Quantisation** to ~0.5 °C / ~1 % RH steps puts a floor on detectable slow
  changes.
- **Report rate is itself a signal** — fast change produces more reports.
  Worth measuring; possibly worth using.
- **Refuse to differentiate across large gaps.** The engine already has this
  instinct in `energy.max_integration_gap` (30 m), which refuses to smear watts
  across a telemetry hole. The same guard applies verbatim here, and reusing
  the idea keeps the reasoning consistent.

The exact cadence is a property of the specific hardware and needs to be
measured from a capture, not assumed. That is the first thing the data ask in
§8 would settle.

### 7.3 Placement dominates
An ensuite sensor 30 cm from the shower behaves completely differently from one
by the door. With a working extractor the decay constant may be short enough
that a 5-minute sampling interval nearly misses the event. Sensor position and
room ventilation are per-room facts the config will need to carry, or at least
that the thresholds will have to be tuned against per-room.

### 7.4 Occupancy pollution is the failure mode that matters
A false `shower_detected` does not just add a bad event — it feeds occupancy,
which drives mode, which can hold the house out of `away`/`sleeping`
indefinitely. Recommend that climate-derived signals start with **low
confidence and a short TTL**, and only earn occupancy weight once corroborated
(Option E). Better to under-report for the first few months.

### 7.5 Retained MQTT messages
On broker reconnect, a retained climate payload arrives with a stale timestamp.
A naive derivative sees the whole interval collapse into one sample and infers
an enormous rate. The gap guard in 7.2 covers it, and the existing
`bridge_restart_flicker.jsonl` fixture is the pattern to extend.

---

## 8. What data would help, and what it would settle

Real data would change this from a design sketch into tuned thresholds. In
priority order:

1. **A raw capture of the TH sensors** — `cmd/fixture-capture` already does
   exactly this and writes the JSONL shape the fixtures expect:
   ```
   ./fixture-capture -broker tcp://192.168.1.200:1883 \
     -topics "zigbee2mqtt/#,climate/#,energy/boiler/sensor/#" \
     -output internal/testdata/fixtures/climate_household_capture.jsonl
   ```
   Include the weather station and the boiler in the same capture — the shared
   clock is what makes §4-E testable. **Two to four weeks** is the useful
   minimum; anything shorter cannot show seasonal baseline movement.

2. **Or an Influx export** of `device_environment` for the relevant device ids.
   Less faithful (already normalised, no MQTT timing artefacts) but likely
   covers *months*, which is exactly what tuning the slow baseline needs. Ideal
   is both: Influx for baselines, raw capture for cadence and edge cases.

3. **Ground-truth labels — the highest-value item per unit of effort.** Even
   10–20 labelled events ("showered 07:20–07:35 Mon/Tue/Wed", "windows open all
   Saturday afternoon", "roast in the oven 16:00–18:30 Sunday") converts
   threshold-setting from guesswork into measurement, and makes it possible to
   state a false-positive rate instead of an opinion. A plain text file of
   timestamps is enough.

4. **The sensor inventory**: which sensors exist, their floorplan room ids,
   where in the room they physically sit, and whether the room has an extractor
   or opening window. Note that `docs/influx-schema.md` lists
   `climate_basement`, `climate_firstfloor`, `climate_weatherstation` and
   `glowsensorth1` — **no bathroom or ensuite sensor**. If the motivating case
   is the ensuite, that sensor may need to exist before any of this can be
   validated, and getting it recording early is the long pole.

**What each answers:** (1) the cadence, quantisation and gap behaviour in §7.2;
(2) the baseline half-life and seasonal range for Option C; (3) the shape
thresholds for Option D and any honest accuracy claim; (4) whether the
motivating case is measurable at all yet.

---

## 9. Suggested increment

Smallest thing that is useful on its own and commits to nothing:

**Step 1 — Absolute humidity everywhere (no detector).** Add the Magnus
conversion; carry `absolute_humidity_gm3` and `dew_point_c` on `Reading`,
`Latest`, the canonical event stream and the `device_environment` Influx
measurement. Independently valuable: it makes the recorded series physically
meaningful, and every option above is built on it. No new state, no new
memory, no risk to existing behaviour.

**Step 1a — Condensation/mould risk (optional, nearly free).** Once dew point
exists, comparing it to the coldest surface in a room is a purely physical,
non-inferential check that needs no history, no baseline and no event
detection. Possibly the most practically useful output on this whole list, and
it falls out of step 1.

**Step 2 — `internal/climate` detector under P2.** Ring buffer + EWMA baseline
+ AH derivative. Emits one event type — `shower_detected` in a named room —
at **low confidence**, with `Since` backdated to onset, gated behind a config
flag and off by default. Validate by replay against captured fixtures.

**Step 3 — Corroboration (Option E).** Gate the confidence on boiler HW demand
and outdoor-differenced AH. Only once measured accuracy justifies it does the
signal earn occupancy weight.

**Step 4 — Shape classification (Option D)**, extending to bath / cooking /
window-open, once there is enough labelled data to distinguish them
defensibly.

Steps 1 and 1a are worth doing regardless of whether the detector is ever
built. Step 2 is the point of no return and should not start before the data in
§8 exists.

---

## 10. Summary

| | Today | Proposed |
|---|---|---|
| Trigger | Level, on instantaneous magnitude | Rate of change, against a slow baseline |
| Quantity | Raw reading (watts, RH) | Derived physical quantity (absolute humidity) |
| Memory | One debounce latch | Bounded ring + EWMA per sensor |
| Scope | One device in isolation | Corroborated across boiler, outdoors, other rooms |
| Output | Device activity state | Room-scoped `ActivitySignal` + `ActivityRecord` |
| Climate sensors | Inert by design | An activity source, at earned confidence |

The through-line: statehouse's existing detectors work because appliance power
is a *large, absolute, instantaneous* signal. Climate is a *small, relative,
time-extended* one. It needs different processing — but it does not need a
different architecture. `ActivitySignal`, `CanonicalSink`, the floorplan room
ids and the corroborating adapters are all already in place. What is genuinely
new is per-sensor history and the physics conversion in §3.
