package config

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Everything that is not a connection setting comes only from config.swee.net.
// A local copy used to be silently overridden whenever the remote namespace
// had the same section, so an edit to the local file could do nothing at all
// with no sign why. Load refuses those sections, naming where they live.
func TestLoadRejectsRemoteOnlySections(t *testing.T) {
	cases := []struct {
		key, yaml, namespace string
	}{
		{"adapters", "adapters:\n  tanktemp:\n    enabled: true\n", "statehouse_behaviour"},
		{"energy", "energy:\n  divergence_warning_pct: 20\n", "statehouse_behaviour"},
		{"availability", "availability:\n  offline_debounce: 30s\n", "statehouse_behaviour"},
		{"house", "house:\n  quiet_after: 30m\n", "statehouse_behaviour"},
		{"device_classes", "device_classes:\n  ups_sensor:\n    name_hints: [ups]\n", "statehouse_classes"},
		{"devices", "devices:\n  kettle:\n    ieee_address: \"0x01\"\n", "devices_home"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			_, err := Load(writeConfig(t, siteBlock+c.yaml))
			if err == nil {
				t.Fatalf("Load() accepted a local %s: section; want an error", c.key)
			}
			for _, want := range []string{c.key, c.namespace} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q, so it cannot be acted on", err, want)
				}
			}
		})
	}
}

// Every offending section is named at once, not one per restart.
func TestLoadRejectsAllRemoteOnlySectionsTogether(t *testing.T) {
	_, err := Load(writeConfig(t, siteBlock+"energy:\n  divergence_warning_pct: 20\nhouse:\n  quiet_after: 30m\n"))
	if err == nil {
		t.Fatal("Load() accepted local energy: and house: sections")
	}
	for _, key := range []string{"energy", "house"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}
}

// Without the config service there is no source for devices, classes,
// adapters or behaviour, so a service with no remote_config or identity
// block would start, look healthy, and do nothing.
func TestValidateRequiresRemoteConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no remote_config.base_url", func(c *Config) { c.RemoteConfig.BaseURL = "" }, "remote_config.base_url"},
		{"no identity.base_url", func(c *Config) { c.Identity.BaseURL = "" }, "identity.base_url"},
		{"no identity.client_id", func(c *Config) { c.Identity.ClientID = "" }, "identity.client_id"},
		{"no identity.client_secret", func(c *Config) { c.Identity.ClientSecret = "" }, "identity.client_secret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := validConfig()
			c.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil with %s; want an error", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name %s", err, c.want)
			}
		})
	}
}

func TestValidateAcceptsRemoteConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// ApplyRemote reports failure, so startup can refuse to run and a SIGHUP
// reload can keep the running config, rather than either quietly carrying on
// with whatever the section held before.
func TestApplyRemoteReportsUnavailableNamespace(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/statehouse_classes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	err := newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg)
	if err == nil {
		t.Fatal("ApplyRemote() = nil with statehouse_classes unavailable; want an error")
	}
	if !strings.Contains(err.Error(), "statehouse_classes") {
		t.Errorf("error %q does not name the failed namespace", err)
	}
}

func TestApplyRemoteReportsTokenFailure(t *testing.T) {
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f := &Fetcher{BaseURL: "http://127.0.0.1:1", Tokens: &errorTokenSource{}}
	if err := f.ApplyRemote(context.Background(), &cfg); err == nil {
		t.Fatal("ApplyRemote() = nil after a token failure; want an error")
	}
}

func TestApplyRemoteReportsNoBaseURL(t *testing.T) {
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	if err := (&Fetcher{}).ApplyRemote(context.Background(), &cfg); err == nil {
		t.Fatal("ApplyRemote() = nil with no base URL; want an error: there is no local mode")
	}
}

func TestApplyRemoteSucceeds(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{})

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	if err := newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote() = %v, want nil", err)
	}
}

