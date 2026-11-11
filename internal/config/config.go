package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sweeney/common/devices"
	"github.com/sweeney/common/remoteconfig"
	"github.com/sweeney/statehouse/internal/origin"
)

// Config is the service configuration. It has two sources, and each field
// has exactly one of them:
//
//   - The local YAML file (Load) holds only what this host needs to start and
//     to reach everything else: site, mqtt, http, recent_log, influx,
//     identity, remote_config and auth.
//   - The config service (Fetcher.ApplyRemote) supplies everything else:
//     adapters, energy, availability and house from statehouse_behaviour,
//     device_classes from statehouse_classes, and devices from the site's
//     devices namespace. These fields are yaml:"-", and Load refuses a local
//     file that sets them (remoteOnlySections), so a local copy can never be
//     silently overridden.
//
// Defaults (Default) fill both halves before either source is applied.
type Config struct {
	// Local: read from the YAML file.
	Site         SiteConfig         `yaml:"site"`
	MQTT         MQTTConfig         `yaml:"mqtt"`
	HTTP         HTTPConfig         `yaml:"http"`
	RecentLog    RecentLogConfig    `yaml:"recent_log"`
	Influx       InfluxConfig       `yaml:"influx"`
	Identity     IdentityConfig     `yaml:"identity"`
	RemoteConfig RemoteConfigConfig `yaml:"remote_config"`
	Auth         AuthConfig         `yaml:"auth"`

	// Remote: from the config service only, never the YAML file. The remote
	// tag is "<the YAML key Load refuses>,<namespace>"; no namespace means
	// the site's devices namespace (site.devices_namespace).
	Energy        EnergyConfig                 `yaml:"-" remote:"energy,statehouse_behaviour"`
	Availability  AvailabilityConfig           `yaml:"-" remote:"availability,statehouse_behaviour"`
	House         HouseConfig                  `yaml:"-" remote:"house,statehouse_behaviour"`
	Adapters      AdaptersConfig               `yaml:"-" remote:"adapters,statehouse_behaviour"`
	DeviceClasses map[string]DeviceClassConfig `yaml:"-" remote:"device_classes,statehouse_classes"`
	Devices       map[string]DeviceConfig      `yaml:"-" remote:"devices"`
}

// remoteOnlySections are the top-level YAML keys that belong to the config
// service, with the namespace each one lives in, read from the remote tags on
// Config so the list cannot drift from the fields. Load refuses them: they
// used to be read locally and then replaced by the remote copy, so a local
// edit could do nothing at all, with no sign why. An empty namespace means
// the site's devices namespace (site.devices_namespace).
var remoteOnlySections = remoteSectionsFromTags()

func remoteSectionsFromTags() []struct{ key, namespace string } {
	var out []struct{ key, namespace string }
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		tag, ok := ct.Field(i).Tag.Lookup("remote")
		if !ok {
			continue
		}
		key, ns, _ := strings.Cut(tag, ",")
		out = append(out, struct{ key, namespace string }{key, ns})
	}
	return out
}

// SiteConfig identifies the property this instance serves and where that property's
// configuration lives.
//
// It is a block rather than a bare id so that adding a second property is a config
// edit rather than a code change: each site names its own devices namespace, which
// is what makes the namespaces per-site rather than one shared document.
type SiteConfig struct {
	// ID matches an entry in the `sites` namespace and supplies the Influx `site`
	// tag value. There is no default: guessing would write a tag asserting which
	// property the readings came from.
	ID string `yaml:"id"`

	// DevicesNamespace is the config namespace holding this site's devices.
	//
	// There is no default. The shared pre-migration namespace it used to fall back
	// to was deleted once every service read its own, so the only guess available
	// names a document that does not exist — and a failed devices fetch is silent:
	// the service starts, reports healthy and serves nothing. It is also not derived
	// from ID, because deriving it would turn a typo in the id into that same silent
	// 404. Both facts are stated so they can be checked against each other.
	DevicesNamespace string `yaml:"devices_namespace"`
}

// UnmarshalYAML accepts either the block form or the bare id it replaced:
//
//	site: home
//	site:
//	  id: home
//	  devices_namespace: devices_home
//
// The deployed config uses the scalar and deploy.sh ships only the binary, so a
// parser that rejected it would take the service down the moment it shipped —
// before anyone could edit the host's config.
func (s *SiteConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var id string
	if err := unmarshal(&id); err == nil {
		s.ID = id
		return nil
	}
	type raw SiteConfig
	var r raw
	if err := unmarshal(&r); err != nil {
		return err
	}
	*s = SiteConfig(r)
	return nil
}

