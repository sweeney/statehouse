package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Issue #86: a statehouse_behaviour section that named only some of its keys
// was decoded into a zero struct and swapped in whole, so every key it left
// out became zero rather than its default. Production's house block names
// only quiet_after and empty_after, which made sleeping_after 0s: the house
// could never enter sleeping mode.
//
// overlayCases and refusedCases run through the pure overlayBehaviour; the
// fetcher tests at the end cover HTTP, /healthz, logging and the SIGHUP
// sequence. The rules are in docs/remote-config.md.

func boolPtr(b bool) *bool { return &b }

// bases are the configs every table row is applied to: Default(), and a
// "dirty" config whose behaviour sections all differ from the defaults (as a
// running config might) plus a field outside them. The dirty base proves the
// result is built from Default(), not from what the caller held (#86), that a
// refusal returns the base unchanged, and that other fields survive.
var bases = []struct {
	name string
	cfg  func() Config
}{
	{"default", Default},
	{"dirty", func() Config {
		c := Default()
		c.Site.DevicesNamespace = "devices_dirty"
		c.Energy = EnergyConfig{DivergenceWarningPct: 99, MaxIntegrationGap: time.Minute}
		c.Availability = AvailabilityConfig{OfflineDebounce: time.Minute}
		c.House = HouseConfig{QuietAfter: time.Minute, EmptyAfter: time.Minute, SleepingAfter: time.Minute, Timezone: "Europe/Paris"}
		c.Adapters.UPS.Enabled = boolPtr(true)
		return c
	}},
}

// wantFrom is the expected result of a successful overlay: base, with its
// behaviour sections taken from Default() edited by mutate.
func wantFrom(base Config, mutate func(*Config)) Config {
	d := Default()
	if mutate != nil {
		mutate(&d)
	}
	base.Energy, base.Availability, base.House, base.Adapters = d.Energy, d.Availability, d.House, d.Adapters
	return base
}

// overlayCases are documents that apply. mutate edits Default() into the
// expected behaviour sections; nil means "all defaults".
var overlayCases = []struct {
	name   string
	doc    string
	mutate func(*Config)
}{
	// #86 itself.
	{"production house block keeps sleeping_after", `{"house":{"empty_after":"2h","quiet_after":"30m"}}`,
		func(c *Config) { c.House.EmptyAfter = 2 * time.Hour; c.House.QuietAfter = 30 * time.Minute }},

	// Absent, empty and null all mean "default".
	{"empty document", `{}`, nil},
	{"null document", `null`, nil},
	{"empty sections", `{"energy":{},"availability":{},"house":{},"adapters":{}}`, nil},
	{"null sections", `{"energy":null,"availability":null,"house":null,"adapters":null}`, nil},
	{"null keys", `{"energy":{"divergence_warning_pct":null,"max_integration_gap":null},
		"availability":{"offline_debounce":null},
		"house":{"quiet_after":null,"empty_after":null,"sleeping_after":null,"timezone":null}}`, nil},

	// One key at a time: the others keep their defaults.
	{"energy: only max_integration_gap", `{"energy":{"max_integration_gap":"45m"}}`,
		func(c *Config) { c.Energy.MaxIntegrationGap = 45 * time.Minute }},
	{"energy: only divergence_warning_pct", `{"energy":{"divergence_warning_pct":15}}`,
		func(c *Config) { c.Energy.DivergenceWarningPct = 15 }},
	{"energy: fractional pct", `{"energy":{"divergence_warning_pct":0.5}}`,
		func(c *Config) { c.Energy.DivergenceWarningPct = 0.5 }},
	{"availability: offline_debounce", `{"availability":{"offline_debounce":"1m"}}`,
		func(c *Config) { c.Availability.OfflineDebounce = time.Minute }},
	{"house: only sleeping_after", `{"house":{"sleeping_after":"90m"}}`,
		func(c *Config) { c.House.SleepingAfter = 90 * time.Minute }},

	// An explicit zero duration is a value, not "unset": sleeping_after "0s"
	// is documented as "never enter sleeping".
	{"explicit zero durations", `{"house":{"quiet_after":"0s","empty_after":"0","sleeping_after":"0s"},
		"availability":{"offline_debounce":"0s"},"energy":{"max_integration_gap":"0s"}}`,
		func(c *Config) {
			c.House.QuietAfter, c.House.EmptyAfter, c.House.SleepingAfter = 0, 0, 0
			c.Availability.OfflineDebounce = 0
			c.Energy.MaxIntegrationGap = 0
		}},

	// Timezone: "" is the documented spelling of UTC; "Local" is the host's
	// zone and is documented, and used by config/remote/statehouse_behaviour.json.
	{"timezone: Europe/London", `{"house":{"timezone":"Europe/London"}}`,
		func(c *Config) { c.House.Timezone = "Europe/London" }},
	{"timezone: explicit empty is UTC", `{"house":{"timezone":""}}`, nil},
	{"timezone: UTC", `{"house":{"timezone":"UTC"}}`, func(c *Config) { c.House.Timezone = "UTC" }},
	{"timezone: Local", `{"house":{"timezone":"Local"}}`, func(c *Config) { c.House.Timezone = "Local" }},

	// Adapters: an adapter named with only enabled gets its default
	// base_topic, and one left out is off (zigbee2mqtt on).
	{"adapters: partial block", `{"adapters":{"boiler":{"enabled":true}}}`,
		func(c *Config) { c.Adapters.Boiler.Enabled = boolPtr(true) }},
	{"adapters: base_topic only", `{"adapters":{"ups":{"base_topic":"nut"}}}`,
		func(c *Config) { c.Adapters.UPS.BaseTopic = "nut" }},
	{"adapters: zigbee2mqtt off", `{"adapters":{"zigbee2mqtt":{"enabled":false}}}`,
		func(c *Config) { c.Adapters.Zigbee2MQTT.Enabled = boolPtr(false) }},
}

