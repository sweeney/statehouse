# What statehouse can learn from the NILMTK ecosystem

A review of the four NILMTK projects against statehouse, and what they open up.

Reviewed at: `nilmtk` 8967bd1, `nilm_metadata` 5df37de, `nilmtk-contrib` 14efd54,
`nilmbench` 88517ce (September 2026).

---

## 0. What NILMTK is, and how it is shaped

NILM — non-intrusive load monitoring — is the problem of recovering per-appliance
energy from a single whole-house meter. NILMTK is the open-source ecosystem around
it, deliberately split into four repositories with one job each:

| Layer | Repo | Job |
| --- | --- | --- |
| Vocabulary | `nilm_metadata` | Appliance taxonomy, meter relationships, dataset schema |
| Engine | `nilmtk` | Dataset conversion, meter access, preprocessing, statistics, metrics |
| Models | `nilmtk-contrib` | Disaggregation algorithms behind one interface |
| Evaluation | `nilmbench` | Frozen T1/T2/T3 protocols, provenance-checked result bundles, leaderboard |

Each README links the other three. That split *is* the first lesson, and §6 returns
to it.

The important asymmetry to keep in mind throughout: **NILMTK is batch, offline and
research-facing; statehouse is online, streaming and operational.** Several NILMTK
ideas do not port at all (post-hoc activation merging, train/test splits, chunked
pipelines over a whole dataset). This document says which ideas port and which
don't, rather than listing everything they have.

---

## 1. Taxonomy: type vs class, and inheritance

### What they do

`nilm_metadata` defines an `ApplianceType` vocabulary in YAML with prototypal
inheritance — `parent:`, arbitrary depth, dicts update, lists extend, with
`do_not_inherit:` as the escape hatch. A type carries:

- `on_power_threshold`, `min_on_duration`, `min_off_duration`
- `synonyms` — `fridge` has `[fridge freezer, freezer]`
- `subtypes` — `[top-loader, front-loader]` for a spin dryer
- `categories` on several independent axes: `traditional`, `size`, `electrical`
  (`resistive`, `SMPS`, `single-phase induction motor`…), `google_shopping`
- `components` — recursive; a dish washer *is* a motor + an electric water heater +
  an electric air heater, each with its own power distribution
- `control` — `[manual, timer, motion, sunlight, thermostat, always on]`
- `distributions` — priors over `on_power`, `on_duration`, `off_duration`,
  `usage_hour_per_day`, `rooms`, `appliance_correlations`, each tagged with a
  `source` of `{subjective, empirical from data, empirical from publication}`

### What statehouse does

Nine flat classes as Go constants in `internal/device/profiles.go`, `name_hints`
for classification, one threshold set per class with a one-level per-device merge
(`mergeThresholds`).

### The gap that matters most: class conflates behaviour with identity

`cycle_power_device` is a *behaviour* — hysteresis, a cycle, a
`finished_recently` decay. `dishwasher` is an *identity*. Statehouse uses one word
for both, and the cost is visible in the config:

```yaml
cycle_power_device:
  name_hints: [dishwasher, washing_machine, washer, tumble_dryer, dryer]
  default_thresholds:
    inactive_sustained_for: 5m
```

Five distinct appliance types, one threshold set. NILM Metadata gives a washing
machine `min_off_duration: 300` and a dish washer `min_off_duration: 1800`, and the
reason is physical: dishwashers have long near-zero dry phases that washing
machines don't. Statehouse's single 5m compromise is long enough to delay every
washing-machine cycle end and short enough to split some dishwasher cycles.

**Recommendation.** Add an `appliance_type` alongside `class`. Class stays the
state-machine dispatch key (`profiles.go` keeps its switch); type supplies defaults
that override the class defaults. Make the type tree inheritable with NILM
Metadata's semantics — this generalises `mergeThresholds` from one level to N, and
removes the copy-paste that per-site remote config will otherwise accumulate.

### `control` is the per-device escape hatch issue #32 asked for

`internal/state/house.go` decides occupancy relevance from a hardcoded class switch:

```go
case device.ClassShortBurst, device.ClassCyclePower,
     device.ClassMedia, device.ClassBinaryState:
     return true
```

