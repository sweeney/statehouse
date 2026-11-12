package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// The example names every house key. Since #86 a key left out would keep
	// its default anyway, but the example shows the full set to copy from.
	if cfg.House.SleepingAfter == 0 || cfg.House.QuietAfter == 0 || cfg.House.EmptyAfter == 0 {
		t.Errorf("example house block leaves a duration unset: %+v", cfg.House)
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

// checkBehaviourKeys refuses any key statehouse would ignore in a
// statehouse_behaviour document. A plain strict decode into behaviourDoc is
// not enough: energy, availability and house have their own UnmarshalJSON,
// which a decoder's DisallowUnknownFields does not reach, so each of those
// sections is decoded strictly into the struct its UnmarshalJSON reads.
func checkBehaviourKeys(raw []byte) error {
	var top struct {
		Energy       json.RawMessage `json:"energy"`
		Availability json.RawMessage `json:"availability"`
		House        json.RawMessage `json:"house"`
		Adapters     *AdaptersConfig `json:"adapters"`
	}
	if err := strictDecode(raw, &top); err != nil {
		return err
	}
	for _, s := range []struct {
		name string
		raw  json.RawMessage
		into any
	}{
		{"energy", top.Energy, &energyJSON{}},
		{"availability", top.Availability, &availabilityJSON{}},
		{"house", top.House, &houseJSON{}},
	} {
		if s.raw == nil {
			continue
		}
		if err := strictDecode(s.raw, s.into); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// The check itself must catch a key inside a custom-unmarshalled section: the
// old example's energy.electricity block was never a real field.
func TestCheckBehaviourKeysCatchesNestedUnknownKey(t *testing.T) {
	for _, doc := range []string{
		`{"energy":{"divergence_warning_pct":20,"electricity":{"idle_after":"5m"}}}`,
		`{"availability":{"offline_debounc":"30s"}}`,
		`{"house":{"sleeping_afer":"2h"}}`,
		`{"adapters":{"tanktemp":{"enabled":true,"base_topc":"x"}}}`,
		`{"hous":{}}`,
	} {
		if err := checkBehaviourKeys([]byte(doc)); err == nil {
			t.Errorf("checkBehaviourKeys(%s) = nil; want the unknown key refused", doc)
		}
	}
}

// Behaviour and classes have no Decode of their own, so their keys are
// checked strictly here, down to each section's fields.
func TestRemoteBehaviourAndClassesExamplesHaveNoUnknownKeys(t *testing.T) {
	if err := checkBehaviourKeys(readRemoteExample(t, "statehouse_behaviour.json")); err != nil {
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