// refusedCases are documents that must be refused whole. wantErr lists
// substrings the error must contain, so the log line and /healthz name both
// the section and the key at fault.
var refusedCases = []struct {
	name    string
	doc     string
	wantErr []string
}{
	// Unknown keys. A typo would otherwise quietly mean "default":
	// sleeping_afer "0s" would leave sleeping mode on.
	{"unknown top-level key", `{"hous":{}}`, []string{`"hous"`}},
	{"unknown house key", `{"house":{"sleeping_afer":"0s"}}`, []string{"house:", `"sleeping_afer"`}},
	{"unknown energy key", `{"energy":{"electricity":{"idle_after":"5m"}}}`, []string{"energy:", `"electricity"`}},
	{"unknown availability key", `{"availability":{"offline_debounc":"30s"}}`, []string{"availability:", `"offline_debounc"`}},
	{"unknown adapter", `{"adapters":{"zigbee":{"enabled":true}}}`, []string{"adapters:", `"zigbee"`}},
	{"unknown adapter key", `{"adapters":{"tanktemp":{"enabled":true,"base_topc":"x"}}}`, []string{"adapters:", `"base_topc"`}},
	// Duplicate keys, compared case-insensitively as encoding/json matches
	// them. A later copy would otherwise win silently (or, for an adapter,
	// merge with the earlier one).
	{"duplicate section, other case", `{"house":{"sleeping_after":"0s"},"House":null}`, []string{`duplicate key "House"`}},
	{"duplicate key in section", `{"house":{"sleeping_after":"0s","Sleeping_After":null}}`, []string{`duplicate key "Sleeping_After"`}},
	{"duplicate adapter would merge", `{"adapters":{"boiler":{"base_topic":"x"},"boiler":{"enabled":true}}}`, []string{`duplicate key "boiler"`}},
	{"duplicate inside an array", `{"house":[{"a":1,"a":2}]}`, []string{`duplicate key "a"`}},

	// Durations that do not parse. "" is refused, not read as "unset": it
	// used to give 0, so reading it as the default would silently flip
	// sleeping mode on.
	{"empty duration: sleeping_after", `{"house":{"sleeping_after":""}}`, []string{"house:", "sleeping_after", "empty"}},
	{"empty duration: quiet_after", `{"house":{"quiet_after":""}}`, []string{"house:", "quiet_after", "empty"}},
	{"empty duration: empty_after", `{"house":{"empty_after":""}}`, []string{"house:", "empty_after", "empty"}},
	{"empty duration: offline_debounce", `{"availability":{"offline_debounce":""}}`, []string{"availability:", "offline_debounce", "empty"}},
	{"empty duration: max_integration_gap", `{"energy":{"max_integration_gap":""}}`, []string{"energy:", "max_integration_gap", "empty"}},
	{"bad duration: house", `{"house":{"sleeping_after":"soon"}}`, []string{"house:", "sleeping_after", "invalid duration"}},
	{"bad duration: energy", `{"energy":{"max_integration_gap":"soon"}}`, []string{"energy:", "max_integration_gap", "invalid duration"}},
	{"bad duration: availability", `{"availability":{"offline_debounce":"soon"}}`, []string{"availability:", "offline_debounce"}},
	{"overflowing duration", `{"house":{"sleeping_after":"9999999999h"}}`, []string{"house:", "sleeping_after"}},

	// Out of range. A negative duration is neither "off" nor "on" in
	// house.go (it gates on > 0 in one place and == 0 in another), and a
	// divergence threshold <= 0 makes every counter-strategy cycle warn.
	{"negative quiet_after", `{"house":{"quiet_after":"-1h"}}`, []string{"house:", "quiet_after", "negative"}},
	{"negative empty_after", `{"house":{"empty_after":"-1h"}}`, []string{"house:", "empty_after", "negative"}},
	{"negative sleeping_after", `{"house":{"sleeping_after":"-5m"}}`, []string{"house:", "sleeping_after", "negative"}},
	{"negative offline_debounce", `{"availability":{"offline_debounce":"-1s"}}`, []string{"availability:", "offline_debounce", "negative"}},
	{"negative max_integration_gap", `{"energy":{"max_integration_gap":"-1m"}}`, []string{"energy:", "max_integration_gap", "negative"}},
	{"zero divergence_warning_pct", `{"energy":{"divergence_warning_pct":0}}`, []string{"energy:", "divergence_warning_pct"}},
	{"negative divergence_warning_pct", `{"energy":{"divergence_warning_pct":-1}}`, []string{"energy:", "divergence_warning_pct"}},

	// Timezones that do not resolve. Location() would fall back to UTC.
	{"misspelt timezone", `{"house":{"timezone":"Europe/Lundun"}}`, []string{"house:", "timezone", "Europe/Lundun"}},
	{"timezone with whitespace", `{"house":{"timezone":" Europe/London"}}`, []string{"house:", "timezone"}},

	// Wrong types, at every level.
	{"pct as a string", `{"energy":{"divergence_warning_pct":"20"}}`, []string{"energy:", "divergence_warning_pct"}},
	{"duration as a number", `{"availability":{"offline_debounce":30}}`, []string{"availability:", "offline_debounce"}},
	{"timezone as a number", `{"house":{"timezone":1}}`, []string{"house:", "timezone"}},
	{"house as an array", `{"house":[]}`, []string{"house:"}},
	{"energy as a string", `{"energy":"20"}`, []string{"energy:"}},
	{"availability as a number", `{"availability":30}`, []string{"availability:"}},
	{"adapters as a string", `{"adapters":"all"}`, []string{"adapters:"}},
	{"enabled as a string", `{"adapters":{"boiler":{"enabled":"yes"}}}`, []string{"adapters:", "enabled"}},
	{"document as an array", `[]`, []string{"array"}},
	{"document not JSON", `{"house":`, nil},
	{"document with a non-string key", `{1:2}`, nil},
	{"trailing data", `{} {}`, nil},
	{"unterminated after a control byte", "[0\x0f", nil},
}

