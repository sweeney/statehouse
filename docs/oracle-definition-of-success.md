# Statehouse: Definition of Success

## Purpose

Statehouse is successful when it transforms noisy, sparse, device-specific house telemetry into stable, explainable, human-meaningful state.

It should reduce complexity for every downstream consumer. Consumers should not need to understand Zigbee2MQTT quirks, retained messages, topic churn, sparse power reporting, hysteresis, appliance energy strategies, or device-specific payload formats.

Statehouse is not successful merely because it subscribes to MQTT. It is successful when it produces useful canonical state and derived events that other systems can trust.

---

## North Star

> Statehouse is successful when downstream systems can reason about the house in terms of meaningful state and events rather than raw sensor telemetry.

Examples of meaningful state and events:

- `house_state = occupied`
- `house_state = quiet`
- `dishwasher = running`
- `dishwasher_cycle_finished`
- `kettle_recently_used`
- `living_room_media_active`
- `boiler_active`
- `energy_divergence_warning`

---

## V1 Scope

Statehouse V1 is a deterministic house state engine.

It should:

- Run as a Go daemon on Linux.
- Subscribe to configured MQTT topics.
- Discover Zigbee2MQTT devices where possible.
- Maintain current in-memory device and whole-house state.
- Publish derived state/events back to MQTT.
- Expose state through a JSON HTTP API.
- Write selected observations and derived metrics to InfluxDB.
- Maintain a bounded local JSONL recent-event log.
- Be testable through deterministic fixture replay.

V1 should model:

- Devices.
- Whole-house state.
- Appliance activity.
- Short-lived lifecycle events.
- Basic house occupancy/quiet/asleep-style states.

V1 should not model:

- Individual people.
- Person-specific location.
- Full room-level aggregation.
- Long-term behavioural prediction.
- AI/LLM reasoning.
- MCP.
- A dashboard.
- A general home automation rules engine.

---

## Core Success Criteria

### 1. Canonical state is stable and useful

Statehouse must maintain a current canonical state model that is more useful than the raw MQTT stream.

A downstream consumer should be able to ask:

- What is the current house state?
- Which devices are active?
- Which appliances are in a lifecycle?
- What derived events happened recently?
- Is the house occupied, empty, quiet, or asleep?
- Are there warnings or uncertain states?

Success means the HTTP API and MQTT outputs expose the same underlying truth.

---

### 2. Derived events are human-meaningful

Statehouse should emit events that describe house activity, not sensor mechanics.

Good derived events:

- `device_state_changed`
- `appliance_cycle_started`
- `appliance_cycle_finished`
- `kettle_used`
- `house_state_changed`
- `energy_divergence_warning`

Avoid treating these as useful end-user events:

- `mqtt_message_received`
- `power_above_threshold`
- `raw_payload_changed`

Low-level facts can exist internally, but derived outputs should be meaningful to another system.

---

### 3. Device identity survives topic churn

A Zigbee2MQTT friendly-name rename must not create a new logical device.

Statehouse should key device identity by stable identifiers such as `ieee_address`, not by MQTT topic or friendly name.

Success means:

- Device state remains continuous across renames.
- Topic/friendly name is treated as mutable metadata.
- The registry updates when Zigbee2MQTT republishes bridge device information.

---

### 4. Sparse and missing telemetry is handled correctly

Statehouse must treat real sensor streams as incomplete and irregular.

It should:

- Distinguish absent fields from zero values.
- Tolerate first-contact messages that contain only partial data.
- Avoid fabricating continuity across large telemetry gaps.
- Continue operating through malformed or partial payloads.
- Avoid state flapping due to single noisy readings.

Success means sparse reporting does not produce obviously false state or energy estimates.

---

### 5. Activity detection is robust

Statehouse should use power readings as the primary signal for appliance activity detection.

It should not rely on:

- Zigbee `state` alone.
- Current readings alone.
- Assumptions that idle power is zero.

It should support:

- Per-device or per-class idle thresholds.
- Hysteresis.
- Sustained active/idle windows.
- Config-backed device classes.
- Sensible defaults for discovered devices.

Success means devices do not flap between active and idle because of noise, compressor cycling, or retained/late messages.

---

### 6. Energy calculation uses two paths

Statehouse must not implement naive `watts × elapsed time` integration as the only energy source.

It should support both:

1. Reported energy counter deltas.
2. Power/time integration.

Where possible, both should run in parallel.

The primary strategy should be selected by device class:

- Short-burst devices, such as kettles and toasters: integration-first.
- Long-cycle devices, such as dishwashers, washing machines, dryers, fridges, and freezers: counter-first.
- Continuous devices: counter-first, with integration and duty-cycle observations retained for diagnostics.

If the two paths diverge significantly, this should be emitted as a first-class warning rather than hidden.

Success means the system can explain which energy source was selected and why.

---

### 7. Deterministic replay is an oracle

Given the same ordered fixture stream, Statehouse must produce the same derived state and events.

