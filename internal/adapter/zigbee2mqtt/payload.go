package zigbee2mqtt

import (
	"encoding/json"
	"strings"

	"github.com/sweeney/statehouse/internal/model"
)

// rawDevicePayload mirrors the set of fields Z2M typically publishes
// for a device under zigbee2mqtt/<friendly_name>. Pointer fields keep
// the "absent vs zero" distinction.
type rawDevicePayload struct {
	State        *string  `json:"state"`
	Power        *float64 `json:"power"`
	Voltage      *float64 `json:"voltage"`
	Current      *float64 `json:"current"`
	Energy       *float64 `json:"energy"`
	Temperature  *float64 `json:"temperature"`
	Humidity     *float64 `json:"humidity"`
	LinkQuality *int     `json:"linkquality"`
	Battery     *float64 `json:"battery"`
	Occupancy   *bool    `json:"occupancy"`
	Contact     *bool    `json:"contact"`
	// last_seen is deliberately omitted — devices send it as either a
	// unix-millisecond integer or a string depending on Z2M config, and
	// we derive timing from the engine clock rather than the payload.
}

// ParseDevicePayload turns a Z2M device payload into a Reading. Any
// unparseable input returns an empty Reading and an error; an empty
// JSON object {} is a valid input that produces an empty reading.
func ParseDevicePayload(b []byte) (model.Reading, error) {
	var raw rawDevicePayload
	if err := json.Unmarshal(b, &raw); err != nil {
		return model.Reading{}, err
	}
	r := model.Reading{}
	if raw.State != nil {
		s := strings.ToUpper(strings.TrimSpace(*raw.State))
		r.State = &s
	}
	r.PowerW = raw.Power
	r.VoltageV = raw.Voltage
	r.CurrentA = raw.Current
	r.EnergyKWh = raw.Energy
	r.TemperatureC = raw.Temperature
	r.HumidityPct = raw.Humidity
	r.LinkQuality = raw.LinkQuality
	r.Battery = raw.Battery
	return r, nil
}

// TopicFriendlyName extracts the friendly name from a topic of the
// form "<base>/<friendly>" or "<base>/<friendly>/<sub>". It returns
// "" if topic does not begin with base or has no friendly segment.
//
// It also rejects bridge-management topics ("bridge/...") which are
// not per-device payloads.
func TopicFriendlyName(base, topic string) string {
	prefix := base + "/"
	if !strings.HasPrefix(topic, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(topic, prefix)
	if rest == "" {
		return ""
	}
	// Bridge-management topics: bridge/devices, bridge/state, etc.
	if strings.HasPrefix(rest, "bridge/") || rest == "bridge" {
		return ""
	}
	// Strip trailing sub-topics such as /availability or /set.
	if i := strings.Index(rest, "/"); i >= 0 {
		// Treat /availability as a special-case sub-topic later; the
		// friendly name is the leading segment.
		return rest[:i]
	}
	return rest
}

// AvailabilityFromTopic returns the friendly name and true if the
// topic is "<base>/<friendly>/availability".
func AvailabilityFromTopic(base, topic string) (string, bool) {
	prefix := base + "/"
	if !strings.HasPrefix(topic, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(topic, prefix)
	if !strings.HasSuffix(rest, "/availability") {
		return "", false
	}
	name := strings.TrimSuffix(rest, "/availability")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// ParseAvailability interprets a Z2M availability payload. Z2M can
// either send a bare string ("online"/"offline") or a JSON object
// {"state":"online"}.
func ParseAvailability(b []byte) (model.Availability, bool) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return model.AvailabilityUnknown, false
	}
	if s[0] == '{' {
		var obj struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(b, &obj); err == nil {
			return availFromString(obj.State)
		}
	}
	// Strip surrounding quotes if present.
	s = strings.Trim(s, `"`)
	return availFromString(s)
}

func availFromString(s string) (model.Availability, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "online":
		return model.AvailabilityOnline, true
	case "offline":
		return model.AvailabilityOffline, true
	}
	return model.AvailabilityUnknown, false
}