// IdentityConfig holds credentials for the identity service used to
// obtain access tokens for service-to-service calls.
type IdentityConfig struct {
	BaseURL      string `yaml:"base_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// RemoteConfigConfig holds the address of the remote config service.
type RemoteConfigConfig struct {
	BaseURL string `yaml:"base_url"`
}

// AuthConfig configures how inbound HTTP callers are authenticated.
//
// It governs tokens coming *in* to this service's API. IdentityConfig above is
// the other direction — the credentials this service uses to call out. The two
// are separate blocks because they are separate trust relationships: a host may
// hold client credentials for the config service and still refuse every inbound
// service token, or the reverse.
type AuthConfig struct {
	ServiceTokens ServiceTokenConfig `yaml:"service_tokens"`
}

// ServiceTokenConfig decides which client_credentials callers reach the API.
//
// A service principal has no IsActive flag and no role, so the checks that
// bound a user token have no equivalent here. These four knobs are what stands
// in for them, and all four are optional: the default is "any token this
// instance's identity service signed".
//
// That default is deliberate. Statehouse is read-only, sits on a private
// network, and already requires a signed token from a single issuer; the
// identity service decides who gets a client registration at all. Requiring a
// scope by default would have meant every sibling 403ing until identity was
// taught to stamp one, which is the same outage as the bug this replaces. The
// tighter settings exist so a deployment can opt in as identity grows the
// claims to support them.
type ServiceTokenConfig struct {
	// Enabled accepts service tokens at all. Absent means true — see the note
	// above on why the permissive default is the safe one here. Set false to
	// restore the user-token-only behaviour.
	Enabled *bool `yaml:"enabled"`

	// RequiredAudience, when set, requires the token's `aud` to include this
	// value. This is the standard defence against token redirection: without
	// it, a token minted for a sibling service is accepted here too, so any
	// service that can obtain a token can read house state.
	//
	// It is enforced against the parsed service claims rather than by setting
	// JWKSVerifierConfig.RequiredAudience, because that field applies to user
	// tokens as well — and user tokens carry no `aud`, so it would lock every
	// human caller out.
	RequiredAudience string `yaml:"required_audience"`

	// RequiredScope, when set, requires the token's space-delimited `scope` to
	// contain this value. One scope, matched whole.
	RequiredScope string `yaml:"required_scope"`

	// AllowedClients, when non-empty, restricts callers to these `client_id`s,
	// matched exactly. It is the one restriction that needs nothing new from
	// the identity service, so it is the cheapest least-privilege control a
	// deployment can turn on today.
	AllowedClients []string `yaml:"allowed_clients"`
}

// IsEnabled reports whether service tokens are accepted. An absent `enabled`
// means yes.
func (s ServiceTokenConfig) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// MQTTConfig describes broker connectivity. Per-adapter subscription
// topics are now owned by the adapter blocks below, not by MQTT.
type MQTTConfig struct {
	Broker   string `yaml:"broker"`
	ClientID string `yaml:"client_id"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// PublishPrefix is prepended to derived MQTT topics. Defaults to "house".
	PublishPrefix string `yaml:"publish_prefix"`
}

// AdaptersConfig groups per-protocol adapter configuration. Adapters
// translate raw MQTT messages into engine calls that don't carry
// protocol vocabulary, so each protocol gets its own settings block.
type AdaptersConfig struct {
	Zigbee2MQTT Zigbee2MQTTConfig `yaml:"zigbee2mqtt" json:"zigbee2mqtt,omitempty"`
	Boiler      BoilerConfig      `yaml:"boiler"      json:"boiler,omitempty"`
	UPS         UPSConfig         `yaml:"ups"         json:"ups,omitempty"`
	Climate     ClimateConfig     `yaml:"climate"     json:"climate,omitempty"`
	Meter       MeterConfig       `yaml:"meter"       json:"meter,omitempty"`
	Intercom    IntercomConfig    `yaml:"intercom"    json:"intercom,omitempty"`
	Tanktemp    TanktempConfig    `yaml:"tanktemp"    json:"tanktemp,omitempty"`
}

