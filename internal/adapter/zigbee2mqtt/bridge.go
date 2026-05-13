package zigbee2mqtt

import (
	"encoding/json"
)

// BridgeDevice is one entry in the retained zigbee2mqtt/bridge/devices
// payload. We only decode the fields the engine needs.
type BridgeDevice struct {
	IEEEAddress  string `json:"ieee_address"`
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	ModelID      string `json:"model_id,omitempty"`
	Description  string `json:"description,omitempty"`
	Interviewing bool   `json:"interviewing,omitempty"`
	Definition   *struct {
		Model       string `json:"model,omitempty"`
		Vendor      string `json:"vendor,omitempty"`
		Description string `json:"description,omitempty"`
	} `json:"definition,omitempty"`
}

// ParseBridgeDevices decodes the bridge/devices retained payload.
func ParseBridgeDevices(b []byte) ([]BridgeDevice, error) {
	var devs []BridgeDevice
	if err := json.Unmarshal(b, &devs); err != nil {
		return nil, err
	}
	// Filter out non-device entries such as the coordinator and
	// anything still interviewing. Coordinator type is reported as
	// "Coordinator"; endpoints as "EndDevice"/"Router".
	out := make([]BridgeDevice, 0, len(devs))
	for _, d := range devs {
		if d.Type == "Coordinator" {
			continue
		}
		if d.IEEEAddress == "" {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}
