package config

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Issue #86: a statehouse_behaviour section that names only some of its keys
// used to be decoded into a zero struct and then swapped in whole, so every
// key it left out became zero rather than keeping its default. Production's
// house block names only quiet_after and empty_after, which silently set
// sleeping_after to 0 and meant the house could never enter sleeping mode.
//
// The tests below pin the fixed behaviour: a section is decoded *over* the
// value cfg already holds (Default() at startup and on every SIGHUP, since
// the local file cannot set these sections), so
//
//   - a key that is absent keeps its current value;
//   - a key that is present replaces it, including an explicit zero;
//   - a document that does not decode changes nothing at all.

// applyBehaviourDoc serves doc as statehouse_behaviour (with empty classes and
// devices namespaces) and applies it to cfg through the real Fetcher, the way
// startup and SIGHUP do.
func applyBehaviourDoc(t *testing.T, cfg *Config, doc string) error {
	t.Helper()
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveRaw(mux, "statehouse_behaviour", []byte(doc))
	cfg.Site.DevicesNamespace = "devices_test"
	return newTestFetcher(t, mux).ApplyRemote(context.Background(), cfg)
}

// The exact house block production runs with (see the issue). Before the
// fix this left SleepingAfter at 0.
func TestBehaviour_ProductionHouseBlockKeepsSleepingAfter(t *testing.T) {
	cfg := Default()
	if err := applyBehaviourDoc(t, &cfg, `{"house":{"empty_after":"2h","quiet_after":"30m"}}`); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.House.SleepingAfter != 2*time.Hour {
		t.Errorf("sleeping_after = %v, want the 2h default: an absent key must keep its default (#86)", cfg.House.SleepingAfter)
	}
	// The keys that were present still apply.
	if cfg.House.EmptyAfter != 2*time.Hour {
		t.Errorf("empty_after = %v, want 2h from the document", cfg.House.EmptyAfter)
	}
	if cfg.House.QuietAfter != 30*time.Minute {
		t.Errorf("quiet_after = %v, want 30m from the document", cfg.House.QuietAfter)
	}
}

// house.timezone used to be assigned unconditionally, so a block without it
// reset whatever cfg held to "" (UTC).
func TestBehaviour_AbsentTimezoneKeepsCurrentValue(t *testing.T) {
	cfg := Default()
	cfg.House.Timezone = "Europe/London"
	if err := applyBehaviourDoc(t, &cfg, `{"house":{"quiet_after":"20m"}}`); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.House.Timezone != "Europe/London" {
		t.Errorf("timezone = %q, want Europe/London kept: the document does not name it", cfg.House.Timezone)
	}
}

// Present-but-empty is a deliberate choice of UTC, and is honoured.
func TestBehaviour_ExplicitEmptyTimezoneMeansUTC(t *testing.T) {
	cfg := Default()
	cfg.House.Timezone = "Europe/London"
	if err := applyBehaviourDoc(t, &cfg, `{"house":{"timezone":""}}`); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.House.Timezone != "" {
		t.Errorf("timezone = %q, want \"\" (UTC) as written", cfg.House.Timezone)
	}
	if cfg.House.Location() != time.UTC {
		t.Errorf("Location() = %v, want UTC", cfg.House.Location())
	}
}

// docs/remote-config.md documents `sleeping_after: 0` as "never enters
// sleeping". Before the fix that worked only by accident (the zero struct);
// skipping zero durations over a non-zero default would have silently broken
// it, so an explicit zero must be applied, not treated as "absent".
func TestBehaviour_ExplicitZeroDurationIsApplied(t *testing.T) {
	cfg := Default()
	if err := applyBehaviourDoc(t, &cfg, `{
		"house":        {"sleeping_after":"0s"},
		"availability": {"offline_debounce":"0s"},
		"energy":       {"max_integration_gap":"0s", "divergence_warning_pct": 0}
	}`); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.House.SleepingAfter != 0 {
		t.Errorf("sleeping_after = %v, want 0 as written", cfg.House.SleepingAfter)
	}
	if cfg.Availability.OfflineDebounce != 0 {
		t.Errorf("offline_debounce = %v, want 0 as written", cfg.Availability.OfflineDebounce)
	}
	if cfg.Energy.MaxIntegrationGap != 0 {
		t.Errorf("max_integration_gap = %v, want 0 as written", cfg.Energy.MaxIntegrationGap)
	}
	if cfg.Energy.DivergenceWarningPct != 0 {
		t.Errorf("divergence_warning_pct = %v, want 0 as written", cfg.Energy.DivergenceWarningPct)
	}
	// Keys the document left out are untouched.
	if cfg.House.QuietAfter != 30*time.Minute || cfg.House.EmptyAfter != 6*time.Hour {
		t.Errorf("house = %+v, want quiet_after/empty_after left at their defaults", cfg.House)
	}
}