// Zigbee2MQTTConfig configures the Zigbee2MQTT adapter.
type Zigbee2MQTTConfig struct {
	// Enabled defaults to true if the block is present at all. Set to
	// false to disable the adapter without removing the block.
	Enabled *bool `yaml:"enabled"    json:"enabled,omitempty"`
	// BaseTopic is the topic prefix of the Z2M bridge ("zigbee2mqtt").
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

// IsEnabled reports whether the adapter should be wired. Defaults to
// true when the Adapters block is absent — the simplest config
// (broker + nothing else) still gets a working Z2M adapter.
func (z Zigbee2MQTTConfig) IsEnabled() bool {
	if z.Enabled == nil {
		return true
	}
	return *z.Enabled
}

// BoilerConfig configures the sweeney/boiler-sensor adapter, which
// listens on energy/boiler/sensor/{events,system}. It is OFF by
// default — the boiler-sensor publisher isn't a generic protocol so
// users opt in by enabling it.
type BoilerConfig struct {
	Enabled *bool `yaml:"enabled"    json:"enabled,omitempty"`
	// BaseTopic is the topic prefix the publisher uses; defaults to
	// "energy/boiler/sensor". The adapter appends "/events" and
	// "/system" for its two subscriptions.
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

// IsEnabled reports whether the boiler adapter should be wired.
// Default is false — adapters that target a specific bespoke device
// shouldn't auto-enable.
func (b BoilerConfig) IsEnabled() bool {
	if b.Enabled == nil {
		return false
	}
	return *b.Enabled
}

// UPSConfig configures the NUT-via-MQTT UPS adapter.
type UPSConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (u UPSConfig) IsEnabled() bool {
	if u.Enabled == nil {
		return false
	}
	return *u.Enabled
}

// ClimateConfig configures the weather station adapter.
type ClimateConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (c ClimateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// MeterConfig configures the Glow/SMETS2 smart meter adapter.
type MeterConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (m MeterConfig) IsEnabled() bool {
	if m.Enabled == nil {
		return false
	}
	return *m.Enabled
}

// IntercomConfig configures the Intercom (Asterisk-via-MQTT) adapter.
// BaseTopic defaults to "asterisk".
type IntercomConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

func (i IntercomConfig) IsEnabled() bool {
	if i.Enabled == nil {
		return false
	}
	return *i.Enabled
}

// TanktempConfig configures the sweeney/tanktemp adapter for hot water
// cylinder probes. It subscribes to {base}/+/state and {base}/status.
// BaseTopic defaults to "energy/hot_water". Off by default: enable it only
// once the devices namespace has entries for the cylinder and its probes,
// or the probes resolve as unclassified.
type TanktempConfig struct {
	Enabled   *bool  `yaml:"enabled"    json:"enabled,omitempty"`
	BaseTopic string `yaml:"base_topic" json:"base_topic,omitempty"`
}

// IsEnabled reports whether the adapter is on. It defaults to false.
func (t TanktempConfig) IsEnabled() bool {
	if t.Enabled == nil {
		return false
	}
	return *t.Enabled
}

type HTTPConfig struct {
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"`

	// AllowedOrigins lists the browser origins permitted to read this API
	// cross-origin. Empty means none, which is how the service behaved before
	// CORS existed — so deploying that change against an unedited config alters
	// nothing, and turning CORS on is an explicit act.
	//
	//	allowed_origins:
	//	  - "https://*.swee.net"   # any subdomain, but not the apex
	//	  - "http://localhost:*"   # any port, for development
	//
	// See internal/origin for the accepted forms. A malformed entry is refused
	// by Validate rather than skipped.
	//
	// Deliberately local-only: this is the one setting that says which pages
	// may spend a token the browser already holds, and putting it in a remote
	// namespace would place it on the far side of a network call that fails
	// open. A remote widening would be an unreviewed grant, and a remote
	// tightening would be silently dropped whenever the fetch failed.
	AllowedOrigins []string `yaml:"allowed_origins"`
}

type RecentLogConfig struct {
	Path           string `yaml:"path"`
	RetentionHours int    `yaml:"retention_hours"`
	MaxSizeMB      int    `yaml:"max_size_mb"`
}

type InfluxConfig struct {
	Enabled   bool   `yaml:"enabled"`
	URL       string `yaml:"url"`
	Org       string `yaml:"org"`
	Bucket    string `yaml:"bucket"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type EnergyConfig struct {
	DivergenceWarningPct float64       `yaml:"divergence_warning_pct"  json:"divergence_warning_pct"`
	MaxIntegrationGap    time.Duration `yaml:"max_integration_gap"     json:"-"`
}

type AvailabilityConfig struct {
	OfflineDebounce time.Duration `yaml:"offline_debounce" json:"-"`
}

type HouseConfig struct {
	// QuietAfter marks the house as quiet when no activity has occurred
	// for this long.
	QuietAfter time.Duration `yaml:"quiet_after"    json:"-"`
	// EmptyAfter marks the house as empty if quiet for this long and no
	// signals of presence have been seen.
	EmptyAfter time.Duration `yaml:"empty_after"    json:"-"`
	// SleepingAfter is the sustained quiet duration beyond which the house
	// mode transitions to sleeping (when occupied).
	SleepingAfter time.Duration `yaml:"sleeping_after" json:"-"`
	// Timezone names a tz database location (e.g. "Europe/London") used to
	// classify the mode dimension's night/day hour window. Empty means UTC
	// (back-compat for existing configs); "Local" uses the host time zone.
	// The remote fetcher refuses a statehouse_behaviour document whose
	// timezone time.LoadLocation cannot resolve (typo, missing tzdata), so
	// the mistake stops startup, or fails a SIGHUP reload, rather than
	// showing up as mis-bucketed mode readings.
	Timezone string `yaml:"timezone" json:"timezone,omitempty"`
}

// Location returns the time.Location implied by Timezone. Falls back to
// time.UTC on parse failure; the remote fetcher refuses invalid timezones
// before they are applied, so this fallback only matters for hand-crafted
// HouseConfig values in tests.
func (h HouseConfig) Location() *time.Location {
	if h.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(h.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Thresholds are per-device activity-detection thresholds. The type, and its
// JSON form, belong to the shared devices schema.
type Thresholds = devices.Thresholds

// DeviceClassConfig describes one device class profile.
type DeviceClassConfig struct {
	NameHints         []string   `yaml:"name_hints"          json:"name_hints,omitempty"`
	DefaultThresholds Thresholds `yaml:"default_thresholds"  json:"default_thresholds,omitempty"`
	EnergyStrategy    string     `yaml:"energy_strategy"     json:"energy_strategy,omitempty"`
	// StalenessSeconds overrides the default class staleness threshold used
	// by the API DTO layer. When nil the class default is used.
	StalenessSeconds *int `yaml:"staleness_seconds" json:"staleness_seconds,omitempty"`
}

// DeviceConfig is one device in a devices_<site> namespace:
// the shared schema, whose Place, Coverage and Normalise statehouse uses as is.
type DeviceConfig = devices.Device

// Default returns a config populated with safe defaults. The local YAML
// overrides the local fields, and the config service the remote ones.
func Default() Config {
	cfg := Config{
		MQTT: MQTTConfig{
			Broker:        "tcp://localhost:1883",
			ClientID:      "statehouse",
			PublishPrefix: "house",
		},
		HTTP: HTTPConfig{Listen: ":8080"},
		RecentLog: RecentLogConfig{
			Path:           "/var/lib/statehouse/events.jsonl",
			RetentionHours: 72,
			MaxSizeMB:      256,
		},
		Energy: EnergyConfig{
			DivergenceWarningPct: 20,
			MaxIntegrationGap:    30 * time.Minute,
		},
		Availability: AvailabilityConfig{
			OfflineDebounce: 30 * time.Second,
		},
		House: HouseConfig{
			QuietAfter:    30 * time.Minute,
			EmptyAfter:    6 * time.Hour,
			SleepingAfter: 2 * time.Hour,
		},
	}
	cfg.Adapters.fillDefaults()
	return cfg
}

// fillDefaults gives every adapter with no base_topic its protocol's default.
// It runs on Default and again after the remote adapters block is applied,
// since that block replaces the whole section.
func (a *AdaptersConfig) fillDefaults() {
	for _, f := range []struct {
		topic *string
		def   string
	}{
		{&a.Zigbee2MQTT.BaseTopic, "zigbee2mqtt"},
		{&a.Boiler.BaseTopic, "energy/boiler/sensor"},
		{&a.UPS.BaseTopic, "ups"},
		{&a.Climate.BaseTopic, "climate"},
		{&a.Meter.BaseTopic, "energy"},
		{&a.Intercom.BaseTopic, "asterisk"},
		// Mirrors tanktemp.DefaultBase; config must not import an adapter.
		{&a.Tanktemp.BaseTopic, "energy/hot_water"},
	} {
		if *f.topic == "" {
			*f.topic = f.def
		}
	}
}

// Load reads the local YAML file from path on top of the defaults. It reads
// only the local fields (see Config); the remote ones are filled later by
// Fetcher.ApplyRemote, and a file that sets one is refused.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := rejectRemoteOnlySections(raw, path); err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Influx.Token == "" && cfg.Influx.TokenFile != "" {
		tok, err := os.ReadFile(cfg.Influx.TokenFile)
		if err != nil {
			return cfg, fmt.Errorf("read influx token: %w", err)
		}
		cfg.Influx.Token = string(trimTrailingNewline(tok))
	}
	if cfg.MQTT.PublishPrefix == "" {
		cfg.MQTT.PublishPrefix = "house"
	}
	return cfg, nil
}

// rejectRemoteOnlySections refuses a local file that sets any section the
// config service owns, naming every one at once and where each now lives.
func rejectRemoteOnlySections(raw []byte, path string) error {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	var found []string
	for _, s := range remoteOnlySections {
		if _, ok := top[s.key]; !ok {
			continue
		}
		ns := s.namespace
		if ns == "" {
			ns = "the site's devices namespace (site.devices_namespace, e.g. devices_home)"
		}
		found = append(found, fmt.Sprintf("  %s: lives in %s", s.key, ns))
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf("%s sets sections that come only from the config service "+
		"(remote_config.base_url), never from the local file:\n\n%s\n\nMove them to "+
		"those namespaces and delete them from %s. They used to be read locally and "+
		"then silently replaced by the remote copy, so a local edit could have no "+
		"effect. See docs/remote-config.md", path, strings.Join(found, "\n"), path)
}

// normaliseDevices converts legacy ieee_address/friendly_name shorthands
// into the canonical scheme/primary/display fields. Called by the remote
// config fetcher for every devices namespace it applies.
func normaliseDevices(m map[string]DeviceConfig) {
	for id, d := range m {
		d.Normalise()
		m[id] = d
	}
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// Validate reports whether the config is complete enough to run a service.
//
// It is deliberately separate from Load: Load parses and normalises, and is used by
// tools and tests that never start a service, while Validate is the gate a running
// service passes through.
//
// Both halves of the site block are errors rather than warnings, for the same reason.
//
// An unset id means the Influx `site` tag is missing, producing exactly the ambiguous
// untagged history the tag exists to prevent, which no later work can recover.
//
// An unnamed devices namespace would have nothing to fetch, so the service would have
// no devices at all. It is refused here, with a message naming the fix, rather than
// left to fail later as a fetch error. A service that looks healthy and serves nothing
// is worse than one that refuses to start.
//
// A malformed browser origin is the same shape of failure a third time. Skipping the
// entry would leave an origin unallowlisted that the operator believes is allowlisted,
// or leave one admitted that the entry meant to replace — and nothing on the server
// side shows either, because a CORS decision is only ever visible in the browser that
// made the request.
func (c Config) Validate() error {
	if c.Site.ID == "" {
		return fmt.Errorf("site is not set: add a site block naming the property this " +
			"instance serves, e.g.\n\n  site:\n    id: <id>\n    devices_namespace: " +
			"devices_<id>\n\nwhere id matches an entry in the sites namespace. There is " +
			"no default because guessing would tag readings with the wrong property")
	}
	if c.Site.DevicesNamespace == "" {
		return fmt.Errorf("site.devices_namespace is not set: name the config namespace "+
			"holding this site's devices, e.g.\n\n  site:\n    id: %s\n    "+
			"devices_namespace: devices_%s\n\nThere is no default: the shared namespace "+
			"this used to fall back to has been deleted, and a failed devices fetch is "+
			"silent — the service would start, report healthy and serve no devices at all",
			c.Site.ID, c.Site.ID)
	}
	// A name config would refuse is refused here. remoteconfig rejects it without
	// sending a request, so left to run it would surface only as a devices fetch
	// error, without saying the name itself is the problem.
	if err := remoteconfig.ValidName(c.Site.DevicesNamespace); err != nil {
		return fmt.Errorf("site.devices_namespace %q is not a name the config service "+
			"accepts (lowercase letters, digits, _ and -, at most 64), so it could never "+
			"be fetched: %w", c.Site.DevicesNamespace, err)
	}
	// Devices, classes, adapters and behaviour come only from the config
	// service, so without it the service would start, report healthy and do
	// nothing. There is no local mode.
	for _, f := range []struct{ value, key string }{
		{c.RemoteConfig.BaseURL, "remote_config.base_url"},
		{c.Identity.BaseURL, "identity.base_url"},
		{c.Identity.ClientID, "identity.client_id"},
		{c.Identity.ClientSecret, "identity.client_secret"},
	} {
		if f.value == "" {
			return fmt.Errorf("%s is not set: statehouse reads its devices, classes, "+
				"adapters and behaviour only from the config service, and needs "+
				"remote_config.base_url and identity credentials to fetch them. See "+
				"docs/remote-config.md", f.key)
		}
	}
	if _, err := origin.Compile(c.HTTP.AllowedOrigins); err != nil {
		return fmt.Errorf("http.allowed_origins: %w\n\nAn entry is an origin — a scheme, a "+
			"host and an optional port, with no trailing slash or path. Accepted forms are an "+
			"exact origin (https://app.swee.net), a subdomain wildcard (https://*.swee.net, "+
			"which does not admit the apex), a port wildcard (http://localhost:*) or \"*\" for "+
			"every origin. A bad entry is refused rather than skipped because a skipped one "+
			"fails silently in both directions: an origin the operator believes is allowlisted "+
			"is not, or one they believe is excluded was never parsed", err)
	}
	return c.Auth.ServiceTokens.validate()
}

// validate rejects service-token settings that would silently match nothing.
//
// Each of these is a config that parses, starts, and then turns away every
// sibling service with a 403 or 401 the operator has no way to explain from the
// outside — the same shape of silent failure as an unnamed devices namespace,
// and refused at startup for the same reason.
func (s ServiceTokenConfig) validate() error {
	// Surrounding whitespace first, because strings.Fields collapses it: a
	// padded value looks like a single token to the checks below while the
	// matchers compare the raw string and match nothing. A trailing space in a
	// YAML scalar is invisible in review and in a diff, which makes it the more
	// likely typo of the two.
	//
	// Refused rather than trimmed. Trimming would start the service on a config
	// that does not say what it reads as, and nothing would ever tell the
	// operator the typo was there.
	if s.RequiredAudience != strings.TrimSpace(s.RequiredAudience) {
		return fmt.Errorf("auth.service_tokens.required_audience %q is padded with "+
			"whitespace: it is matched whole against the token's aud claim, so a padded "+
			"value can never match and every service token would be refused with a 401. "+
			"Write it without the surrounding space", s.RequiredAudience)
	}
	if s.RequiredScope != strings.TrimSpace(s.RequiredScope) {
		return fmt.Errorf("auth.service_tokens.required_scope %q is padded with whitespace: "+
			"it is matched whole against an element of the token's space-delimited scope "+
			"claim, so a padded value can never match and every service token would be "+
			"refused with a 403. Write it without the surrounding space", s.RequiredScope)
	}
	if f := strings.Fields(s.RequiredAudience); len(f) > 1 {
		return fmt.Errorf("auth.service_tokens.required_audience %q contains a space: it "+
			"names one audience, matched whole against the token's aud claim, and a value "+
			"with a space in it can never match", s.RequiredAudience)
	}
	if f := strings.Fields(s.RequiredScope); len(f) > 1 {
		return fmt.Errorf("auth.service_tokens.required_scope %q contains a space: it names "+
			"one scope, matched whole against the token's space-delimited scope claim, so "+
			"%q would match nothing. Require the single scope every permitted caller holds",
			s.RequiredScope, s.RequiredScope)
	}
	for i, c := range s.AllowedClients {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("auth.service_tokens.allowed_clients[%d] is empty: an empty "+
				"entry matches no client_id but does switch the allowlist on, so the list "+
				"reads as \"permit nothing\". Remove the entry, or remove the list to "+
				"permit any client the identity service issued a token to", i)
		}
		if c != strings.TrimSpace(c) {
			return fmt.Errorf("auth.service_tokens.allowed_clients[%d] %q is padded with "+
				"whitespace: entries are matched exactly against the token's client_id, so "+
				"this one permits nobody. Write it without the surrounding space", i, c)
		}
	}
	return nil
}

// CoverageHouse is the sentinel meaning a device's readings describe the whole
// property rather than the room it sits in.
const CoverageHouse = devices.CoverageHouse