so the boiler's CH/HW channels — `binary_state_device` in the shipped config —
drive occupancy. **That is deliberate, not an accident.** Issue #32 argued for it
explicitly ("for a home where central heating is the strongest occupancy signal,
heating on at 06:30 → empty at 06:31 is a meaningful misclassification"), it was
fixed in b4c850a, and `TestDeriveHouseState_BinaryStateIdleWithinQuietAfter` and
`TestDeriveHouseState_BinaryStateCurrentlyActive` pin it. For the primary case — a
UK home in winter, heating on, nothing else running — the prior is right.

What NILM Metadata adds is the exception that issue #32 itself said should exist
and still doesn't:

> If specific binary devices *shouldn't* count as occupancy (e.g. a freezer alarm
> contact), the right place to express that is at the device config level — not by
> class-wide exclusion.

`control: [manual | timer | thermostat | motion | sunlight | always on]` is exactly
that device-level place, already designed and already vocabulary-controlled.

#### The cost of having no exception yet: `away` never fires

Heating-as-occupancy is right when the heating tracks occupants. It inverts when
the heating is running *because* they're gone — frost protection, or a winter
schedule left on during a holiday. Each firing refreshes `mostRecentActivity`, so
`EmptyAfter` restarts before it can elapse.

Simulating an empty house over 48h against `DeriveHouseState`, with `EmptyAfter: 6h`
and the boiler running 30 min on a fixed schedule:

| Heating interval | Reaches `occupancy: empty`? | Reaches `mode: away`? |
| --- | --- | --- |
| every 4h | no | no |
| every 8h | yes | yes |

Whenever the heating interval is shorter than `EmptyAfter`, the house can never
report empty or away, for as long as the heating schedule runs — and that is
precisely the two-weeks-in-January case where `away` is worth having. The bound is
sharp: it flips purely on interval vs `EmptyAfter`, with no other signal involved.

A second, smaller effect: a 3am thermostat firing moves `mode` from `night` to
`day`, because `HouseActivityQuiet` plus `OccupancyOccupied` takes the day branch
before the hour is consulted.

`control` fixes both without giving up issue #32's decision, because it is per
device, not per class: the CH relay on a thermostat-led system can stay occupancy
evidence, while a frost-protection channel or a freezer alarm contact declares
itself as machine-driven. It also lets the existing `ClassContinuous` special-case
in `DeriveHouseState` (compressor cycles excluded from `activeCount`) fall out of
the model rather than stay hand-carved.

The honest caveat on all of this: it only bites when the devices namespace actually
classifies the boiler channels as `binary_state_device`. An unclassified boiler
device contributes nothing to occupancy at all — verified by driving a `CH_ON`
through the real adapter and engine with and without a devices entry. Worth
confirming against the live `devices_home` namespace before treating it as
load-bearing.

### `components` reframes the split-cycle problem

Statehouse doesn't need component modelling. But the framing is useful: a dishwasher
cycle is not one power band, it is a motor phase, a heat phase and a dry phase. The
current `finished_recently` machinery — `finishedRecentlyLowReadings = 3` plus a
`finishedRecentlyTTL = 30m` fallback plus `inactive_sustained_for` — is three
ad-hoc mechanisms approximating "this cycle has phases". Naming the phases is the
honest fix.

Related: NILMTK's `min_off_duration` *merges* activations separated by a short gap,
post hoc, as one parameter. Statehouse can't merge post hoc without buffering —
but it could hold a provisional cycle end and retract it, which is the streaming
equivalent and would replace all three mechanisms with one.

### Provenance on priors

Every `distributions` entry in NILM Metadata records where the number came from,
down to `source: subjective  # These values are basically guesses!` in their own
worked example. Every threshold in statehouse's config is a subjective guess with
nothing recording that it's a guess — and once §5's eval harness exists, some of
them will become empirical. Tag them.

The immediate payoff is `on_duration`: a distribution over expected cycle length
gives statehouse something it can't currently do — "the dishwasher is ~20 minutes
from finishing" — and, more valuably, an anomaly signal. A fridge whose compressor
cycle runs 3× its usual duration is a fridge that is failing.

---

## 2. The wiring tree — the largest structural gap

### What they do

`ElecMeter` in NILM Metadata carries `submeter_of`, `site_meter`,
`submeter_of_is_uncertain`, `disabled`, `phase`, `upstream_meter_in_building`.
NILMTK builds a `wiring_graph` from it and computes `proportion_of_upstream`,
`proportion_of_energy_submetered`, and an explicit `Remainder` pseudo-meter.