func TestOverlayBehaviour_Applies(t *testing.T) {
	for _, tc := range overlayCases {
		for _, b := range bases {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				got, err := overlayBehaviour(b.cfg(), []byte(tc.doc))
				if err != nil {
					t.Fatalf("overlayBehaviour(%s) = %v, want it applied", tc.doc, err)
				}
				if want := wantFrom(b.cfg(), tc.mutate); !reflect.DeepEqual(got, want) {
					t.Errorf("overlayBehaviour(%s):\n got  energy=%+v availability=%+v house=%+v adapters=%s\n want energy=%+v availability=%+v house=%+v adapters=%s",
						tc.doc, got.Energy, got.Availability, got.House, adaptersString(got.Adapters),
						want.Energy, want.Availability, want.House, adaptersString(want.Adapters))
				}
			})
		}
	}
}

func TestOverlayBehaviour_Refuses(t *testing.T) {
	for _, tc := range refusedCases {
		for _, b := range bases {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				got, err := overlayBehaviour(b.cfg(), []byte(tc.doc))
				if err == nil {
					t.Fatalf("overlayBehaviour(%s) = nil error, want it refused", tc.doc)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				if !reflect.DeepEqual(got, b.cfg()) {
					t.Errorf("a refused document changed the config: house=%+v adapters=%s", got.House, adaptersString(got.Adapters))
				}
			})
		}
	}
}

