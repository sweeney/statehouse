# Residual load attribution

Turning `UnmonitoredW` from one opaque number into named, tracked appliances.

Status: **design sketch.** Nothing here is built. The measurements against the
current engine are real and reproducible; the design on top of them is a proposal.

Background on where the idea came from: `docs/nilmtk-ecosystem-review.md` §6.2.

---

## 1. Problem statement

### What exists today

`AggregateElectricity` computes three numbers on every power-bearing reading:

```
GrossW       — the smart meter's whole-house draw
MonitoredW   — the sum of fresh per-device power readings
UnmonitoredW — Gross − Monitored
```

`UnmonitoredW` is everything in the house without a monitoring plug: the oven, the
hob, the shower, the immersion heater, lighting circuits, the boiler's own pump,
every unmetered socket. It is served on `/state` and written to Influx, and it is
a single scalar with no structure.

### What we want

An unmonitored oven should be as visible to downstream consumers as a monitored
dishwasher: a device in `/state/devices`, with activity states, cycle start and
finish events, and energy per cycle. The house should be able to say *"the oven has
been on for 40 minutes"* without anyone having bought a plug for it.

This is squarely the North Star in `oracle-definition-of-success.md` — downstream
systems reasoning about meaningful state rather than telemetry — extended to
appliances that have no sensor at all.

### Why it is hard

The only signal is a single aggregate at roughly 10-second cadence. This is
**low-frequency NILM**, and it is not a solved problem: NILMbench's published T1
results at 60 s show F1 around 0.45 for a fridge using a strong transformer model.
Anyone promising reliable full disaggregation at this resolution is overselling.

Three specific difficulties, in increasing order of nastiness:

1. **The residual is a difference of two unsynchronised streams.** The meter
   samples on its own clock; Zigbee plugs are change-reporting. Their disagreement
   in time appears as structure in the residual that isn't there.
2. **Knowing when a load stops.** A step up is easy. Matching the corresponding
   step down, when several loads overlap, is a combinatorial matching problem with
   no ground truth to check against.
3. **Loads change shape.** An oven thermostats. An element ages. An EV charger
   tapers. A detector built around "constant draw between two steps" describes
   almost none of the appliances we actually care about.

### Scope

**In scope:** step-change detection on the residual; tracking a load from start to
stop; attributing a tracked load to a human-supplied label; emitting it as a
first-class device.

**Out of scope:** true multi-appliance disaggregation; any ML model; sub-second or
harmonic signatures (the meter cannot provide them); appliances whose draw ramps
rather than steps (see §4.5).

---

## 2. Ideas

Six ideas, roughly in dependency order. The first three stand alone and are worth
building even if the rest never happens.

### 2.1 The residual is just another power signal

Statehouse already owns a well-tested state machine that turns a power signal into
activity states, hysteresis-bounded cycles and integrated energy: `device.Runtime`
in `internal/device/statemachine.go`.

The residual is a power signal. Feed it to a virtual device and everything
downstream works unchanged — `/state/devices`, `cycle_started` / `cycle_finished`,
the Influx writer, retained MQTT topics, the recent-event log,
`house.ActiveDevices`.

This is the single highest value-per-line idea here: no labelling, no matching, no
new consumer contract, and it immediately answers "is something unmonitored
running right now, and for how long?"

### 2.2 Step detection, locked to meter cadence

NILMTK's `switch_times` is simply "power changed by more than a threshold between
consecutive readings". Applied to the residual, an up-step is a load starting and a
down-step is one stopping.

The critical constraint — measured, not assumed — is in §4.1: this must run **only**
on meter-triggered recomputes.

### 2.3 Sessions: a tracked load has a lifetime

An up-step opens a session `{id, magnitude_w, started_at, label?}`. A matching
down-step closes it. A session is a first-class object with an identity, not a pair
of unrelated events — which is what makes "the oven has been on 40 minutes"
expressible.

### 2.4 Conservation as a self-check

At any instant, `UnmonitoredW ≈ Σ(open session magnitudes)`. The drift between the
tracked sum and the actual residual is a direct, continuous measure of how wrong
the tracking is.

When drift exceeds a threshold: force-close every open session, re-seed from the
current residual level, emit a warning. This **bounds** error rather than letting a
single mis-match corrupt state indefinitely.

This is deliberately the same shape as the existing energy design — two parallel
estimates tracked continuously with a divergence warning when they disagree
(`energy_divergence_warning`). Second application of a pattern the codebase already
trusts.

### 2.5 Labelling, retro-first

Store every session's features whether or not anyone was watching, and label
afterwards from the activity log. A live "arm a label then go switch it on"
calibration mode is the secondary path, not the primary one.

### 2.6 Signatures as distributions, not constants