// energy.divergence_warning_pct was read as a plain float64, so an energy
// block that named only max_integration_gap zeroed it, and every cycle with
// any counter/integration difference would raise a divergence warning.
func TestBehaviour_EnergyKeyLeftOutKeepsDefault(t *testing.T) {
	t.Run("only max_integration_gap", func(t *testing.T) {
		cfg := Default()
		if err := applyBehaviourDoc(t, &cfg, `{"energy":{"max_integration_gap":"45m"}}`); err != nil {
			t.Fatalf("ApplyRemote() = %v", err)
		}
		if cfg.Energy.DivergenceWarningPct != 20 {
			t.Errorf("divergence_warning_pct = %v, want the default 20", cfg.Energy.DivergenceWarningPct)
		}
		if cfg.Energy.MaxIntegrationGap != 45*time.Minute {
			t.Errorf("max_integration_gap = %v, want 45m from the document", cfg.Energy.MaxIntegrationGap)
		}
	})
	t.Run("only divergence_warning_pct", func(t *testing.T) {
		cfg := Default()
		if err := applyBehaviourDoc(t, &cfg, `{"energy":{"divergence_warning_pct":15}}`); err != nil {
			t.Fatalf("ApplyRemote() = %v", err)
		}
		if cfg.Energy.MaxIntegrationGap != 30*time.Minute {
			t.Errorf("max_integration_gap = %v, want the default 30m", cfg.Energy.MaxIntegrationGap)
		}
		if cfg.Energy.DivergenceWarningPct != 15 {
			t.Errorf("divergence_warning_pct = %v, want 15 from the document", cfg.Energy.DivergenceWarningPct)
		}
	})
}

// An empty (or null) section is present but names nothing, so every key
// keeps its default.
func TestBehaviour_EmptyOrNullSectionKeepsDefaults(t *testing.T) {
	for _, doc := range []string{
		`{"energy":{},"availability":{},"house":{}}`,
		`{"energy":null,"availability":null,"house":null}`,
	} {
		cfg := Default()
		want := Default()
		if err := applyBehaviourDoc(t, &cfg, doc); err != nil {
			t.Fatalf("%s: ApplyRemote() = %v", doc, err)
		}
		if cfg.Energy != want.Energy {
			t.Errorf("%s: energy = %+v, want defaults %+v", doc, cfg.Energy, want.Energy)
		}
		if cfg.Availability != want.Availability {
			t.Errorf("%s: availability = %+v, want defaults %+v", doc, cfg.Availability, want.Availability)
		}
		if cfg.House != want.House {
			t.Errorf("%s: house = %+v, want defaults %+v", doc, cfg.House, want.House)
		}
	}
}