### What statehouse does

`internal/state/electricity.go` is a flat, one-level version of exactly this:
gross (site meter) − monitored (sum of plugs) = unmonitored (remainder). The README
is admirably honest that coverage can exceed 1 and unmonitored can go negative.

### The latent bug

Statehouse assumes every monitored device is directly downstream of the meter.
There is no way to say otherwise. `isPowerMonitored` includes `ClassUPSSensor`, and
the code comment reasons about it:

> A UPS is included too: it reports its output load (LoadWatts) as PowerW, which is
> real consumption — typically the network/computing gear behind it, **none of
> which is otherwise on a monitored plug**, so counting it improves coverage rather
> than double-counting.

That emphasised clause is a hoped-for property of the current deployment, not an
expressed constraint. The moment a monitored plug goes behind the UPS,
`MonitoredW` silently double-counts it, `UnmonitoredW` goes negative, and the only
signal is a README sentence saying consumers decide whether to render it.

`submeter_of` makes that constraint expressible and, more importantly, checkable:
a device whose upstream is another monitored device is excluded from the top-level
sum by construction rather than by hope.

**Also worth taking:**

- **`disabled: true` for redundant parallel meters.** Statehouse's "lowest-id meter
  wins" tiebreak is deterministic-but-arbitrary where NILM Metadata has an explicit
  flag. A two-meter home is currently stably wrong rather than correct.
- **`submeter_of_is_uncertain`.** A schema with a first-class place to say "I think,
  but I'm not sure". Statehouse has `Unclassified` but no general uncertainty
  channel on configuration.
- **`phase`.** Irrelevant for a single-phase UK home; relevant the day a second site
  is three-phase.

---

## 3. Data quality — the clearest immediate win

### What they do

NILMTK treats data quality as first-class, with a **catalogue of meter device
models** (`meter_devices.yaml`, keyed by make and model) carrying:

- `sample_period` — nominal seconds between samples
- `max_sample_period` — beyond this we assume the meter was off
- `measurements` — `physical_quantity` × `type` (AC type: `active` / `reactive` /
  `apparent`) × `upper_limit` / `lower_limit`

and, computed from those:

- `good_sections` — contiguous spans where every gap ≤ `max_sample_period`
- `dropout_rate` = 1 − (actual samples / expected samples)
- `uptime` — total duration of all good sections

### What statehouse does

`StalenessSecondsForClass` — a four-branch switch returning 900 or 3600 — plus
`electricity.staleness_active` / `staleness_idle`.

### Why the axis is wrong

Staleness is attached to the **class**, but reporting cadence is a property of the
**hardware**. The config comments already say so:

> Change-reporting plugs (Aqara, IKEA) can legitimately go many minutes silent at idle

An Aqara plug and an IKEA plug in the same class have different cadences, and the
class has one number for both. Z2M already publishes `definition.model` and
`definition.vendor` on `bridge/devices` — the adapter almost certainly already sees
this — so a meter-device catalogue keyed by model is mostly plumbing already in
hand.

### Four concrete items

1. **Add a meter-device catalogue** keyed by Z2M model/vendor, carrying
   `sample_period` and `max_sample_period`. Per-hardware staleness replaces
   per-class staleness.

2. **Declare `ac_type` per meter model.** This is the documented cause of
   statehouse's coverage>1 problem. The README mentions "apparent-vs-active power on
   some plugs" as an aside; NILM Metadata makes it a declared property, and
   nilmbench records a resolved *preference order* per dataset
   (`"mains_ac_types": ["apparent", "active"]`) in every result bundle. Statehouse
   currently sums apparent-power plugs against an active-power meter and calls the
   difference "unmonitored". Declaring it lets the aggregator convert, refuse, or at
   minimum flag the mixture — instead of silently folding a systematic error into
   the residual.

3. **Compute `dropout_rate` per device.** Statehouse has `LinkQuality` and `RSSI`
   but no measure of delivered-vs-expected samples. With `sample_period` from the
   catalogue this is a handful of lines, and it is far more actionable than
   linkquality: it is the metric that catches a plug silently dropping half its
   reports, which directly corrupts the integration energy path.

