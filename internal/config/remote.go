package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sweeney/common/remoteconfig"
)

// TokenSource is satisfied by identity.TokenSource. remoteconfig uses it to
// obtain a Bearer token for the config service, and invalidates that token on a
// 401 before retrying once with a fresh credential.
type TokenSource = remoteconfig.TokenSource

// NamespaceStatus records the outcome of the most recent fetch attempt for
// a single config namespace.
type NamespaceStatus = remoteconfig.Status

// Fetcher fetches the three statehouse config namespaces from the remote
// config service and merges them into a local Config. Errors are per-namespace
// and non-fatal: a namespace that cannot be fetched is skipped with a warning
// and the local config value for that section is preserved.
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
// namespace. Pointer fields let us distinguish "key absent" (nil — keep
// local) from "key present but empty" (non-nil — replace local).
type behaviourDoc struct {
	Energy       *EnergyConfig       `json:"energy,omitempty"`
	Availability *AvailabilityConfig `json:"availability,omitempty"`
	House        *HouseConfig        `json:"house,omitempty"`
	Adapters     *AdaptersConfig     `json:"adapters,omitempty"`
}

// ApplyRemote fetches the three statehouse namespaces and merges them into
// cfg. Remote values win over local on overlap. Each namespace is fetched
// independently; a failure on one does not prevent the others from applying.
//
// An empty BaseURL is explicit local mode: nothing is fetched, no token is
// asked for, and no status is recorded.
func (f *Fetcher) ApplyRemote(ctx context.Context, cfg *Config) {
	if f.BaseURL == "" {
		return
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
		// statehouse ran on local config alone.
		if _, err := f.Tokens.Token(ctx); err != nil {
			f.warn("remote config: identity token fetch failed, using local config only", "error", err)
			err = fmt.Errorf("remote config token: %w", err)
			for _, ns := range []string{nsClasses, cfg.Site.DevicesNamespace, nsBehaviour} {
				if ns != "" {
					f.tracker.Record(ns, err)
				}
			}
			return
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
	f.applyClasses(ctx, cfg, c)
	f.applyDevices(ctx, cfg, c)
	f.applyBehaviour(ctx, cfg, c)
}

const (
	nsClasses   = "statehouse_classes"
	nsBehaviour = "statehouse_behaviour"
)

func (f *Fetcher) applyClasses(ctx context.Context, cfg *Config, c *remoteconfig.Client) {
	var remote map[string]DeviceClassConfig
	if err := c.Get(ctx, nsClasses, &remote); err != nil {
		f.warn("remote config: statehouse_classes unavailable, using local", "error", err)
		f.tracker.Record(nsClasses, err)
		return
	}
	f.tracker.Record(nsClasses, nil)
	if cfg.DeviceClasses == nil {
		cfg.DeviceClasses = make(map[string]DeviceClassConfig)
	}
	for k, v := range remote {
		cfg.DeviceClasses[k] = v
	}
}

func (f *Fetcher) applyDevices(ctx context.Context, cfg *Config, c *remoteconfig.Client) {
	// Named by config, not hardcoded: devices are per-site, so the namespace a site
	// reads is a property of that site. There is no fallback — Validate refuses to
	// start an unnamed config, and guessing here would ask the service for a
	// namespace nobody named. Callers that skip Validate (tools, tests) get no
	// devices rather than a request for a deleted document.
	ns := cfg.Site.DevicesNamespace
	if ns == "" {
		f.warn("remote config: no devices namespace named, skipping devices fetch")
		return
	}

	var remote map[string]DeviceConfig
	if err := c.Get(ctx, ns, &remote); err != nil {
		f.warn("remote config: devices namespace unavailable, using local",
			"namespace", ns, "error", err)
		f.tracker.Record(ns, err)
		return
	}
	f.tracker.Record(ns, nil)
	if cfg.Devices == nil {
		cfg.Devices = make(map[string]DeviceConfig)
	}
	for k, v := range remote {
		cfg.Devices[k] = v
	}
	normaliseDevices(cfg.Devices)
}

func (f *Fetcher) applyBehaviour(ctx context.Context, cfg *Config, c *remoteconfig.Client) {
	var b behaviourDoc
	if err := c.Get(ctx, nsBehaviour, &b); err != nil {
		f.warn("remote config: statehouse_behaviour unavailable, using local", "error", err)
		f.tracker.Record(nsBehaviour, err)
		return
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
	}
}

func (f *Fetcher) warn(msg string, args ...any) {
	if f.Logger != nil {
		f.Logger.Warn(msg, args...)
	}
}