This is the main correctness oracle for the project.

Fixture replay should test:

- Normal appliance cycles.
- Short-burst appliance use.
- Fridge/freezer compressor cycles.
- Zigbee2MQTT bridge/device retained messages.
- Device rename/topic changes.
- Availability flicker during Zigbee2MQTT restart.
- Missing fields.
- Sparse power reporting.
- Large integration gaps.
- Malformed messages.
- MQTT reconnect behaviour where practical.

Success means fixture replay can be used for regression tests and future refactoring.

---

### 8. Inferences are explainable

Every inferred state should be traceable to evidence.

For example, an appliance state should be able to expose:

- Current state.
- Previous state.
- Confidence, if applicable.
- Last transition time.
- Evidence used.
- Thresholds or strategy used.
- Energy strategy used, if relevant.

Example:

```json
{
  "device_id": "kitchen_dishwasher",
  "state": "running",
  "evidence": {
    "power_w": 1480,
    "active_threshold_w": 20,
    "sustained_for_seconds": 30,
    "energy_strategy": "counter_primary"
  }
}
```

Success means downstream systems can explain why Statehouse believes something is true.

---

### 9. The service degrades gracefully

Statehouse should keep running when adjacent systems fail.

It should:

- Continue operating if InfluxDB is unavailable.
- Reconnect to MQTT.
- Debounce short offline/online device flickers.
- Avoid unbounded memory growth.
- Rotate or bound local JSONL history.
- Expose health status clearly.
- Log warnings without crashing on bad input.

Success means Statehouse can run unattended on a modest Linux box.

---

### 10. APIs expose canonical truth, not raw implementation detail

The HTTP API should present state and recent derived events.

Required V1 endpoints:

- `GET /healthz`
- `GET /state`
- `GET /state/house`
- `GET /state/devices`
- `GET /events/recent`
- `GET /metrics`

The API should avoid making raw MQTT topics the primary public interface.

MQTT derived outputs should include:

- `house/state/snapshot`
- `house/state/house`
- `house/state/devices/{device}`
- `house/events/derived`

Success means consumers can use the API/MQTT outputs without parsing raw Zigbee2MQTT payloads.

---

## Config and Discovery Success

Statehouse should use assisted discovery plus config-backed classification.

It may infer likely device classes from:

- Zigbee2MQTT bridge device data.
- Topic names.
- Friendly names.
- Payload shape.
- Observed power behaviour.

But committed behaviour should be deterministic and represented in config.

Success means:

- New devices can be discovered.
- Guesses can be reviewed or overridden.
- Behaviour is repeatable across restarts and tests.
- Adding a device should not require changing Go code.

---

## Testing Success

A coding agent should consider the project incomplete unless it has meaningful tests.

Required test categories:

- Unit tests for energy counter logic.
- Unit tests for integration and gap clamping.
- Unit tests for activity detection and hysteresis.
- Unit tests for registry/device identity behaviour.
- Unit tests for malformed and partial payloads.
- Fixture replay tests using anonymised real streams.
- HTTP handler tests.
- MQTT publish/subscribe integration tests where practical.

Success means the hard real-world cases are encoded in tests, not left as comments.

---

## Operational Success

Statehouse should be deployable as a normal Linux service.

Success means:

- It has clear configuration.
- It has structured logs.
- It has health checks.
- It can be run under systemd.
- It shuts down cleanly.
- It reconnects to MQTT.
- It does not require a database to start.
- It can run with InfluxDB disabled or unavailable.

---

## Non-Goals

Statehouse V1 is not:

- A home automation platform.
- A rule engine.
- A person tracker.
- A behavioural prediction engine.
- An LLM agent.
- An MCP server.
- A visual dashboard.
- A replacement for InfluxDB.
- A long-term data warehouse.
- A complete smart home operating system.

These may be built later on top of Statehouse.

---

## Prior Art

Inspiration may be taken from the existing `power-monitor` project:

https://github.com/sweeney/power-monitor

Useful lessons include:

- Zigbee2MQTT discovery.
- Device identity via stable addresses.
- Sparse power reporting.
- Hysteresis.
- Counter-vs-integration energy strategies.
- Divergence warnings.
- Fixture-based testing.

However, Statehouse should not be treated as a direct port of that project or constrained by its structure. The goal is broader: a house state engine, not only appliance power monitoring.

---

## Final Acceptance Statement

Statehouse V1 is successful when:

1. It can consume real MQTT telemetry from the house.
2. It can maintain stable in-memory canonical state.
3. It can publish meaningful derived events.
4. It can expose the same state over HTTP.
5. It can write selected observations and lifecycle metrics to InfluxDB.
6. It can survive real-world Zigbee2MQTT behaviour.
7. It can replay fixtures deterministically.
8. It can explain its inferred states.
9. It reduces complexity for all downstream consumers.

If downstream systems no longer need to understand raw sensor telemetry in order to reason about the house, Statehouse is doing its job.
