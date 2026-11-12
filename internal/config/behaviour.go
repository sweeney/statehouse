package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// This file holds the rules for applying a statehouse_behaviour document
// (see docs/remote-config.md, "How each namespace is applied"):
//
//   - The four sections (energy, availability, house, adapters) are built from
//     Default() plus the document, and nothing else. What the caller's config
//     held does not matter, so a key or section the document leaves out is the
//     default, and applying the same document twice gives the same result.
//     Before #86 each section was decoded into a zero struct instead, so a key
//     left out became zero: production lost sleeping_after that way.
//   - A key that is present replaces the default, including an explicit zero
//     duration. JSON null, for a section or a key, is the same as leaving it
//     out.
//   - Anything statehouse cannot use exactly as written refuses the whole
//     document, and none of it is applied: an unknown or misspelt key, a key
//     given twice, a wrong type, an empty or unparseable duration, an
//     out-of-range value, or a timezone that does not resolve.
//
// Keeping this pure (no I/O, logging or status recording) lets the rules be
// unit- and fuzz-tested without a config service; Fetcher.applyBehaviour only
// fetches, records the outcome and commits.

// behaviourWire is the statehouse_behaviour document with each section kept
// raw. Each present section is then decoded explicitly onto its default (see
// overlayBehaviour), rather than by pre-seeding pointers for encoding/json to
// decode into: with pointers, {"house":null,"house":{...}} made the first null
// clear the seed and the second allocate a zero struct, which was #86 again.
// Duplicate keys are refused before this is decoded (see checkDuplicateKeys),
// so each section is here at most once.
type behaviourWire struct {
	Energy       json.RawMessage `json:"energy"`
	Availability json.RawMessage `json:"availability"`
	House        json.RawMessage `json:"house"`
	Adapters     json.RawMessage `json:"adapters"`
}

// overlayBehaviour returns base with its four behaviour sections replaced by
// Default()'s, with doc's keys applied over them. Every other field of base is
// returned as it was. On any error it returns base unchanged and an error
// naming the section and key at fault.
func overlayBehaviour(base Config, doc []byte) (Config, error) {
	var w behaviourWire
	if err := strictUnmarshal(doc, &w); err != nil {
		return base, err
	}

	// Seeded from Default(), not from base: "absent means default" then holds
	// by construction, whatever the caller passes. Default()'s adapters have
	// no Enabled pointers, so there is nothing for decoding to write through.
	def := Default()
	energy, availability, house, adapters := def.Energy, def.Availability, def.House, def.Adapters

	// Closures rather than method values: a method value on a value receiver
	// (energy.validate) would bind a copy of energy taken here, before decode
	// has run, and validate the default instead of the result.
	sections := []struct {
		name     string
		raw      json.RawMessage
		decode   func([]byte) error
		validate func() error
	}{
		{"energy", w.Energy, energy.UnmarshalJSON, func() error { return energy.validate() }},
		{"availability", w.Availability, availability.UnmarshalJSON, func() error { return availability.validate() }},
		{"house", w.House, house.UnmarshalJSON, func() error { return house.validate() }},
		{"adapters", w.Adapters, func(b []byte) error {
			// Adapters replace the section whole: decode onto a zero struct,
			// which is the default (zigbee2mqtt on, the rest off) apart from
			// base_topic, which fillDefaults supplies.
			var a AdaptersConfig
			if err := strictUnmarshal(b, &a); err != nil {
				return err
			}
			a.fillDefaults()
			adapters = a
			return nil
		}, nil},
	}
	for _, s := range sections {
		if isPresent(s.raw) {
			if err := s.decode(s.raw); err != nil {
				return base, fmt.Errorf("%s: %w", s.name, err)
			}
		}
		// Validate present and absent sections alike: an absent one is the
		// default, which TestDefaultBehaviourValidates keeps valid, so this
		// costs nothing and needs no special case.
		if s.validate != nil {
			if err := s.validate(); err != nil {
				return base, fmt.Errorf("%s: %w", s.name, err)
			}
		}
	}

	out := base
	out.Energy, out.Availability, out.House, out.Adapters = energy, availability, house, adapters
	return out, nil
}