A label's signature should be a running distribution updated by each confirmed
session, not a fixed magnitude. Matching becomes "within k standard deviations",
which adapts to drift automatically and widens honestly for genuinely variable
loads.

This is NILM Metadata's `distributions.on_power` with `source: empirical from data`
and `n_datapoints` — the provenance idea from the review arriving where it is
actually load-bearing.

---

## 3. Measurements against the current engine

Four facts established by driving the real code. They constrain the design more
than any of the ideas above.

### 3.1 Plug-triggered recomputes make the residual lie

`recomputeElectricity` runs on **every** power-bearing reading, plug and meter
alike. A monitored 3 kW kettle switching on:

| | gross | monitored | residual |
| --- | --- | --- | --- |
| t+0s steady, kettle off | 640 | 0 | 640 |
| t+4s plug reports, meter stale | 640 | 3000 | **−2360** |
| t+10s meter catches up | 3640 | 3000 | 640 |

A detector on every recompute fires on **every monitored appliance in the house**,
with the wrong sign.

### 3.2 A stale meter still supplies gross

`isFreshDevice` is applied to monitored devices but **never to the meter**:

| meter last seen | gross | grossSeen |
| --- | --- | --- |
| 0s ago | 640 | true |
| 6h ago | 640 | true |
| 72h ago | 640 | true |

A meter three days dead keeps supplying its last reading, and the residual becomes
fiction. This is a pre-existing issue affecting `/state` today, not something the
detector introduces — but the detector must not build on it.

### 3.3 Real meter cadence is irregular, with duplicates

From `internal/testdata/fixtures/meter_readings_real.jsonl`: timestamps at
`10:57:03`, `:09`, `:19`, `:19`, `:39` — intervals of 6 s, 10 s, 0 s and 20 s. The
duplicate is real, not a capture artefact.

Nominal 10 s cadence is not a safe assumption; `max_sample_period` semantics (§3 of
the review) are the right framing.

### 3.4 The noise floor looks workable

The same fixture sits at 640 W with a ±4 W wobble and 1 W quantisation. NILMTK's
conventional 40 W step threshold clears that comfortably — but this is five samples
of one quiet moment, not a measurement.

---

## 4. Risks

Ordered by how much damage they do if missed.

### 4.1 Plug/meter skew — false positives on every monitored appliance

**Severity: fatal if unhandled.** Per §3.1.

**Mitigation:** gate detection on `triggeredByMeter`. The precedent already exists —
`emitElectricityCanonical` is behind that gate specifically so "the canonical stream
stays locked to meter cadence". Residual detection belongs behind it for the same
reason.

**Residual risk after mitigation:** skew survives in smaller form. The meter reports
at *t* reflecting power at *t*; the plug last spoke at *t−8s*. A monitored load
starting in between shows in gross before monitored, so the residual steps up and
back down over one or two samples. Handle with a settle window — require the new
level to hold for N meter samples — reusing `candidateSample` /
`maybeBegin` / `maybeEnd` rather than writing a second hysteresis.

### 4.2 Meter gaps and recovery

**Severity: high.** On recovery from an outage, a meter-triggered recompute fires
with a gross that jumped across the gap, producing an enormous spurious step.

**Mitigation:** do not compute a step across an interval longer than the meter's
`max_sample_period`. This is exactly NILMTK's `good_sections` concept, and exactly
what `energy.max_integration_gap` already does for kWh. Reuse the framing: a step is
only valid within a good section.

Fixing §3.2 (meter staleness) is a prerequisite for trusting anything here.

### 4.3 Stale monitored devices inflate the residual

**Severity: high, and silent.** When a plug goes stale it is dropped from
`MonitoredW`, so the residual rises by that plug's last draw — indistinguishable
from a real load starting.

**Mitigation:** `StaleDevices` and `StaleDeviceCount` are already computed. Suppress
detection when `StaleDeviceCount > 0`, or at minimum mark the session
`contaminated: true` and exclude it from signature learning.

### 4.4 Systematic bias from mixed AC types

**Severity: medium, and permanent.** Review §3 establishes that three adapters write
`Reading.PowerW` with potentially different quantities — the UPS's `load_watts` is
likely apparent power derived from NUT's percentage-of-VA. Summing apparent against
the meter's active watts puts a constant offset into the residual.

A constant offset does not break *step* detection — steps are differences, so the
offset cancels. It does corrupt the **magnitude** of a session and therefore its
signature, and it corrupts the conservation check's drift measure.

**Mitigation:** the power-factor cross-check (review §3, item 4) before trusting
learned magnitudes.

### 4.5 Ramping loads never close

**Severity: medium; unfixable within this design.** An EV charger tapering as the
battery fills slides rather than steps. Step detection misses the end, the session
appears to run forever, and the conservation check eventually flags drift —
correctly reporting failure, but not tracking the load.