4. **Carry good-section coverage on the energy number.** Statehouse already clamps
   integration across gaps > `max_integration_gap` and counts `GapsClamped` — that
   is `good_sections` in disguise. But the count is a diagnostic, and the resulting
   kWh is presented with the same confidence as a gap-free one. Add a
   `coverage_fraction` to `CycleEnergy` next to `DivergencePct`, so a cycle
   integrated across a 25-minute hole says so in the data rather than in a counter.

---

## 4. Statehouse is ahead in places — worth recording

Not everything flows one way.

- **Absent vs zero.** `model.Reading`'s pointer-per-field discipline preserves
  "field not reported" distinctly from "field reported as zero". NILMTK's
  DataFrame-and-NaN model cannot express the fire-alarm subtlety statehouse
  documents — that a battery-only payload from a smoke detector must never be read
  as `smoke=false`.
- **Dual-path energy.** Tracking a hardware counter and a power integral in
  parallel and emitting `energy_divergence_warning` when they disagree has **no
  NILMTK analogue at all**. NILMTK has `cumulative energy` as a physical quantity
  but no cross-check between it and integrated power. This is a genuinely good idea
  that could be contributed back.
- **Availability separate from activity.** NILMTK's `good_sections` conflates "the
  meter was off" with "the appliance was off". Statehouse separates them properly,
  with debounce on top.
- **Fixture capture across broker restarts.** `cmd/fixture-capture`'s synthetic
  `_capture/connection_lost` markers preserve outage timing in the fixture. That is
  the hard half of a reproducibility story, and most home projects never build it.

---

## 5. Evaluation — the deepest lesson, and the biggest hole

### What nilmbench freezes

Every benchmark run pins, in typed TOML and then in the result bundle:

- a **task**: train/test windows, buildings, appliances, `sample_period`
- a **metric policy**: the activation thresholds used for scoring — and they ship
  **two**, `legacy-nilmtk-10w` (a flat 10 W) and `paper-appliance-thresholds`
  (fridge 50 W, kettle 2000 W, microwave 200 W…), each with a `source_url`, so
  older numbers stay reproducible after the definition improves
- a **coverage policy**: `warn` vs strict rejection of silently truncated windows
- a **shared_meter_policy**: what to do when two appliances share a circuit
- dataset SHA-256s and sizes, seeds, model parameter hashes, container digests

Results land in `results/candidates`; publication into `results/published` is an
explicit reviewed copy, "never an automatic side effect of training".

### What statehouse has

Fixtures and a capture tool — the hard half. What's missing is the scoring half:
the fixtures assert on *code behaviour*, not on *accuracy against ground truth*.

There is currently no way to answer: **did raising `active_above_w` from 20 to 25
make dishwasher detection better or worse?** Every threshold in
`config.example.yaml` is therefore unfalsifiable, and so is every proposal in §1
and §3 of this document.

### Recommendation — highest leverage item here

A `cmd/statehouse-eval` that replays a fixture against a config and emits:

- cycles detected vs expected; start/end timestamp error
- energy error per cycle, per strategy
- F1 on the active/idle series (NILMTK's `f1_score`, `precision_recall`,
  `mean_normalized_error_power` are all ~30-line pure functions worth porting)
- the config hash the run used

The fixtures already exist. What's missing is a ground-truth sidecar per fixture
(`dishwasher_cycle.expected.json`) and a scorer. Everything else on this list
becomes measurable the moment it lands.

And steal the versioned metric policy specifically: **statehouse's thresholds live
in remote config and change under a running system with no record.** A threshold
change is a semantic change to every event emitted after it, and there is currently
nothing in the data that says which threshold generation produced a given event.

---

## 6. What this opens up

### 6.1 Statehouse is a NILM ground-truth generator

The hard problem in NILM research is labelled data: you need a whole-home meter
*and* per-appliance submeters *and* appliance labels, time-aligned. Statehouse has
exactly that, live — a SMETS2 meter for gross, per-plug power as submeters,
classified appliance identities, and cycle boundaries already segmented.

REDD is 2011. UK-DALE is 2013–2015. A modern UK home with Zigbee plugs, a smart
meter, a UPS and an EV is not represented in any public dataset. A
NILM-Metadata-conformant exporter — `dataset.yaml`, `meter_devices.yaml`,
`building1.yaml`, with the Influx series as the data — makes the house's history
loadable by every tool in that ecosystem. `nilm_metadata` already ships
`convert_yaml_to_hdf5` for the last mile.