// isPresent reports whether a raw section was given a value: a section that
// was left out, or given as null, is applied as its default. encoding/json
// hands a json.RawMessage field the exact bytes of the value, so "null" needs
// no trimming.
func isPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// strictUnmarshal decodes b into into, refusing an object that names a key
// twice (see checkDuplicateKeys) and any object key that is not one of into's
// json keys spelled exactly (see checkKeys).
func strictUnmarshal(b []byte, into any) error {
	if err := checkDuplicateKeys(b); err != nil {
		return err
	}
	if err := checkKeys(b, reflect.TypeOf(into), ""); err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// checkDuplicateKeys refuses a JSON object, at any depth, that names the same
// key twice. encoding/json accepts duplicates, but not consistently: a
// json.RawMessage or scalar field keeps the last occurrence, while a struct
// field merges every occurrence (in adapters, {"boiler":{"base_topic":"x"},
// "boiler":{"enabled":true}} gave base_topic "x" and enabled true), and a
// misspelt key in an earlier occurrence passed checkKeys, which only sees the
// last. A document that says two things about one key is ambiguous, so it is
// refused like any other value statehouse cannot use exactly as written.
//
// It returns nil for input that is not valid JSON, leaving json.Unmarshal to
// report the syntax error.
func checkDuplicateKeys(b []byte) error {
	err := walkDuplicateKeys(json.NewDecoder(bytes.NewReader(b)), "")
	if errors.Is(err, errStopWalk) {
		return nil // a syntax error: json.Unmarshal reports it
	}
	return err
}

// errStopWalk ends walkDuplicateKeys on input that is not valid JSON. It must
// end the whole walk, not just the current value: after a failed Token,
// dec.More can keep reporting true (for `[0\x0f` it does, forever), so an
// enclosing loop that carried on would never finish.
var errStopWalk = errors.New("not valid JSON")

// walkDuplicateKeys reads one JSON value from dec, checking every object in
// it. path prefixes the key named in the error, as in checkKeys.
func walkDuplicateKeys(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return errStopWalk
	}
	switch tok {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return errStopWalk
			}
			key, _ := tok.(string) // Token only returns a string in key position
			if seen[key] {
				return fmt.Errorf("duplicate key %q", path+key)
			}
			seen[key] = true
			if err := walkDuplicateKeys(dec, path+key+"."); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for dec.More() {
			if err := walkDuplicateKeys(dec, path); err != nil {
				return err
			}
		}
	default:
		return nil // a scalar
	}
	if _, err := dec.Token(); err != nil { // the closing '}' or ']'
		return errStopWalk
	}
	return nil
}

// checkKeys refuses any object key in raw that is not exactly the json name of
// a field of t, recursing into fields that are themselves structs (so a
// misspelt key inside an adapter is caught too). It returns nil for anything
// that is not a JSON object, leaving json.Unmarshal to report the type error.
//
// json.Decoder.DisallowUnknownFields is not enough on its own: encoding/json
// matches keys case-insensitively, so "Sleeping_After" would be accepted, and
// the docs and the fuzz test both rely on keys being exactly as documented.
// Keys are checked in sorted order so the error is the same on every run.
func checkKeys(raw []byte, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	fields := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			fields[name] = t.Field(i).Type
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ft, ok := fields[k]
		if !ok {
			return fmt.Errorf("unknown key %q", path+k)
		}
		if err := checkKeys(obj[k], ft, path+k+"."); err != nil {
			return err
		}
	}
	return nil
}

// validate reports the first energy value statehouse cannot use. A divergence
// threshold of 0 or less would make every counter-strategy cycle warn
// (engine.go compares pct >= threshold). A max_integration_gap of 0 is valid:
// the integrator then never clamps.
func (e EnergyConfig) validate() error {
	if !(e.DivergenceWarningPct > 0) {
		return fmt.Errorf("divergence_warning_pct must be greater than 0, got %v", e.DivergenceWarningPct)
	}
	return nonNegative("max_integration_gap", e.MaxIntegrationGap)
}

// validate reports a negative offline_debounce. 0 is valid: no debounce.
func (a AvailabilityConfig) validate() error {
	return nonNegative("offline_debounce", a.OfflineDebounce)
}

// validate reports the first house value statehouse cannot use. A negative
// duration is refused because house.go reads them inconsistently (it gates on
// > 0 in some places and == 0 in others), so it would be neither "off" nor
// "on". 0 is valid and turns that rule off. The timezone must resolve:
// Location() would otherwise fall back to UTC silently and put the night/day
// window an hour out for half the year. "" (UTC) and "Local" (the host's
// zone) are both documented and resolve.
func (h HouseConfig) validate() error {
	for _, d := range []struct {
		name string
		v    time.Duration
	}{
		{"quiet_after", h.QuietAfter},
		{"empty_after", h.EmptyAfter},
		{"sleeping_after", h.SleepingAfter},
	} {
		if err := nonNegative(d.name, d.v); err != nil {
			return err
		}
	}
	if h.Timezone != "" {
		if _, err := time.LoadLocation(h.Timezone); err != nil {
			return fmt.Errorf("timezone %q: %w", h.Timezone, err)
		}
	}
	return nil
}

func nonNegative(name string, d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("%s must not be negative, got %v", name, d)
	}
	return nil
}
