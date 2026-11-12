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
// The contract these tests pin (see docs/remote-config.md):
//
//   - The four behaviour sections are built from Default() plus the document,
//     and nothing else. What the caller's config held before does not matter,
//     so a key or section the document leaves out is the default, and
//     applying a document is idempotent.
//   - A key that is present replaces the default, including an explicit zero
//     duration. JSON null, for a section or for a key inside one, is the same
//     as leaving it out.
//   - Anything statehouse cannot use exactly as written refuses the whole
//     document, and none of it is applied: an unknown key, a wrong type, an
//     empty or unparseable duration, an out-of-range value, an unresolvable
//     timezone.
//
// Most cases are rows in the two tables below: overlayCases (applied) and
// refusedCases (refused). A new corner case is one row. Each row runs through
// the pure overlayBehaviour against two bases (see bases), and the same rows
// seed FuzzOverlayBehaviour. The fetcher-level tests at the end cover only
// what the fetcher adds: HTTP, /healthz status, logging, and the SIGHUP
// sequence.

func boolPtr(b bool) *bool { return &b }

// bases are the configs every table row is applied to. "default" is what
// startup and SIGHUP pass today. "dirty" has every behaviour section set to
// something non-default (as a running config carried over from an earlier
// document would be) plus a non-behaviour field, to prove that a document's
// result does not depend on what the caller held, and that fields outside the
// behaviour sections survive.
func bases() []struct {
	name string
	cfg  func() Config
} {
	return []struct {
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
			c.Adapters.UPS.BaseTopic = "ups/dirty"
			return c
		}},
	}
}

// wantFrom is the expected result of a successful overlay: base, with the four
// behaviour sections taken from Default() after mutate has applied the keys
// the document sets. Deriving it from Default() rather than hardcoding the
// defaults means a change to a default cannot fail these tests for the wrong
// reason, and comparing whole configs means no field goes unchecked.
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

	// Duplicate keys: the last occurrence wins, at either level, and a null
	// before a real section cannot zero it (the #86 shape the reviewer found
	// with pre-seeded pointers).
	{"duplicate section: null then value", `{"house":null,"house":{"quiet_after":"5m"}}`,
		func(c *Config) { c.House.QuietAfter = 5 * time.Minute }},
	{"duplicate section: value then null", `{"house":{"quiet_after":"5m"},"house":null}`, nil},
	{"duplicate section: last wins whole", `{"house":{"quiet_after":"5m"},"house":{"empty_after":"1h"}}`,
		func(c *Config) { c.House.EmptyAfter = time.Hour }},
	{"duplicate key in section: last wins", `{"house":{"quiet_after":"5m","quiet_after":"6m"}}`,
		func(c *Config) { c.House.QuietAfter = 6 * time.Minute }},

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
	// Unknown keys. Before strict decoding these were silently ignored, and
	// after #86 a typo would quietly mean "default": sleeping_afer "0s" left
	// sleeping mode on.
	{"unknown top-level key", `{"hous":{}}`, []string{`"hous"`}},
	{"unknown house key", `{"house":{"sleeping_afer":"0s"}}`, []string{"house:", `"sleeping_afer"`}},
	{"unknown energy key", `{"energy":{"electricity":{"idle_after":"5m"}}}`, []string{"energy:", `"electricity"`}},
	{"unknown availability key", `{"availability":{"offline_debounc":"30s"}}`, []string{"availability:", `"offline_debounc"`}},
	{"unknown adapter", `{"adapters":{"zigbee":{"enabled":true}}}`, []string{"adapters:", `"zigbee"`}},
	{"unknown adapter key", `{"adapters":{"tanktemp":{"enabled":true,"base_topc":"x"}}}`, []string{"adapters:", `"base_topc"`}},

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
}

