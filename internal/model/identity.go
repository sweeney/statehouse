package model

// DeviceIdentity identifies a physical device in a protocol-agnostic
// way. The pair (Scheme, Primary) is the stable key — Scheme names
// the adapter providing the device ("zigbee", "tasmota", "shelly",
// "homie", "topic", ...) and Primary is the stable identifier within
// that scheme (IEEE address for zigbee, MAC or topic stem for
// tasmota, etc.). Display is the human-readable mutable name (Z2M
// friendly_name, Tasmota friendly name, room+sensor for raw topics).
//
// Adapters MUST set Scheme. They SHOULD set Primary as soon as it is
// known — if a device shows up before its stable id can be resolved
// (e.g. a Z2M payload arrives before bridge/devices), the adapter
// falls back to Primary == Display so the engine still has a key. The
// engine's Store merges records when a real Primary is later learned.
type DeviceIdentity struct {
	Scheme  string `json:"scheme"`            // adapter name
	Primary string `json:"primary"`           // stable adapter-specific id
	Display string `json:"display,omitempty"` // human-readable mutable name
}

// Key returns the canonical stable key for this identity. It is what
// the Store uses to index records.
func (d DeviceIdentity) Key() string {
	if d.Primary != "" {
		return d.Scheme + ":" + d.Primary
	}
	if d.Display != "" {
		return d.Scheme + ":" + d.Display
	}
	return d.Scheme + ":unknown"
}

// DisplayKey returns the "scheme:display" key the Store uses as a
// secondary index, or "" if no display is set.
func (d DeviceIdentity) DisplayKey() string {
	if d.Display == "" {
		return ""
	}
	return d.Scheme + ":" + d.Display
}
