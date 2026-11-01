package main

// rules.go turns MQTT messages from the existing ecosystem into candidate
// notifications. It is pure: no I/O, no clock except the one passed in, so the
// whole mapping is table-testable against real payload shapes.
//
// Sources, and why each one:
//
//   house/events/derived     statehouse's derived events. cycle_finished is the
//                            dishwasher/washer signal; intercom_ringing is the
//                            doorbell (statehouse already folds asterisk-mqtt in).
//   ups/{label}/outage       ups-mqtt's call-to-action topic. Retained, and
//                            re-published every poll, so it must be deduped on
//                            outage_started_at, not on arrival.
//   ups/{label}/last_outage  ups-mqtt's outage summary. Retained and never
//                            cleared, so a fresh subscriber always receives the
//                            last one — only a recent ended_at is news.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Kind string

const (
	KindDoorbell      Kind = "doorbell"
	KindApplianceDone Kind = "appliance_finished"
	KindUPSOnBattery  Kind = "ups_on_battery"
	KindUPSLowBattery Kind = "ups_low_battery"
	KindUPSRestored   Kind = "ups_restored"
	KindExternal      Kind = "external" // posted by a sibling service via /api/notify
)

// CatalogEntry is what the web app renders as a preference toggle.
type CatalogEntry struct {
	Kind        Kind   `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description"`
	DefaultOn   bool   `json:"default_on"`
	// BypassQuiet marks kinds that still ring during a user's quiet hours
	// unless they say otherwise. Losing power at 3am is the case that matters.
	BypassQuiet bool `json:"bypass_quiet"`
}

var Catalog = []CatalogEntry{
	{KindDoorbell, "Doorbell", "Someone rang the door intercom", true, false},
	{KindApplianceDone, "Appliance finished", "Dishwasher, washing machine or dryer cycle finished", false, false},
	{KindUPSOnBattery, "Power cut", "A UPS switched to battery", true, true},
	{KindUPSLowBattery, "UPS battery low", "A UPS is about to run out", true, true},
	{KindUPSRestored, "Power restored", "Mains came back after an outage", false, false},
	{KindExternal, "Other services", "Messages from sibling services (bins, etc.)", false, false},
}

func catalogEntry(k Kind) (CatalogEntry, bool) {
	for _, c := range Catalog {
		if c.Kind == k {
			return c, true
		}
	}
	return CatalogEntry{}, false
}

type Urgency string // RFC 8030 §5.3

const (
	UrgencyNormal Urgency = "normal"
	UrgencyHigh   Urgency = "high"
)

// Notification is a candidate message, before per-user filtering.
type Notification struct {
	Kind Kind
	// Subject is what a user can narrow a kind to: a device id, a UPS label,
	// an intercom extension. Empty means "not narrowable".
	Subject string
	Title   string
	Body    string
	URL     string
	// Topic collapses undelivered messages at the push service (RFC 8030 §5.4):
	// a phone that was offline through an outage gets the latest UPS state,
	// not a backlog. ≤32 base64url characters.
	Topic   string
	Urgency Urgency
	// TTL bounds how long the push service holds a message for an offline
	// device. A doorbell ten minutes late is noise, so it gets a short one.
	TTL time.Duration
	// DedupeKey is persisted once sent, so retained topics and restarts never
	// produce a second copy of the same real-world event.
	DedupeKey string
}

type RulesConfig struct {
	HousePrefix string            `yaml:"house_prefix"` // default "house"
	UPSPrefix   string            `yaml:"ups_prefix"`   // default "ups"
	PublicURL   string            `yaml:"public_url"`
	DisplayName map[string]string `yaml:"display_names"` // device id / ups label → name
	// DoorExtensions are the intercom extensions that are door stations.
	// statehouse emits intercom_ringing for every call, including an internal
	// call from the kitchen to the office, so "ringing" alone is not "doorbell".
	DoorExtensions []string `yaml:"door_extensions"`
	// RestoredWindow: how recent a last_outage ended_at must be to be news.
	RestoredWindow time.Duration `yaml:"restored_window"`
}

