package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sweeney/common/devices"
	"github.com/sweeney/common/remoteconfig"
)

// TokenSource is satisfied by identity.TokenSource. remoteconfig uses it to
// obtain a Bearer token for the config service, and invalidates that token on a
// 401 before retrying once with a fresh credential.
type TokenSource = remoteconfig.TokenSource

// NamespaceStatus records the outcome of the most recent fetch attempt for
// a single config namespace.
type NamespaceStatus = remoteconfig.Status

// Fetcher fetches the three statehouse config namespaces from the config
// service and applies them to a Config. They are the only source of the
// remote fields (see Config); the local YAML cannot set them.
//
// ApplyRemote reports any namespace it could not apply. The caller decides
// what that means: at startup the service refuses to run, and on a SIGHUP
// reload it keeps the config it is already running. Neither carries on with
// a section half-filled from defaults.
type Fetcher struct {
	BaseURL    string
	Tokens     TokenSource
	HTTPClient *http.Client
	Logger     *slog.Logger
	// UserAgent is sent to the config service, conventionally
	// "statehouse/<version>". Empty sends remoteconfig's default.
	UserAgent string

	tracker remoteconfig.Tracker
}

// Statuses returns a snapshot of the last fetch result for each namespace.
func (f *Fetcher) Statuses() map[string]NamespaceStatus {
	return f.tracker.Statuses()
}

// behaviourDoc mirrors the JSON structure of the statehouse_behaviour
// namespace. applyBehaviour seeds energy, availability and house with
// pointers to copies of the sections cfg already holds, so decoding overlays
// the document onto the current values: a key the document leaves out keeps
// its value instead of becoming zero (#86). See json.go for the per-key
// rules. Adapters are seeded with a fresh struct instead; see applyBehaviour.
//
// A section given as JSON null sets its pointer to nil, which is applied the
// same as an absent section: nothing changes.
type behaviourDoc struct {
	Energy       *EnergyConfig       `json:"energy,omitempty"`
	Availability *AvailabilityConfig `json:"availability,omitempty"`
	House        *HouseConfig        `json:"house,omitempty"`
	Adapters     *AdaptersConfig     `json:"adapters,omitempty"`
}

// ApplyRemote fetches the three statehouse namespaces and applies them to
// cfg:
//
//   - statehouse_classes: each class entry is added to cfg.DeviceClasses.
//   - the devices namespace (cfg.Site.DevicesNamespace): each device entry is
//     added to cfg.Devices.
//   - statehouse_behaviour: each top-level section present (energy,
//     availability, house) is decoded over the section cfg already holds, so
//     a key the document leaves out keeps its value (the default, at startup
//     and on SIGHUP), and a section left out is untouched. The adapters
//     section replaces cfg.Adapters whole: an adapter left out is off. A
//     document with any bad value is refused and changes nothing.
//
// Each namespace is fetched independently, and every failure is recorded on
// /healthz and returned, joined, so one error names all of them. There is no
// local mode: an empty BaseURL is an error.
func (f *Fetcher) ApplyRemote(ctx context.Context, cfg *Config) error {
	if f.BaseURL == "" {
		return errors.New("remote config: no base_url; statehouse has no local mode")
	}
	if !strings.HasPrefix(f.BaseURL, "https://") {
		f.warn("remote config: base_url is not https — bearer token will be transmitted in cleartext", "url", f.BaseURL)
	}
	if f.Tokens != nil {
		// One token serves every namespace: remoteconfig asks for it per request,
		// and the TokenSource answers from its cache. Asking here first means a
		// token that cannot be had costs identity one call rather than three, and
		// that every namespace records the real cause on /healthz. It used to
		// record nothing, so /healthz kept the previous fetch's ok:true while
		// statehouse ran without the namespaces at all.
		if _, err := f.Tokens.Token(ctx); err != nil {
			f.warn("remote config: identity token fetch failed", "error", err)
			err = fmt.Errorf("remote config token: %w", err)
			for _, ns := range []string{nsClasses, cfg.Site.DevicesNamespace, nsBehaviour} {
				if ns != "" {
					f.tracker.Record(ns, err)
				}
			}
			return err
		}
	}
	// Built once per ApplyRemote, so one reload reads every namespace with the
	// same settings.
	c := &remoteconfig.Client{
		BaseURL:    f.BaseURL,
		Tokens:     f.Tokens,
		UserAgent:  f.UserAgent,
		HTTPClient: f.HTTPClient,
	}
	return errors.Join(
		f.applyClasses(ctx, cfg, c),
		f.applyDevices(ctx, cfg, c),
		f.applyBehaviour(ctx, cfg, c),
	)
}

const (
	nsClasses   = "statehouse_classes"
	nsBehaviour = "statehouse_behaviour"
)

