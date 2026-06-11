package zigbee2mqtt

import (
	"testing"
)

func TestParseDevicePayload_MissingFieldsAreNil(t *testing.T) {
	r, err := ParseDevicePayload([]byte(`{"state":"ON","linkquality":87}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.PowerW != nil {
		t.Fatalf("expected PowerW to be nil, got %v", *r.PowerW)
	}
	if r.EnergyKWh != nil {
		t.Fatalf("expected EnergyKWh to be nil")
	}
	if r.State == nil || *r.State != "ON" {
		t.Fatalf("state not preserved: %v", r.State)
	}
	if r.LinkQuality == nil || *r.LinkQuality != 87 {
		t.Fatalf("linkquality not preserved")
	}
}

// TestParseDevicePayload_AlarmFields verifies a HEIMAN smoke-detector
// payload decodes alarm_1 → Smoke and tamper → Tamper (real shape from
// docs/firealarm_office-sensor-report.md §5.4).
func TestParseDevicePayload_AlarmFields(t *testing.T) {
	r, err := ParseDevicePayload([]byte(`{"alarm_1":true,"alarm_2":false,"battery":100,"battery_low":false,"linkquality":160,"tamper":false,"temperature":23.57}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Smoke == nil || !*r.Smoke {
		t.Fatalf("expected Smoke=true from alarm_1, got %v", r.Smoke)
	}
	if r.Tamper == nil || *r.Tamper {
		t.Fatalf("expected Tamper=false present, got %v", r.Tamper)
	}
	if !r.HasAnyMeasurement() {
		t.Fatalf("alarm reading must count as a measurement")
	}
}

// TestParseDevicePayload_PartialPayloadAbsentIsNotCleared pins the
// safety-critical "absent ≠ cleared" guarantee: these detectors emit
// partial per-cluster payloads (battery/temperature only, no alarm_*).
// A missing alarm_1 must stay nil so the engine never reads a routine
// battery report as "smoke cleared". Real shape from §7.1.
func TestParseDevicePayload_PartialPayloadAbsentIsNotCleared(t *testing.T) {
	r, err := ParseDevicePayload([]byte(`{"battery":100,"linkquality":120,"temperature":21.3}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Smoke != nil {
		t.Fatalf("absent alarm_1 must stay nil (not false), got %v", *r.Smoke)
	}
	if r.Tamper != nil {
		t.Fatalf("absent tamper must stay nil, got %v", *r.Tamper)
	}
}

func TestParseDevicePayload_ZeroIsDistinct(t *testing.T) {
	r, err := ParseDevicePayload([]byte(`{"power":0,"energy":0}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.PowerW == nil || *r.PowerW != 0 {
		t.Fatalf("expected PowerW to be present and zero")
	}
	if r.EnergyKWh == nil || *r.EnergyKWh != 0 {
		t.Fatalf("expected EnergyKWh to be present and zero")
	}
}

func TestTopicFriendlyName(t *testing.T) {
	cases := []struct {
		topic string
		want  string
	}{
		{"zigbee2mqtt/kitchen_dishwasher", "kitchen_dishwasher"},
		{"zigbee2mqtt/kitchen_dishwasher/availability", "kitchen_dishwasher"},
		{"zigbee2mqtt/bridge/devices", ""},
		{"zigbee2mqtt/bridge", ""},
		{"home/sensors/foo", ""},
	}
	for _, c := range cases {
		got := TopicFriendlyName("zigbee2mqtt", c.topic)
		if got != c.want {
			t.Errorf("topic=%q want=%q got=%q", c.topic, c.want, got)
		}
	}
}

// TestTopicFriendlyName_RejectLongRandomString verifies that a topic
// containing a 100-character random string (the DoS vector closed by
// issue #33) is rejected and returns "".
func TestTopicFriendlyName_RejectLongRandomString(t *testing.T) {
	// 100-character random-looking string — well above the 64-char limit.
	longRandom := "aB3xQrKzPmLvNwOuTsYeHfGdCjIiJlRnVbUqWoAkEhSyXtDpFcMgZaBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcd"
	topic := "zigbee2mqtt/" + longRandom
	got := TopicFriendlyName("zigbee2mqtt", topic)
	if got != "" {
		t.Errorf("expected empty string for oversized random friendly name, got %q", got)
	}
}

// TestTopicFriendlyName_AcceptValidFriendlyName verifies that a
// well-formed friendly name (alphanumeric, underscores, dots, slashes)
// within the 64-char limit is accepted.
func TestTopicFriendlyName_AcceptValidFriendlyName(t *testing.T) {
	cases := []string{
		"kitchen_kettle",
		"boiler-relay",
		"sensor.living_room",
		"device64charname012345678901234567890123456789012345678901234567", // exactly 64 chars
	}
	for _, name := range cases {
		topic := "zigbee2mqtt/" + name
		got := TopicFriendlyName("zigbee2mqtt", topic)
		if got == "" {
			t.Errorf("valid friendly name %q was rejected", name)
		}
	}
}

func TestAvailabilityFromTopic(t *testing.T) {
	name, ok := AvailabilityFromTopic("zigbee2mqtt", "zigbee2mqtt/kettle/availability")
	if !ok || name != "kettle" {
		t.Fatalf("expected (kettle,true), got (%q,%v)", name, ok)
	}
	if _, ok := AvailabilityFromTopic("zigbee2mqtt", "zigbee2mqtt/kettle"); ok {
		t.Fatalf("non-availability topic should not match")
	}
}

func TestParseAvailability(t *testing.T) {
	a, ok := ParseAvailability([]byte(`online`))
	if !ok || string(a) != "online" {
		t.Fatalf("bare string online: got %v ok=%v", a, ok)
	}
	a, ok = ParseAvailability([]byte(`{"state":"offline"}`))
	if !ok || string(a) != "offline" {
		t.Fatalf("json offline: got %v ok=%v", a, ok)
	}
	if _, ok := ParseAvailability([]byte(`{}`)); ok {
		t.Fatalf("empty object should not be a valid availability")
	}
}

// TestParseDevicePayload_LastSeenIntegerIgnored verifies that a payload with
// last_seen as a unix-millisecond integer (a common Z2M config) does not cause
// a parse error. The field is intentionally absent from rawDevicePayload, so
// JSON unmarshal silently ignores it and returns the rest of the reading.
func TestParseDevicePayload_LastSeenIntegerIgnored(t *testing.T) {
	r, err := ParseDevicePayload([]byte(`{"last_seen":1700000000000,"power":42}`))
	if err != nil {
		t.Fatalf("expected permissive parse (last_seen ignored), got error: %v", err)
	}
	if r.PowerW == nil || *r.PowerW != 42 {
		t.Fatalf("expected PowerW=42, got %v", r.PowerW)
	}
}

// TestParseDevicePayload_ErrorWrapped verifies that parse errors returned by
// ParseDevicePayload are wrapped with a "zigbee2mqtt:" prefix for context.
func TestParseDevicePayload_ErrorWrapped(t *testing.T) {
	_, err := ParseDevicePayload([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if err.Error()[:len("zigbee2mqtt:")] != "zigbee2mqtt:" {
		t.Errorf("expected error to be wrapped with zigbee2mqtt: prefix, got: %v", err)
	}
}

func TestParseBridgeDevices_FiltersCoordinator(t *testing.T) {
	raw := []byte(`[
		{"ieee_address":"0x00000000","friendly_name":"coordinator","type":"Coordinator"},
		{"ieee_address":"0x00158d0000000001","friendly_name":"kettle","type":"EndDevice"}
	]`)
	devs, err := ParseBridgeDevices(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(devs) != 1 || devs[0].FriendlyName != "kettle" {
		t.Fatalf("unexpected bridge devices: %+v", devs)
	}
}
