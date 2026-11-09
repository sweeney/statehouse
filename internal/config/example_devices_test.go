package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sweeney/common/devices"
)

// The example config is what people copy to write their own, so its devices
// must decode cleanly and pass everything devices.Check reports: the
// placement vocabulary (no deprecated location, room ids shaped
// "<floor>.<room>", a floor that agrees with its room, a covers that is
// "house" or a room id) and also identity, duplicate and part_of problems.
//
// Devices are decoded strictly, so a misspelt key (flor:, cover:) fails here
// instead of being silently dropped and read as "unknown". Only the devices:
// subtree is strict; the rest of the file is not, and need not be.
//
// The commented-out entries (office_heater) are not parsed, so nothing here
// covers them; keep them in step by hand.
func TestExampleConfigDevicesPassCheck(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	devs, err := decodeDevicesStrict(raw)
	if err != nil {
		t.Fatalf("example config devices: %v", err)
	}
	if len(devs) == 0 {
		t.Fatal("the example config lists no devices; this test would pass vacuously")
	}
	for _, p := range devices.Check(devs) {
		t.Errorf("example config: %s", p)
	}
}

// The strict decode is what catches a misspelt key; Check alone does not,
// because a missing floor or covers means "unknown", not a problem.
func TestDecodeDevicesStrictRejectsUnknownKeys(t *testing.T) {
	for _, key := range []string{"flor", "cover"} {
		raw := []byte("devices:\n  loft_sensor:\n    ieee_address: \"0x01\"\n    room: loft.bedroom\n    " + key + ": x\n")
		_, err := decodeDevicesStrict(raw)
		if err == nil || !strings.Contains(err.Error(), "field "+key+" not found") {
			t.Errorf("%s: want an unknown-field error, got %v", key, err)
		}
	}
}

// decodeDevicesStrict decodes the devices: subtree of a config document,
// rejecting unknown keys. yaml.Node.Decode ignores KnownFields, so the node
// is re-marshalled and read through a Decoder.
func decodeDevicesStrict(raw []byte) (map[string]DeviceConfig, error) {
	var doc struct {
		Devices yaml.Node `yaml:"devices"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	sub, err := yaml.Marshal(&doc.Devices)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(sub))
	dec.KnownFields(true)
	var devs map[string]DeviceConfig
	if err := dec.Decode(&devs); err != nil {
		return nil, err
	}
	return devs, nil
}
