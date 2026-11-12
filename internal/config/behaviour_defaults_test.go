package config

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Issue #86: a statehouse_behaviour section that names only some of its keys
// was decoded into a zero struct, so every key it left out became zero rather
// than keeping its default. Production's house block names only quiet_after
// and empty_after, which made sleeping_after 0s: the house could never enter
// sleeping mode. Each present section is now decoded over a copy of
// Default()'s section, so a key left out (or null) keeps its default.

// applyBehaviourDoc serves doc as statehouse_behaviour, with empty classes and
// devices namespaces, and applies it to cfg the way startup and SIGHUP do.
func applyBehaviourDoc(t *testing.T, cfg *Config, doc string) {
	t.Helper()
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveRaw(mux, "statehouse_behaviour", []byte(doc))
	cfg.Site.DevicesNamespace = "devices_test"
	if err := newTestFetcher(t, mux).ApplyRemote(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyRemote(%s) = %v", doc, err)
	}
}

// The test #86 asks for: production's exact house block, over Default().
func TestBehaviour_HouseKeyLeftOutKeepsDefault(t *testing.T) {
	cfg := Default()
	applyBehaviourDoc(t, &cfg, `{"house":{"empty_after":"2h","quiet_after":"30m"}}`)

	want := Default().House
	want.EmptyAfter, want.QuietAfter = 2*time.Hour, 30*time.Minute
	if cfg.House != want {
		t.Errorf("house = %+v, want %+v: sleeping_after and timezone must keep their defaults", cfg.House, want)
	}
}

func TestBehaviour_EnergyKeyLeftOutKeepsDefault(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		edit func(*EnergyConfig)
	}{
		{`{"energy":{"max_integration_gap":"45m"}}`, func(e *EnergyConfig) { e.MaxIntegrationGap = 45 * time.Minute }},
		{`{"energy":{"divergence_warning_pct":15}}`, func(e *EnergyConfig) { e.DivergenceWarningPct = 15 }},
		{`{"energy":{"divergence_warning_pct":null,"max_integration_gap":null}}`, func(*EnergyConfig) {}},
	} {
		cfg := Default()
		applyBehaviourDoc(t, &cfg, tc.doc)
		want := Default().Energy
		tc.edit(&want)
		if cfg.Energy != want {
			t.Errorf("%s: energy = %+v, want %+v", tc.doc, cfg.Energy, want)
		}
	}
}

func TestBehaviour_AvailabilityKeyLeftOutKeepsDefault(t *testing.T) {
	for _, doc := range []string{`{"availability":{}}`, `{"availability":{"offline_debounce":null}}`} {
		cfg := Default()
		applyBehaviourDoc(t, &cfg, doc)
		if want := Default().Availability; cfg.Availability != want {
			t.Errorf("%s: availability = %+v, want %+v", doc, cfg.Availability, want)
		}
	}
}

// An explicit zero is a value, not "unset": sleeping_after "0s" is documented
// as "never enter sleeping", so decoding over a non-zero default must not
// skip it.
func TestBehaviour_ExplicitZeroApplies(t *testing.T) {
	cfg := Default()
	applyBehaviourDoc(t, &cfg, `{"house":{"sleeping_after":"0s"},"availability":{"offline_debounce":"0s"}}`)
	if cfg.House.SleepingAfter != 0 || cfg.Availability.OfflineDebounce != 0 {
		t.Errorf("sleeping_after = %v, offline_debounce = %v; want both 0 as written",
			cfg.House.SleepingAfter, cfg.Availability.OfflineDebounce)
	}
}

// SIGHUP: document A sets sleeping_after, then document B drops it. B must
// give the default, not A's value, whether it is applied onto a fresh base
// (what watchSIGHUP does) or onto the config A produced.
func TestBehaviour_KeyDroppedOnReloadReturnsToDefault(t *testing.T) {
	running := Default()
	applyBehaviourDoc(t, &running, `{"house":{"sleeping_after":"1h"}}`)
	if running.House.SleepingAfter != time.Hour {
		t.Fatalf("after A: sleeping_after = %v, want 1h", running.House.SleepingAfter)
	}

	for name, cfg := range map[string]Config{"fresh base": Default(), "running config": running} {
		applyBehaviourDoc(t, &cfg, `{"house":{"quiet_after":"20m"}}`)
		if want := Default().House.SleepingAfter; cfg.House.SleepingAfter != want {
			t.Errorf("%s: sleeping_after = %v after B dropped it, want the default %v", name, cfg.House.SleepingAfter, want)
		}
	}
}
