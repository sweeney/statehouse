package config

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sweeney/common/devices"
)

// The documents in config/remote/ are what people copy into the config
// service, so they must apply through the real Fetcher without error, and
// carry no key that statehouse would silently ignore.

func readRemoteExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "remote", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

func serveRaw(mux *http.ServeMux, ns string, raw []byte) {
	mux.HandleFunc("/api/v1/config/"+ns, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw) //nolint:errcheck
	})
}

func TestRemoteExamplesApply(t *testing.T) {
	mux := http.NewServeMux()
	serveRaw(mux, "statehouse_behaviour", readRemoteExample(t, "statehouse_behaviour.json"))
	serveRaw(mux, "statehouse_classes", readRemoteExample(t, "statehouse_classes.json"))
	serveRaw(mux, "devices_example", readRemoteExample(t, "devices_example.json"))

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_example"
	if err := newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if len(cfg.Devices) == 0 || len(cfg.DeviceClasses) == 0 {
		t.Fatalf("examples applied nothing: %d devices, %d classes", len(cfg.Devices), len(cfg.DeviceClasses))
	}
	if !cfg.Adapters.Zigbee2MQTT.IsEnabled() {
		t.Error("example behaviour leaves zigbee2mqtt off")
	}
}

// The devices example decodes with no problems and passes everything
// devices.Check reports: the placement vocabulary, identity, duplicates and
// part_of. Decode reports unknown keys as problems, so a misspelt key (flor:,
// cover:) fails here rather than being read as "unknown".
func TestRemoteDevicesExamplePassesCheck(t *testing.T) {
	devs, problems, err := devices.Decode(readRemoteExample(t, "devices_example.json"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range append(problems, devices.Check(devs)...) {
		t.Errorf("devices_example.json: %s", p)
	}
}

// strictDecode decodes raw into into, refusing any key into has no field for.
func strictDecode(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// Classes have no Decode of their own, so their keys are checked strictly
// here. Behaviour needs no test-only checker: the production decoder
// (overlayBehaviour) refuses unknown keys at every level, so applying the
// example is the check. The cases it must catch are in refusedCases.
func TestRemoteBehaviourAndClassesExamplesHaveNoUnknownKeys(t *testing.T) {
	if _, err := overlayBehaviour(Default(), readRemoteExample(t, "statehouse_behaviour.json")); err != nil {
		t.Errorf("statehouse_behaviour.json: %v", err)
	}
	if err := strictDecode(readRemoteExample(t, "statehouse_classes.json"), &map[string]DeviceClassConfig{}); err != nil {
		t.Errorf("statehouse_classes.json: %v", err)
	}
}

// The local example holds only local sections, so Load would accept it. (Load
// itself would also read influx.token_file, a path that exists only on a
// deployed host, so the section check is called directly.)
func TestExampleConfigHasNoRemoteOnlySections(t *testing.T) {
	path := filepath.Join("..", "..", "config", "config.example.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectRemoteOnlySections(raw, path); err != nil {
		t.Fatal(err)
	}
}