**Mitigation:** none that is clean. Options: monitor such loads directly (an EV
charger is precisely where a dedicated meter earns its cost); or add a slow-drift
mode attributing gradual residual change to the largest open session. Do not let
this block the earlier stages.

### 4.6 Ambiguous matching between similar loads

**Severity: medium.** Two 2 kW resistive loads are one signature. No amount of
modelling at 10 s aggregate resolution separates them.

**Mitigation:** honesty in the API. Return a candidate set with confidences rather
than a single guess, and let consumers decide. Never present an ambiguous match as
a fact.

### 4.7 Restart blindness

**Severity: medium.** Loads already running at process start are invisible — there
was no up-step to observe. Their eventual down-step arrives unexplained.

**Mitigation:** on start, seed a single "pre-existing" session at the current
residual level, unlabelled, and let the conservation check reconcile it as loads
close. Expect and tolerate `unexplained_stop` events in the first hours. This also
argues for persisting session state across restarts eventually — noting that
statehouse deliberately holds `Lifetime` in memory only, so persistence would be a
new pattern needing its own justification.

### 4.8 Derived state feeding derived state

**Severity: medium, and architectural.** If a residual device feeds occupancy (and
an oven turning on genuinely is strong occupancy evidence), then a *derived,
uncertain* signal is driving another derived signal. A mislabelled or phantom
residual load could assert occupancy in an empty house.

**Mitigation:** residual devices carry their confidence into the occupancy weight
rather than contributing as a full-strength boolean — which needs the evidence
weighting from review §1 to exist first. Until then, keep residual devices out of
occupancy entirely. Sequencing matters: weighting before occupancy participation.

### 4.9 Negative gross

**Severity: low today, high if solar is ever installed.** SMETS2 meters with
generation report net export as negative `GrossW`; the current export fixture reads
`0.000`, so this is hypothetical. A negative gross makes the residual meaningless
and would generate phantom steps at every cloud passing.

**Mitigation:** detect and suspend attribution when gross is negative. Revisit
properly if generation is ever added — at that point the residual needs a different
formulation entirely.

### 4.10 Threshold sensitivity

**Severity: low, but it decides the false-positive rate.** Too low and the detector
chases noise; too high and it misses a 400 W load entirely.

**Mitigation:** measure rather than inherit. This is the first job for the eval
harness in review §5.

### 4.11 Inference sensitivity

**Severity: low technically; worth stating.** Attributing the residual infers
household behaviour more finely than the current aggregate does — shower times,
cooking times, who is in and when. It stays local to statehouse and the existing
Influx bucket, and no new egress is proposed, but it is a meaningful increase in
resolution about people's lives and should be a conscious choice rather than a side
effect.

---

## 5. Implementation suggestions

### 5.1 Stage 1 — the residual as one virtual device

Smallest useful thing; no labelling, no matching. Roughly 150 lines.

- Inside `recomputeElectricity`, behind `if triggeredByMeter`.
- Reject the sample if the gap since the last meter sample exceeds
  `max_sample_period` (§4.2), or if `StaleDeviceCount > 0` (§4.3), or if
  `GrossW < 0` (§4.9).
- Feed `UnmonitoredW` into a `device.Runtime` for a virtual device with
  `identity.scheme = "residual"`, `primary = "unattributed"`, and a new
  `residual_load` class whose thresholds are configurable like any other.

Delivers: `/state/devices/residual:unattributed`, cycle events, Influx series,
retained MQTT topic — all through existing machinery.

**Prerequisite:** fix §3.2 first. Apply `isFreshDevice` to the meter, or add an
explicit meter-staleness check, so a dead meter stops producing a confident gross.
That is a small correctness fix worth landing on its own merits regardless of this
work.

### 5.2 Stage 2 — session tracking

```go
type Session struct {
    ID          string
    MagnitudeW  float64
    StartedAt   time.Time
    Label       string    // empty until labelled
    Contaminated bool     // stale devices or busy house at start
    PendingClose *time.Time // set on down-step, cleared if it reopens
}
```

On each confirmed step Δ:

- **Δ > 0** — first try to reopen a `PendingClose` session whose magnitude matches
  (§5.3). Otherwise open a new session.
- **Δ < 0** — match |Δ| against open sessions within `max(50 W, 10%)`. Exactly one
  match closes it; several resolve by best-fit duration against the label's expected
  duration, falling back to oldest-first; none emits `unexplained_stop`.

Run the conservation check on every meter sample. On drift beyond threshold,
force-close all, re-seed, warn.

### 5.3 Cycling: `min_off_duration` merging

A down-step sets `PendingClose` rather than closing. A matching up-step within
`min_off_duration` clears it and the session continues as one. Otherwise the session
finalises when the window expires.