// A document with one bad value is refused whole, as before: none of it —
// not even the sections that decoded cleanly — reaches cfg.
func TestBehaviour_BadValueChangesNothing(t *testing.T) {
	for name, doc := range map[string]string{
		"bad duration": `{"energy":{"max_integration_gap":"45m"},"house":{"sleeping_after":"soon"}}`,
		"bad timezone": `{"energy":{"max_integration_gap":"45m"},"house":{"timezone":"Europe/Lundun"}}`,
		"wrong type":   `{"energy":{"max_integration_gap":"45m"},"availability":{"offline_debounce":30}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			want := Default()
			if err := applyBehaviourDoc(t, &cfg, doc); err == nil {
				t.Fatal("ApplyRemote() = nil, want the document refused")
			}
			if cfg.Energy != want.Energy || cfg.Availability != want.Availability || cfg.House != want.House {
				t.Errorf("a refused document changed cfg: energy=%+v availability=%+v house=%+v",
					cfg.Energy, cfg.Availability, cfg.House)
			}
		})
	}
}

// Adapters keep their documented semantics: an adapter named with only
// `enabled` gets its protocol's default base_topic, and one left out is off
// (zigbee2mqtt on). For adapters the zero struct *is* the default, apart from
// base_topic, which fillDefaults supplies, so this pins behaviour rather than
// fixing it.
func TestBehaviour_PartialAdaptersBlockKeepsDefaults(t *testing.T) {
	cfg := Default()
	if err := applyBehaviourDoc(t, &cfg, `{"adapters":{"boiler":{"enabled":true}}}`); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if !cfg.Adapters.Boiler.IsEnabled() {
		t.Error("boiler: want enabled as written")
	}
	if cfg.Adapters.Boiler.BaseTopic != "energy/boiler/sensor" {
		t.Errorf("boiler base_topic = %q, want the default", cfg.Adapters.Boiler.BaseTopic)
	}
	if !cfg.Adapters.Zigbee2MQTT.IsEnabled() {
		t.Error("zigbee2mqtt: want on, the default when left out")
	}
	if cfg.Adapters.UPS.IsEnabled() {
		t.Error("ups: want off, the default when left out")
	}
}

// The section decoders themselves must decode over the value they are called
// on, not reset it: that is what lets the overlay seed them with the current
// config. Exercised directly so a regression points at the decoder.
func TestSectionUnmarshalDecodesOverExistingValue(t *testing.T) {
	h := HouseConfig{QuietAfter: time.Minute, EmptyAfter: time.Hour, SleepingAfter: 2 * time.Hour, Timezone: "Local"}
	if err := json.Unmarshal([]byte(`{"quiet_after":"5m"}`), &h); err != nil {
		t.Fatal(err)
	}
	if want := (HouseConfig{QuietAfter: 5 * time.Minute, EmptyAfter: time.Hour, SleepingAfter: 2 * time.Hour, Timezone: "Local"}); h != want {
		t.Errorf("house = %+v, want %+v", h, want)
	}

	e := EnergyConfig{DivergenceWarningPct: 20, MaxIntegrationGap: 30 * time.Minute}
	if err := json.Unmarshal([]byte(`{"max_integration_gap":"1h"}`), &e); err != nil {
		t.Fatal(err)
	}
	if want := (EnergyConfig{DivergenceWarningPct: 20, MaxIntegrationGap: time.Hour}); e != want {
		t.Errorf("energy = %+v, want %+v", e, want)
	}

	a := AvailabilityConfig{OfflineDebounce: 30 * time.Second}
	if err := json.Unmarshal([]byte(`{}`), &a); err != nil {
		t.Fatal(err)
	}
	if a.OfflineDebounce != 30*time.Second {
		t.Errorf("availability = %+v, want offline_debounce kept", a)
	}
}

// The adapters section must not be decoded into a shallow copy of
// cfg.Adapters: the copy would share each adapter's Enabled *bool, and
// decoding `"enabled": false` would write through it into the config the
// caller still holds (on SIGHUP, the running engine's base config).
func TestBehaviour_AdaptersDecodeDoesNotWriteThroughCfgPointers(t *testing.T) {
	on := true
	cfg := Default()
	cfg.Adapters.Boiler.Enabled = &on
	held := cfg.Adapters.Boiler.Enabled // what a previous copy of cfg would share

	if err := applyBehaviourDoc(t, &cfg, `{"adapters":{"boiler":{"enabled":false}}}`); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.Adapters.Boiler.IsEnabled() {
		t.Error("boiler: want off as written")
	}
	if !*held {
		t.Error("decoding wrote through a pointer shared with the previous config")
	}
}

// A refused document says which key was wrong, so the log line and /healthz
// point at the fix.
func TestBehaviour_BadDurationErrorNamesTheKey(t *testing.T) {
	cfg := Default()
	err := applyBehaviourDoc(t, &cfg, `{"house":{"sleeping_after":"soon"}}`)
	if err == nil || !strings.Contains(err.Error(), "sleeping_after") {
		t.Errorf("ApplyRemote() = %v, want an error naming sleeping_after", err)
	}
}