func (c *RulesConfig) defaults() {
	if c.HousePrefix == "" {
		c.HousePrefix = "house"
	}
	if c.UPSPrefix == "" {
		c.UPSPrefix = "ups"
	}
	if c.RestoredWindow == 0 {
		c.RestoredWindow = 30 * time.Minute
	}
}

// Topics are the MQTT subscriptions the rules need.
func (c RulesConfig) Topics() []string {
	return []string{
		c.HousePrefix + "/events/derived",
		c.UPSPrefix + "/+/outage",
		c.UPSPrefix + "/+/last_outage",
	}
}

type Rules struct{ cfg RulesConfig }

func NewRules(cfg RulesConfig) *Rules { cfg.defaults(); return &Rules{cfg} }

func (r *Rules) Translate(topic string, payload []byte, now time.Time) []Notification {
	switch {
	case topic == r.cfg.HousePrefix+"/events/derived":
		return r.derived(payload)
	case strings.HasPrefix(topic, r.cfg.UPSPrefix+"/") && strings.HasSuffix(topic, "/last_outage"):
		return r.lastOutage(upsLabel(topic, r.cfg.UPSPrefix), payload, now)
	case strings.HasPrefix(topic, r.cfg.UPSPrefix+"/") && strings.HasSuffix(topic, "/outage"):
		return r.outage(upsLabel(topic, r.cfg.UPSPrefix), payload)
	}
	return nil
}

func upsLabel(topic, prefix string) string {
	parts := strings.Split(strings.TrimPrefix(topic, prefix+"/"), "/")
	return parts[0]
}

