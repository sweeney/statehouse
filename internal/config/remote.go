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
	// DeviceChecks are the advisory checks run over every fetched devices
	// document, their problems logged as warnings. nil means DefaultDeviceChecks,
	// which is what production uses; tests inject fakes to exercise the wiring.
	DeviceChecks []DeviceCheck

	tracker remoteconfig.Tracker
}

// deviceChecks returns the checks to run: DeviceChecks, or the defaults when nil.
// An explicitly empty, non-nil list runs none.
func (f *Fetcher) deviceChecks() []DeviceCheck {
	if f.DeviceChecks == nil {
		return DefaultDeviceChecks
	}
	return f.DeviceChecks
}

// Statuses returns a snapshot of the last fetch result for each namespace.
func (f *Fetcher) Statuses() map[string]NamespaceStatus {
	return f.tracker.Statuses()
}

// behaviourDoc mirrors the JSON structure of the statehouse_behaviour
// namespace. Pointer fields let us distinguish "section absent" (nil — keep
// cfg's section) from "section present" (non-nil — replace cfg's section).
// A present energy, availability or house section has been decoded over a
// copy of Default()'s, so a key it leaves out is the default (#86; see
// json.go). A present adapters section starts from zero plus fillDefaults.
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
//   - statehouse_behaviour: each top-level section present (adapters, energy,
//     availability, house) replaces that section of cfg. Within energy,
//     availability and house, a key the document leaves out (or sets to null)
//     takes its default. A section the document leaves out keeps its default.
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
	// The decoder's problems plus every advisory check's (by default devices.Check
	// and statehouse's own, e.g. a probe relying on an inherited room, #83), sorted
	// together by device id. All are advisory: logged the same way, no device lost.
	for _, p := range runDeviceChecks(cfg.Devices, f.deviceChecks(), problems) {
		f.warn("remote config: devices problem", "namespace", ns, "device", p.ID,
			"field", p.Field, "code", p.Code, "problem", p.Message)
	}
	return nil
}

func (f *Fetcher) applyBehaviour(ctx context.Context, cfg *Config, c *remoteconfig.Client) error {
	var b behaviourDoc
	if err := c.Get(ctx, nsBehaviour, &b); err != nil {
		f.warn("remote config: statehouse_behaviour unavailable", "error", err)
		f.tracker.Record(nsBehaviour, err)
		return fmt.Errorf("%s: %w", nsBehaviour, err)
	}
	// Location() falls back to UTC on a name it cannot resolve, so a typo
	// would apply cleanly and shift the night/day window with nothing showing
	// it. Refuse the document instead, before any of it is applied.
	if b.House != nil && b.House.Timezone != "" {
		if _, err := time.LoadLocation(b.House.Timezone); err != nil {
			err = fmt.Errorf("house.timezone %q: %w", b.House.Timezone, err)
			f.warn("remote config: statehouse_behaviour rejected", "error", err)
			f.tracker.Record(nsBehaviour, err)
			return fmt.Errorf("%s: %w", nsBehaviour, err)
		}
	}
	f.tracker.Record(nsBehaviour, nil)
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