Small amount of code. Large amount of reach. It also forces §2 and §3 to be
answered honestly, because the schema has required fields for exactly the things
statehouse currently leaves implicit.

### 6.2 Attribution of the residual — start with events, not models

Statehouse already computes `UnmonitoredW`. Today it is one opaque number.

The tempting move is disaggregation. Calibrate expectations first: Glow/SMETS2 is
~10s cadence, which is low-frequency NILM, and nilmbench's own T1 numbers at 60s
show F1 around 0.45 for a fridge with a strong transformer model. Full
disaggregation is not close to a solved problem at this resolution.

The realistic win is **event detection on the residual**, which needs no ML at all.
NILMTK's `switch_times` is a >40 W step change between successive readings. A step
in gross that no monitored device accounts for is an unattributed load starting —
the oven, the shower, the immersion heater. That is cheap, online, and produces a
derived event in exactly statehouse's existing shape:

```
unattributed_load_started  { delta_w: 2400, at: ... }
```

Disaggregation proper becomes interesting *later*, once §6.1 has accumulated enough
labelled history to train on — and at that point the labels are free, because
statehouse generated them.

### 6.3 Routines, learned rather than configured

NILMTK's `simultaneous_switches` counts timestamps where two or more submeters
change state together. In a house-state engine that is a behavioural primitive:
simultaneous switches are what a *routine* looks like. Kettle plus toaster is
breakfast.

`docs/oracle-definition-of-success.md` already lists `morning_routine`,
`meal_preparation`, `evening_wind_down` as future semantic overlays. NILM
Metadata's `appliance_correlations` prior and NILMTK's `pairwise_mutual_information`
and `pairwise_correlation` are the machinery for *learning* those from the house's
own history instead of hand-coding them.

### 6.4 A learned baseline for the mode dimension

`activity_histogram` bins "when on" by hour-of-day over a period. Statehouse's
`mode` dimension currently flips night/day on wall-clock hours from
`house.timezone`. A per-house learned histogram replaces a configured guess with an
observation — and, more usefully, supplies an anomaly baseline: the house is active
at 3am, and that is unusual *for this house*.

### 6.5 The four-repo split is a comment on statehouse's own shape

Statehouse currently holds its vocabulary in three places at once: classes as Go
constants in `profiles.go`, name hints in `config.example.yaml`, behaviour in a
switch statement, per-device overrides in remote config namespaces.

NILMTK's separation of vocabulary from engine from models from evaluation is the
move that makes each layer independently versionable. The specific step for
statehouse is to pull the **vocabulary** — types, synonyms, default durations,
`control`, meter device models — out into a versioned data artifact, as a peer to
the existing remote-config namespaces and ideally shared across both sites.

That is also the precondition for §5: an eval harness needs a config that is a
hashable artifact, not a switch statement plus three YAML files plus a remote fetch.

---

## 7. Suggested order

Ordered by leverage-per-unit-effort, not by section number.

| # | Item | § | Effort | Why first/last |
| --- | --- | --- | --- | --- |
| 1 | `cmd/statehouse-eval` + ground-truth sidecars | 5 | M | Everything below is unfalsifiable without it |
| 2 | `control` on device config, feeding occupancy | 1 | S | Smallest change; unblocks `away` without reversing issue #32 |
| 3 | Meter-device catalogue (`sample_period`, `max_sample_period`, `ac_type`) | 3 | M | Fixes staleness axis *and* explains coverage>1 |
| 4 | `submeter_of` + `disabled` on device config | 2 | M | Closes a real latent double-count |
| 5 | `dropout_rate`, `coverage_fraction` on cycle energy | 3 | S | Cheap once 3 lands |
| 6 | `appliance_type` with inheritance, per-type durations | 1 | L | Wants 1 to prove it helps |
| 7 | NILM-Metadata export | 6.1 | M | Independent; do when 3 and 4 are settled |
| 8 | `unattributed_load_started` from residual step changes | 6.2 | S | Independent, no ML, fits existing event model |

---

## Sources

- <https://github.com/nilmtk/nilmtk> — NILMTK core
- <https://github.com/nilmtk/nilm_metadata> — NILM Metadata
- <https://github.com/nilmtk/nilmtk-contrib> — models
- <https://github.com/nilmtk/nilmbench> — NILMBench2026 (BuildSys '26)
- <https://nilmtk.github.io/> — ecosystem start page