func (r *Rules) name(id string) string {
	if n, ok := r.cfg.DisplayName[id]; ok {
		return n
	}
	// kitchen_dishwasher → Kitchen dishwasher
	s := strings.ReplaceAll(strings.ReplaceAll(id, "_", " "), "-", " ")
	if s == "" {
		return id
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (r *Rules) url(path string) string { return strings.TrimRight(r.cfg.PublicURL, "/") + path }

// derivedEvent mirrors statehouse's model.DerivedEvent wire shape.
type derivedEvent struct {
	ID          string         `json:"id"`
	Timestamp   time.Time      `json:"ts"`
	Type        string         `json:"type"`
	DeviceID    string         `json:"device_id"`
	DeviceClass string         `json:"device_class"`
	Evidence    map[string]any `json:"evidence"`
}

func (r *Rules) derived(payload []byte) []Notification {
	var ev derivedEvent
	if json.Unmarshal(payload, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "cycle_finished":
		// statehouse also emits cycle_finished for binary_state_device (boiler
		// relays). Without this filter every hot-water cycle would buzz a phone.
		if ev.DeviceClass != "cycle_power_device" {
			return nil
		}
		body := "Cycle finished"
		if d, ok := ev.Evidence["duration_seconds"].(float64); ok && d > 0 {
			body = fmt.Sprintf("Cycle finished after %s", humanDuration(time.Duration(d)*time.Second))
		}
		if kwh, ok := ev.Evidence["selected_energy_kwh"].(float64); ok && kwh > 0 {
			body += fmt.Sprintf(" · %.2f kWh", kwh)
		}
		return []Notification{{
			Kind: KindApplianceDone, Subject: ev.DeviceID,
			Title: r.name(ev.DeviceID) + " finished", Body: body,
			URL: r.url("/"), Topic: topicKey("cyc", ev.DeviceID),
			Urgency: UrgencyNormal, TTL: 6 * time.Hour,
			DedupeKey: "evt:" + ev.Type + ":" + ev.DeviceID + ":" + ev.Timestamp.UTC().Format(time.RFC3339),
		}}
	case "intercom_ringing":
		from, _ := ev.Evidence["from_extension"].(string)
		if !contains(r.cfg.DoorExtensions, from) {
			return nil
		}
		callID, _ := ev.Evidence["call_id"].(string)
		who, _ := ev.Evidence["from_name"].(string)
		if who == "" {
			who = "Front door"
		}
		return []Notification{{
			Kind: KindDoorbell, Subject: from,
			Title: "Doorbell", Body: who + " is ringing",
			URL: r.url("/"), Topic: "doorbell",
			Urgency: UrgencyHigh, TTL: 60 * time.Second,
			DedupeKey: "call:" + callID,
		}}
	}
	return nil
}

// outagePayload mirrors ups-mqtt's {prefix}/{label}/outage.
type outagePayload struct {
	OutageStartedAt    string  `json:"outage_started_at"`
	BatteryChargePct   float64 `json:"battery_charge_pct"`
	BatteryRuntimeMins float64 `json:"battery_runtime_mins"`
	LoadWatts          float64 `json:"load_watts"`
	LowBattery         bool    `json:"low_battery"`
}

func (r *Rules) outage(label string, payload []byte) []Notification {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return nil // the clear; last_outage reports the end
	}
	var p outagePayload
	if json.Unmarshal(payload, &p) != nil || p.OutageStartedAt == "" {
		return nil
	}
	name := r.name(label)
	base := "ups:" + label + ":" + p.OutageStartedAt
	out := []Notification{{
		Kind: KindUPSOnBattery, Subject: label,
		Title: "Power cut — " + name + " on battery",
		Body:  fmt.Sprintf("%.0f%% charge, about %.0f min left at %.0f W", p.BatteryChargePct, p.BatteryRuntimeMins, p.LoadWatts),
		URL:   r.url("/"), Topic: topicKey("ups", label),
		Urgency: UrgencyHigh, TTL: time.Hour,
		DedupeKey: base + ":ob",
	}}
	if p.LowBattery {
		out = append(out, Notification{
			Kind: KindUPSLowBattery, Subject: label,
			Title: name + " battery low",
			Body:  fmt.Sprintf("%.0f%% left, about %.0f min", p.BatteryChargePct, p.BatteryRuntimeMins),
			URL:   r.url("/"), Topic: topicKey("ups", label),
			Urgency: UrgencyHigh, TTL: 30 * time.Minute,
			DedupeKey: base + ":lb",
		})
	}
	return out
}

type lastOutagePayload struct {
	StartedAt         string `json:"started_at"`
	EndedAt           string `json:"ended_at"`
	DurationSecs      int    `json:"duration_secs"`
	LowBatteryReached bool   `json:"low_battery_reached"`
	MinChargePct      int    `json:"min_battery_charge_pct"`
}

func (r *Rules) lastOutage(label string, payload []byte, now time.Time) []Notification {
	var p lastOutagePayload
	if json.Unmarshal(payload, &p) != nil || p.StartedAt == "" {
		return nil
	}
	ended, err := time.Parse(time.RFC3339, p.EndedAt)
	if err != nil || now.Sub(ended) > r.cfg.RestoredWindow {
		return nil // retained history, not news
	}
	return []Notification{{
		Kind: KindUPSRestored, Subject: label,
		Title: "Power restored — " + r.name(label),
		Body:  fmt.Sprintf("Outage lasted %s; battery fell to %d%%", humanDuration(time.Duration(p.DurationSecs)*time.Second), p.MinChargePct),
		URL:   r.url("/"), Topic: topicKey("ups", label),
		Urgency: UrgencyNormal, TTL: 6 * time.Hour,
		DedupeKey: "ups:" + label + ":" + p.StartedAt + ":restored",
	}}
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "under a minute"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %02dm", h, m)
}

// topicKey builds an RFC 8030 Topic: base64url alphabet, ≤32 chars.
func topicKey(prefix, id string) string {
	var b strings.Builder
	for _, c := range prefix + "-" + id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			b.WriteRune(c)
		}
	}
	s := b.String()
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