This is the **same mechanism** review §1 proposes for the dishwasher's
`finished_recently` problem — hold a provisional end, retract it if the load
returns. One primitive, two uses. Build it once in the state machine.

### 5.4 Labelling API

```
PUT  /state/loads/sessions/{id}/label   {"label": "oven"}      # retro (primary)
POST /state/loads/labelling             {"label": "oven", "ttl": "5m"}   # live
GET  /state/loads/sessions?labelled=false&since=...            # review queue
GET  /state/loads/signatures                                   # learned distributions
```

Record `house.Activity` and `StaleDeviceCount` at session start so signatures can be
marked clean or contaminated, and weight clean ones when learning. The API can also
answer "is now a good time to calibrate?" from existing house state.

Per the review's licensing note, any appliance vocabulary adopted from NILM Metadata
carries attribution in the file that holds it.

### 5.5 Event vocabulary

Fits the existing `DerivedEventType` list without stretching it:

| Event | Evidence |
| --- | --- |
| `unattributed_load_started` | `delta_w`, `residual_before/after`, `session_id`, `contaminated` |
| `unattributed_load_finished` | `session_id`, `duration_seconds`, `energy_kwh`, `matched_magnitude_w` |
| `unattributed_load_identified` | `session_id`, `label`, `confidence`, `candidates[]` |
| `unattributed_load_unexplained_stop` | `delta_w`, `open_sessions[]` |
| `residual_divergence_warning` | `tracked_sum_w`, `actual_residual_w`, `drift_w` |

### 5.6 Promotion to a device

Once labelled, a session's label becomes a device: `scheme = "residual"`,
`primary = "oven"`. From that point an unmonitored oven is indistinguishable from a
monitored one to every downstream consumer.

Gate participation in occupancy behind the evidence weighting of review §1
(see §4.8).

### 5.7 Sequencing

1. Fix meter staleness (§3.2) — independent correctness fix.
2. Measure the residual noise floor from a quiet-house fixture; set the threshold.
3. Stage 1 — residual as one virtual device.
4. Stage 2 — session tracking plus the conservation check.
5. `min_off_duration` merging, shared with the dishwasher fix.
6. Retro-labelling, then live calibration.
7. Per-label distributions replacing fixed magnitudes.
8. Promotion to devices; occupancy participation only after weighting exists.

Steps 1–4 are useful on their own and need no labelling.

---

## 6. Areas for further thought and research

**Which features actually discriminate.** The available features are step magnitude,
steady-state mean and variance, duty cycle, duration, time of day and day of week,
house state at start, and concurrently-active monitored devices. Expectation:
magnitude plus duration plus time-of-day separates oven from shower from immersion
heater. That is a hypothesis, not a result, and it needs labelled data to test.

**How to evaluate attribution at all.** The eval harness in review §5 scores
detection against fixtures with known ground truth. Residual attribution has no
ground truth unless someone labels it — so evaluation and labelling are the same
problem. Worth designing the labelling store so it doubles as the eval fixture
format.

**Whether the conservation check can do more than warn.** It currently detects that
tracking is wrong. Could the drift signal instead *correct* magnitudes continuously
— nudging each open session's magnitude to minimise total drift? That is a small
optimisation problem per sample and might handle mild ramping (§4.5) as a side
effect. Unclear whether it is stable; worth an experiment before any commitment.

**Whether NILMTK models are ever worth it here.** Once §6.1 of the review has
accumulated labelled history, the contrib models become trainable on this house's
own data. The honest expectation from NILMbench's numbers is modest. The interesting
question is not "can we run a transformer" but "does a model beat
magnitude-plus-time-of-day on our own labelled set" — which the eval harness can
answer cheaply, and which is worth knowing either way.

**Multi-load decomposition of simultaneous steps.** Two loads switching within one
meter sample appear as a single step of their sum. Detecting and splitting those
needs either a step-size prior per label or the assumption that simultaneous
switching is rare. NILMTK's `simultaneous_switches` treats co-switching as a
*feature* (it is what a routine looks like) rather than noise — possibly the more
useful framing.

**Interaction with the ground-truth export.** Review §6.1 proposes exporting this
house as a NILM-Metadata dataset. Labelled residual sessions would be unusually
valuable there — they are labelled appliance activations without a submeter, which
is exactly what the field lacks. It also raises the question of whether an exported
dataset should include inferred labels at all, or only plug-measured ground truth.
Probably: include them, clearly marked as inferred, with confidence attached.

**Persistence.** Session state is in-memory and dies with the process, like
`Lifetime`. Signatures learned over months plainly should not. Where do learned
distributions live — remote config, a local store, Influx? This needs a decision
before step 7 of the sequencing, and it is the first thing in statehouse that
genuinely wants durable local state.
