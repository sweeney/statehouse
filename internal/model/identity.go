package model

// DeviceIdentity identifies a physical device. The IEEE address is the
// stable key; friendly name and source topic are mutable metadata that
// must not be relied on as identity.
type DeviceIdentity struct {
	IEEEAddress  string `json:"ieee_address,omitempty"`
	FriendlyName string `json:"friendly_name,omitempty"`
}

// Key returns the stable identity key for this device. Prefer the IEEE
// address; fall back to friendly name only when nothing else is known
// (e.g. a non-Zigbee source).
func (d DeviceIdentity) Key() string {
	if d.IEEEAddress != "" {
		return d.IEEEAddress
	}
	return d.FriendlyName
}