// The example document people copy into the config service applies, and
// sets every house key (it is the full set to copy from, even though a key
// left out would now keep its default).
func TestOverlayBehaviour_ExampleDocument(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "remote", "statehouse_behaviour.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		House map[string]json.RawMessage `json:"house"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"quiet_after", "empty_after", "sleeping_after", "timezone"} {
		if _, ok := doc.House[key]; !ok {
			t.Errorf("example house block does not show %s", key)
		}
	}
	if _, err := overlayBehaviour(Default(), raw); err != nil {
		t.Errorf("example statehouse_behaviour.json is refused: %v", err)
	}
}

// adaptersString renders adapters with the Enabled pointers dereferenced, so a
// failure message shows values rather than addresses.
func adaptersString(a AdaptersConfig) string {
	b, _ := json.Marshal(a)
	return string(b)
}

// The defaults themselves must pass validation, or Default() would describe
// a config the fetcher refuses.
func TestDefaultBehaviourValidates(t *testing.T) {
	d := Default()
	for name, err := range map[string]error{
		"energy":       d.Energy.validate(),
		"availability": d.Availability.validate(),
		"house":        d.House.validate(),
	} {
		if err != nil {
			t.Errorf("Default().%s.validate() = %v", name, err)
		}
	}
}

// --- through the Fetcher ---

// behaviourFetcher serves statehouse_behaviour from *doc (read on every
// request, so a test can change it between applies) with empty classes and
// devices namespaces.
func behaviourFetcher(t *testing.T, doc *string, logger *slog.Logger) *Fetcher {
	t.Helper()
	var mu sync.Mutex
	mux := http.NewServeMux()
	serveNamespace(mux, "statehouse_classes", map[string]any{})
	serveNamespace(mux, "devices_test", map[string]any{})
	mux.HandleFunc("/api/v1/config/statehouse_behaviour", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(*doc)) //nolint:errcheck
	})
	f := newTestFetcher(t, mux)
	f.Logger = logger
	return f
}

func testBase() Config {
	c := Default()
	c.Site.DevicesNamespace = "devices_test"
	return c
}

// End to end, production's block reaches cfg with sleeping_after intact.
func TestFetcher_ProductionHouseBlockKeepsSleepingAfter(t *testing.T) {
	doc := `{"house":{"empty_after":"2h","quiet_after":"30m"}}`
	cfg := testBase()
	if err := behaviourFetcher(t, &doc, nil).ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.House.SleepingAfter != Default().House.SleepingAfter {
		t.Errorf("sleeping_after = %v, want the default %v (#86)", cfg.House.SleepingAfter, Default().House.SleepingAfter)
	}
}