// Base topics were defaulted in Load, which no longer sees adapters. A remote
// adapter that is switched on without a base_topic still gets its default.
func TestApplyRemoteFillsAdapterBaseTopicDefaults(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"adapters": map[string]any{"tanktemp": map[string]any{"enabled": true}},
	})

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	if err := newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	want := map[string]string{
		"zigbee2mqtt": cfg.Adapters.Zigbee2MQTT.BaseTopic,
		"boiler":      cfg.Adapters.Boiler.BaseTopic,
		"ups":         cfg.Adapters.UPS.BaseTopic,
		"climate":     cfg.Adapters.Climate.BaseTopic,
		"meter":       cfg.Adapters.Meter.BaseTopic,
		"intercom":    cfg.Adapters.Intercom.BaseTopic,
		"tanktemp":    cfg.Adapters.Tanktemp.BaseTopic,
	}
	defaults := map[string]string{
		"zigbee2mqtt": "zigbee2mqtt",
		"boiler":      "energy/boiler/sensor",
		"ups":         "ups",
		"climate":     "climate",
		"meter":       "energy",
		"intercom":    "asterisk",
		"tanktemp":    "energy/hot_water",
	}
	for name, got := range want {
		if got != defaults[name] {
			t.Errorf("%s base_topic = %q, want default %q", name, got, defaults[name])
		}
	}
}

// Every field the local file cannot set (yaml:"-") must carry a remote tag,
// or Load would ignore a local copy of it silently rather than refuse it.
// (The keys themselves are checked against the spellings an operator would
// write by TestLoadRejectsRemoteOnlySections.)
func TestRemoteOnlyFieldsAreAllRefused(t *testing.T) {
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		f := ct.Field(i)
		_, remote := f.Tag.Lookup("remote")
		if local := f.Tag.Get("yaml") != "-"; local == remote {
			t.Errorf("Config.%s: yaml:%q with remote tag %v; a field is either local (yaml key) or remote (yaml:\"-\" and a remote tag)",
				f.Name, f.Tag.Get("yaml"), remote)
		}
	}
}

// The table in TestLoadRejectsRemoteOnlySections spells each key the way an
// operator would; the derived list must name exactly those, so a misspelt
// remote tag (device_class) fails here instead of letting device_classes:
// through again.
func TestRemoteOnlySectionsMatchOperatorSpellings(t *testing.T) {
	want := map[string]string{
		"adapters": "statehouse_behaviour", "energy": "statehouse_behaviour",
		"availability": "statehouse_behaviour", "house": "statehouse_behaviour",
		"device_classes": "statehouse_classes", "devices": "",
	}
	got := map[string]string{}
	for _, s := range remoteOnlySections {
		got[s.key] = s.namespace
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("remoteOnlySections = %v, want %v", got, want)
	}
}

// A remote house.timezone that time.LoadLocation cannot resolve used to apply
// cleanly and then fall back to UTC in Location(), putting the night/day
// window an hour out for half the year with /healthz still ok. The behaviour
// document is refused instead: startup stops, and a SIGHUP keeps the running
// config.
func TestApplyRemoteRejectsInvalidTimezone(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"house": map[string]any{"quiet_after": "30m", "timezone": "Europe/Londn"},
	})

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	before := cfg.House
	f := newTestFetcher(t, mux)
	err := f.ApplyRemote(context.Background(), &cfg)
	if err == nil {
		t.Fatal("ApplyRemote() = nil with house.timezone \"Europe/Londn\"; want an error")
	}
	for _, want := range []string{"statehouse_behaviour", "house.timezone", "Europe/Londn"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if cfg.House != before {
		t.Errorf("House = %+v after a rejected document, want it unchanged %+v", cfg.House, before)
	}
	if f.Statuses()["statehouse_behaviour"].OK {
		t.Error("/healthz would report statehouse_behaviour ok after rejecting it")
	}
}

func TestApplyRemoteAcceptsValidTimezone(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	serveNamespace(mux, "statehouse_behaviour", map[string]any{
		"house": map[string]any{"quiet_after": "30m", "timezone": "Europe/London"},
	})

	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	if err := newTestFetcher(t, mux).ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.House.Timezone != "Europe/London" {
		t.Errorf("House.Timezone = %q, want Europe/London", cfg.House.Timezone)
	}
}