func (f *Fetcher) applyClasses(ctx context.Context, cfg *Config, c *remoteconfig.Client) error {
	var remote map[string]DeviceClassConfig
	if err := c.Get(ctx, nsClasses, &remote); err != nil {
		f.warn("remote config: statehouse_classes unavailable", "error", err)
		f.tracker.Record(nsClasses, err)
		return fmt.Errorf("%s: %w", nsClasses, err)
	}
	f.tracker.Record(nsClasses, nil)
	if cfg.DeviceClasses == nil {
		cfg.DeviceClasses = make(map[string]DeviceClassConfig)
	}
	for k, v := range remote {
		cfg.DeviceClasses[k] = v
	}
	return nil
}

func (f *Fetcher) applyDevices(ctx context.Context, cfg *Config, c *remoteconfig.Client) error {
	// Named by config, not hardcoded: devices are per-site, so the namespace a site
	// reads is a property of that site. There is no fallback — Validate refuses to
	// start an unnamed config, and guessing here would ask the service for a
	// namespace nobody named. Callers that skip Validate (tools, tests) get no
	// devices rather than a request for a deleted document.
	ns := cfg.Site.DevicesNamespace
	if ns == "" {
		f.warn("remote config: no devices namespace named, skipping devices fetch")
		return errors.New("devices: site.devices_namespace is not set")
	}

	// Decoded by the shared schema, field by field: a value that does not fit
	// costs that field (a bad threshold, say), not the whole document.
	var raw json.RawMessage
	err := c.Get(ctx, ns, &raw)
	var remote map[string]DeviceConfig
	var problems []devices.Problem
	if err == nil {
		remote, problems, err = devices.Decode(raw)
	}
	if err != nil {
		f.warn("remote config: devices namespace unavailable",
			"namespace", ns, "error", err)
		f.tracker.Record(ns, err)
		return fmt.Errorf("%s: %w", ns, err)
	}
	f.tracker.Record(ns, nil)
	if cfg.Devices == nil {
		cfg.Devices = make(map[string]DeviceConfig)
	}
	for k, v := range remote {
		cfg.Devices[k] = v
	}
	normaliseDevices(cfg.Devices)
	for _, p := range append(problems, devices.Check(cfg.Devices)...) {
		f.warn("remote config: devices problem", "namespace", ns, "device", p.ID,
			"field", p.Field, "code", p.Code, "problem", p.Message)
	}
	return nil
}

func (f *Fetcher) applyBehaviour(ctx context.Context, cfg *Config, c *remoteconfig.Client) error {
	// Seed the sections with copies, not with cfg's own fields: the document
	// must not reach cfg until all of it has decoded and been checked, so a
	// refused document leaves cfg exactly as it was.
	energy, availability, house := cfg.Energy, cfg.Availability, cfg.House
	b := behaviourDoc{
		Energy:       &energy,
		Availability: &availability,
		House:        &house,
		// Adapters is left nil, so encoding/json allocates a fresh struct only
		// when the document has the key: the section replaces cfg.Adapters
		// whole, as documented (an adapter left out is off). That already is
		// decode-over-defaults, since the zero AdaptersConfig is the default
		// (zigbee2mqtt on, the rest off) apart from base_topic, which
		// fillDefaults supplies below. Seeding it with a copy of cfg.Adapters
		// would instead share that copy's Enabled pointers, and decoding
		// would write through them into cfg.
	}
	if err := c.Get(ctx, nsBehaviour, &b); err != nil {
		f.warn("remote config: statehouse_behaviour unavailable", "error", err)
		f.tracker.Record(nsBehaviour, err)
		return fmt.Errorf("%s: %w", nsBehaviour, err)
	}
	// Location() falls back to UTC on a name it cannot resolve, so a typo
	// would apply cleanly and shift the night/day window with nothing showing
	// it. Refuse the document instead, before any of it is applied. This
	// checks the zone that would be applied: the document's if it names one,
	// otherwise the current one carried over by the overlay.
	if b.House != nil && b.House.Timezone != "" {
		if _, err := time.LoadLocation(b.House.Timezone); err != nil {
			err = fmt.Errorf("house.timezone %q: %w", b.House.Timezone, err)
			f.warn("remote config: statehouse_behaviour rejected", "error", err)
			f.tracker.Record(nsBehaviour, err)
			return fmt.Errorf("%s: %w", nsBehaviour, err)
		}
	}
	f.tracker.Record(nsBehaviour, nil)
	// Each of these is the current section with the document's keys laid
	// over it, or nil if the document set the section to null. An absent
	// section is the unchanged copy, so assigning it is a no-op.
	if b.Energy != nil {
		cfg.Energy = *b.Energy
	}
	if b.Availability != nil {
		cfg.Availability = *b.Availability
	}
	if b.House != nil {
		cfg.House = *b.House
	}
	if b.Adapters != nil {
		cfg.Adapters = *b.Adapters
		cfg.Adapters.fillDefaults()
	}
	return nil
}

func (f *Fetcher) warn(msg string, args ...any) {
	if f.Logger != nil {
		f.Logger.Warn(msg, args...)
	}
}