// A refused document is returned as an error naming the namespace, leaves
// cfg untouched (adapters included), is recorded on /healthz and logged with
// the key at fault; a good document afterwards clears the /healthz error.
func TestFetcher_RefusedBehaviourIsReportedAndRecovers(t *testing.T) {
	var logs bytes.Buffer
	doc := `{"adapters":{"boiler":{"enabled":true}},"house":{"sleeping_afer":"0s"}}`
	f := behaviourFetcher(t, &doc, slog.New(slog.NewTextHandler(&logs, nil)))

	cfg := testBase()
	before := testBase()
	err := f.ApplyRemote(context.Background(), &cfg)
	if err == nil || !strings.Contains(err.Error(), "statehouse_behaviour") || !strings.Contains(err.Error(), "sleeping_afer") {
		t.Fatalf("ApplyRemote() = %v, want an error naming the namespace and the key", err)
	}
	if !reflect.DeepEqual(cfg.Energy, before.Energy) || !reflect.DeepEqual(cfg.Availability, before.Availability) ||
		!reflect.DeepEqual(cfg.House, before.House) || !reflect.DeepEqual(cfg.Adapters, before.Adapters) {
		t.Errorf("a refused document changed cfg: adapters=%s house=%+v", adaptersString(cfg.Adapters), cfg.House)
	}
	st := f.Statuses()["statehouse_behaviour"]
	if st.OK || !strings.Contains(st.Error, "sleeping_afer") {
		t.Errorf("/healthz status = %+v, want not ok with the key named", st)
	}
	if !strings.Contains(logs.String(), "statehouse_behaviour rejected") || !strings.Contains(logs.String(), "sleeping_afer") {
		t.Errorf("log = %q, want a rejection naming the key", logs.String())
	}

	doc = `{"house":{"sleeping_after":"0s"}}`
	cfg = testBase()
	if err := f.ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote(good doc) = %v", err)
	}
	if st := f.Statuses()["statehouse_behaviour"]; !st.OK || st.Error != "" {
		t.Errorf("/healthz status after a good document = %+v, want ok", st)
	}
	if cfg.House.SleepingAfter != 0 {
		t.Errorf("sleeping_after = %v, want 0 as written", cfg.House.SleepingAfter)
	}
}

// The SIGHUP sequence: document A sets sleeping_after, document B drops it.
// B must give the default, whether it is applied onto a fresh base (what
// watchSIGHUP does today) or onto the config A produced (what a later
// "optimisation" of SIGHUP might do). A's value must not stick either way.
func TestFetcher_KeyDroppedOnReloadReturnsToDefault(t *testing.T) {
	doc := `{"house":{"sleeping_after":"1h"}}`
	f := behaviourFetcher(t, &doc, nil)
	running := testBase()
	if err := f.ApplyRemote(context.Background(), &running); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if running.House.SleepingAfter != time.Hour {
		t.Fatalf("apply A: sleeping_after = %v, want 1h", running.House.SleepingAfter)
	}

	doc = `{"house":{"quiet_after":"20m"}}`
	for name, cfg := range map[string]Config{"fresh base": testBase(), "running config": running} {
		if err := f.ApplyRemote(context.Background(), &cfg); err != nil {
			t.Fatalf("%s: apply B: %v", name, err)
		}
		if cfg.House.SleepingAfter != Default().House.SleepingAfter {
			t.Errorf("%s: sleeping_after = %v after B dropped it, want the default %v", name, cfg.House.SleepingAfter, Default().House.SleepingAfter)
		}
		if cfg.House.QuietAfter != 20*time.Minute {
			t.Errorf("%s: quiet_after = %v, want 20m from B", name, cfg.House.QuietAfter)
		}
	}
}

// Decoding adapters must never write through Enabled pointers the caller's
// config holds: on SIGHUP that is the running engine's base config.
func TestFetcher_AdaptersDecodeDoesNotWriteThroughCfgPointers(t *testing.T) {
	doc := `{"adapters":{"boiler":{"enabled":false}}}`
	cfg := testBase()
	cfg.Adapters.Boiler.Enabled = boolPtr(true)
	held := cfg.Adapters.Boiler.Enabled
	if err := behaviourFetcher(t, &doc, nil).ApplyRemote(context.Background(), &cfg); err != nil {
		t.Fatalf("ApplyRemote() = %v", err)
	}
	if cfg.Adapters.Boiler.IsEnabled() {
		t.Error("boiler: want off as written")
	}
	if !*held {
		t.Error("decoding wrote through a pointer shared with the previous config")
	}
}