// withGoodSections adds valid, non-default energy and adapters sections ahead
// of a refused document's own keys. They prove that a refusal drops the
// sections that did decode as well, not just the bad one. (If the bad case
// itself sets energy or adapters, its occurrence comes last and wins.)
func withGoodSections(doc string) (string, bool) {
	if !strings.HasPrefix(doc, "{") || doc == "{}" || !json.Valid([]byte(doc)) {
		return "", false
	}
	return `{"adapters":{"boiler":{"enabled":true}},"energy":{"max_integration_gap":"45m"},` + doc[1:], true
}

func TestOverlayBehaviour_Applies(t *testing.T) {
	for _, tc := range overlayCases {
		for _, b := range bases() {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				base := b.cfg()
				got, err := overlayBehaviour(base, []byte(tc.doc))
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
		docs := []string{tc.doc}
		if wrapped, ok := withGoodSections(tc.doc); ok {
			docs = append(docs, wrapped)
		}
		for i, doc := range docs {
			for _, b := range bases() {
				name := tc.name + "/" + b.name
				if i == 1 {
					name += "/with good sections"
				}
				t.Run(name, func(t *testing.T) {
					base := b.cfg()
					got, err := overlayBehaviour(base, []byte(doc))
					if err == nil {
						t.Fatalf("overlayBehaviour(%s) = nil error, want it refused", doc)
					}
					for _, want := range tc.wantErr {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error %q does not mention %q", err, want)
						}
					}
					if !reflect.DeepEqual(got, b.cfg()) {
						t.Errorf("a refused document changed the config: energy=%+v availability=%+v house=%+v adapters=%s",
							got.Energy, got.Availability, got.House, adaptersString(got.Adapters))
					}
				})
			}
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

// --- the section decoders and validators on their own ---

// Each decoder overlays the keys present onto its receiver and, on any error,
// leaves the receiver exactly as it was. Exercised directly so a regression
// points at the decoder rather than at the fetcher.
func TestSectionDecoders(t *testing.T) {
	seedHouse := HouseConfig{QuietAfter: time.Minute, EmptyAfter: time.Hour, SleepingAfter: 2 * time.Hour, Timezone: "Local"}
	seedEnergy := EnergyConfig{DivergenceWarningPct: 20, MaxIntegrationGap: 30 * time.Minute}
	seedAvail := AvailabilityConfig{OfflineDebounce: 30 * time.Second}

	type decoder interface{ UnmarshalJSON([]byte) error }
	cases := []struct {
		name    string
		in      string
		seed    func() decoder
		want    any // nil: an error is expected and the receiver must equal the seed
		section func(decoder) any
	}{
		{"house: one key", `{"quiet_after":"5m"}`, func() decoder { h := seedHouse; return &h },
			HouseConfig{QuietAfter: 5 * time.Minute, EmptyAfter: time.Hour, SleepingAfter: 2 * time.Hour, Timezone: "Local"},
			func(d decoder) any { return *d.(*HouseConfig) }},
		{"house: timezone", `{"timezone":"UTC"}`, func() decoder { h := seedHouse; return &h },
			HouseConfig{QuietAfter: time.Minute, EmptyAfter: time.Hour, SleepingAfter: 2 * time.Hour, Timezone: "UTC"},
			func(d decoder) any { return *d.(*HouseConfig) }},
		{"house: null", `null`, func() decoder { h := seedHouse; return &h }, seedHouse,
			func(d decoder) any { return *d.(*HouseConfig) }},
		{"house: unknown key", `{"sleeping_afer":"0s"}`, func() decoder { h := seedHouse; return &h }, nil,
			func(d decoder) any { return *d.(*HouseConfig) }},
		{"house: bad type", `{"timezone":1}`, func() decoder { h := seedHouse; return &h }, nil,
			func(d decoder) any { return *d.(*HouseConfig) }},
		{"house: second duration bad", `{"quiet_after":"5m","sleeping_after":"soon"}`, func() decoder { h := seedHouse; return &h }, nil,
			func(d decoder) any { return *d.(*HouseConfig) }},
		{"house: bad duration after timezone", `{"timezone":"UTC","quiet_after":""}`, func() decoder { h := seedHouse; return &h }, nil,
			func(d decoder) any { return *d.(*HouseConfig) }},

		{"energy: one key", `{"max_integration_gap":"1h"}`, func() decoder { e := seedEnergy; return &e },
			EnergyConfig{DivergenceWarningPct: 20, MaxIntegrationGap: time.Hour},
			func(d decoder) any { return *d.(*EnergyConfig) }},
		{"energy: pct and bad gap", `{"divergence_warning_pct":5,"max_integration_gap":"soon"}`, func() decoder { e := seedEnergy; return &e }, nil,
			func(d decoder) any { return *d.(*EnergyConfig) }},
		{"energy: bad type", `{"divergence_warning_pct":"20"}`, func() decoder { e := seedEnergy; return &e }, nil,
			func(d decoder) any { return *d.(*EnergyConfig) }},
		{"energy: unknown key", `{"electricity":{}}`, func() decoder { e := seedEnergy; return &e }, nil,
			func(d decoder) any { return *d.(*EnergyConfig) }},

		{"availability: empty", `{}`, func() decoder { a := seedAvail; return &a }, seedAvail,
			func(d decoder) any { return *d.(*AvailabilityConfig) }},
		{"availability: empty string", `{"offline_debounce":""}`, func() decoder { a := seedAvail; return &a }, nil,
			func(d decoder) any { return *d.(*AvailabilityConfig) }},
		{"availability: bad type", `{"offline_debounce":30}`, func() decoder { a := seedAvail; return &a }, nil,
			func(d decoder) any { return *d.(*AvailabilityConfig) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.seed()
			err := d.UnmarshalJSON([]byte(tc.in))
			got := tc.section(d)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s) = nil, want an error", tc.in)
				}
				if seed := tc.section(tc.seed()); !reflect.DeepEqual(got, seed) {
					t.Errorf("a failed decode changed the receiver: got %+v, want %+v", got, seed)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("UnmarshalJSON(%s): got %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
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

func TestSectionValidate(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantErr string // "" = valid
	}{
		{"house zero durations", HouseConfig{}.validate(), ""},
		{"house Local", HouseConfig{Timezone: "Local"}.validate(), ""},
		{"house negative quiet", HouseConfig{QuietAfter: -1}.validate(), "quiet_after"},
		{"house negative empty", HouseConfig{EmptyAfter: -1}.validate(), "empty_after"},
		{"house negative sleeping", HouseConfig{SleepingAfter: -1}.validate(), "sleeping_after"},
		{"house bad zone", HouseConfig{Timezone: "Mars/Olympus"}.validate(), "Mars/Olympus"},
		{"energy smallest pct", EnergyConfig{DivergenceWarningPct: 0.01}.validate(), ""},
		{"energy zero pct", EnergyConfig{}.validate(), "divergence_warning_pct"},
		{"energy negative pct", EnergyConfig{DivergenceWarningPct: -5}.validate(), "divergence_warning_pct"},
		{"energy negative gap", EnergyConfig{DivergenceWarningPct: 20, MaxIntegrationGap: -1}.validate(), "max_integration_gap"},
		{"energy zero gap (no clamp)", EnergyConfig{DivergenceWarningPct: 20}.validate(), ""},
		{"availability zero", AvailabilityConfig{}.validate(), ""},
		{"availability negative", AvailabilityConfig{OfflineDebounce: -1}.validate(), "offline_debounce"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch {
			case tc.wantErr == "" && tc.err != nil:
				t.Errorf("validate() = %v, want nil", tc.err)
			case tc.wantErr != "" && (tc.err == nil || !strings.Contains(tc.err.Error(), tc.wantErr)):
				t.Errorf("validate() = %v, want an error mentioning %q", tc.err, tc.wantErr)
			}
		})
	}
}

// --- fuzz ---

// FuzzOverlayBehaviour checks the contract on arbitrary documents, seeded
// with every table row and the example document:
//
//   - refused  ⇒ the returned config is the base, unchanged;
//   - applied  ⇒ fields outside the behaviour sections are the base's;
//   - applied  ⇒ applying the same document again changes nothing
//     (idempotent, whatever the base held);
//   - applied  ⇒ a section, or a key inside one, that the document leaves
//     out (or sets to null) is the default.
func FuzzOverlayBehaviour(f *testing.F) {
	for _, tc := range overlayCases {
		f.Add(tc.doc)
	}
	for _, tc := range refusedCases {
		f.Add(tc.doc)
	}
	if raw, err := os.ReadFile(filepath.Join("..", "..", "config", "remote", "statehouse_behaviour.json")); err == nil {
		f.Add(string(raw))
	}
	def := Default()
	f.Fuzz(func(t *testing.T, doc string) {
		for _, b := range bases() {
			base := b.cfg()
			got, err := overlayBehaviour(base, []byte(doc))
			if err != nil {
				if !reflect.DeepEqual(got, b.cfg()) {
					t.Fatalf("%s: refused %q but changed the config", b.name, doc)
				}
				continue
			}
			rest := got
			rest.Energy, rest.Availability, rest.House, rest.Adapters = base.Energy, base.Availability, base.House, base.Adapters
			if !reflect.DeepEqual(rest, base) {
				t.Fatalf("%s: %q changed a field outside the behaviour sections", b.name, doc)
			}
			again, err := overlayBehaviour(got, []byte(doc))
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatalf("%s: %q is not idempotent: second apply err=%v", b.name, doc, err)
			}
			checkAbsentIsDefault(t, doc, got, def)
		}
	})
}

// checkAbsentIsDefault asserts that every section and key doc leaves out, or
// sets to null, has its default value in got. It reads doc with the same
// last-occurrence-wins rule encoding/json uses, so duplicate keys agree.
func checkAbsentIsDefault(t *testing.T, doc string, got, def Config) {
	t.Helper()
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(doc), &top) != nil {
		return // "null" applies as all-defaults; anything else that applies is an object
	}
	keys := func(section string) (map[string]json.RawMessage, bool) {
		raw, ok := top[section]
		if !ok || string(raw) == "null" {
			return nil, false
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		return m, true
	}
	absent := func(m map[string]json.RawMessage, key string) bool {
		raw, ok := m[key]
		return !ok || string(raw) == "null"
	}
	fields := []struct {
		section, key string
		got, def     any
	}{
		{"energy", "divergence_warning_pct", got.Energy.DivergenceWarningPct, def.Energy.DivergenceWarningPct},
		{"energy", "max_integration_gap", got.Energy.MaxIntegrationGap, def.Energy.MaxIntegrationGap},
		{"availability", "offline_debounce", got.Availability.OfflineDebounce, def.Availability.OfflineDebounce},
		{"house", "quiet_after", got.House.QuietAfter, def.House.QuietAfter},
		{"house", "empty_after", got.House.EmptyAfter, def.House.EmptyAfter},
		{"house", "sleeping_after", got.House.SleepingAfter, def.House.SleepingAfter},
		{"house", "timezone", got.House.Timezone, def.House.Timezone},
	}
	for _, f := range fields {
		m, present := keys(f.section)
		if (!present || absent(m, f.key)) && f.got != f.def {
			t.Fatalf("%q leaves %s.%s out but it is %v, not the default %v", doc, f.section, f.key, f.got, f.def)
		}
	}
	if _, present := keys("adapters"); !present && !reflect.DeepEqual(got.Adapters, def.Adapters) {
		t.Fatalf("%q leaves adapters out but they are %s, not the defaults", doc, adaptersString(got.Adapters))
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
