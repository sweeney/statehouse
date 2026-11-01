package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

var now = time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)

func rules() *Rules {
	return NewRules(RulesConfig{PublicURL: "https://bells.example", DoorExtensions: []string{"100"},
		DisplayName: map[string]string{"office-ups": "Office UPS"}})
}

func TestRules(t *testing.T) {
	r := rules()
	cases := []struct {
		name, topic, payload string
		want                 []Kind
	}{
		{"dishwasher finished", "house/events/derived",
			`{"id":"e1","ts":"2026-10-31T20:00:00Z","type":"cycle_finished","device_id":"kitchen_dishwasher","device_class":"cycle_power_device","evidence":{"duration_seconds":8100,"selected_energy_kwh":0.92}}`,
			[]Kind{KindApplianceDone}},
		{"boiler relay cycle is not an appliance", "house/events/derived",
			`{"type":"cycle_finished","device_id":"hot_water","device_class":"binary_state_device"}`, nil},
		{"door station rings", "house/events/derived",
			`{"type":"intercom_ringing","evidence":{"call_id":"1770.4","from_extension":"100","from_name":"Front door"}}`,
			[]Kind{KindDoorbell}},
		{"internal call is not the doorbell", "house/events/derived",
			`{"type":"intercom_ringing","evidence":{"call_id":"1770.5","from_extension":"1986","from_name":"Martin"}}`, nil},
		{"ups on battery", "ups/office-ups/outage",
			`{"outage_started_at":"2026-10-31T20:59:00Z","battery_charge_pct":100,"battery_runtime_mins":68,"load_watts":72,"low_battery":false}`,
			[]Kind{KindUPSOnBattery}},
		{"ups low battery", "ups/office-ups/outage",
			`{"outage_started_at":"2026-10-31T20:59:00Z","battery_charge_pct":9,"battery_runtime_mins":3,"low_battery":true}`,
			[]Kind{KindUPSOnBattery, KindUPSLowBattery}},
		{"outage clear is silent", "ups/office-ups/outage", ``, nil},
		{"recent restore", "ups/office-ups/last_outage",
			`{"started_at":"2026-10-31T20:10:00Z","ended_at":"2026-10-31T20:50:00Z","duration_secs":2400,"min_battery_charge_pct":61}`,
			[]Kind{KindUPSRestored}},
		{"retained old restore is history", "ups/office-ups/last_outage",
			`{"started_at":"2026-10-13T10:02:10Z","ended_at":"2026-10-13T10:40:00Z","duration_secs":2270}`, nil},
		{"garbage", "house/events/derived", `not json`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Translate(c.topic, []byte(c.payload), now)
			if len(got) != len(c.want) {
				t.Fatalf("got %d notifications %+v, want %v", len(got), got, c.want)
			}
			for i := range got {
				if got[i].Kind != c.want[i] {
					t.Errorf("[%d] kind %s, want %s", i, got[i].Kind, c.want[i])
				}
				if len(got[i].Topic) > 32 || got[i].DedupeKey == "" {
					t.Errorf("[%d] bad topic/dedupe %+v", i, got[i])
				}
			}
		})
	}
	n := r.Translate("ups/office-ups/outage", []byte(`{"outage_started_at":"x","battery_charge_pct":80,"battery_runtime_mins":40,"load_watts":90}`), now)[0]
	if n.Title != "Power cut — Office UPS on battery" {
		t.Errorf("title %q", n.Title)
	}
}

func TestOutageRepublishDedupes(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "s.json"))
	r := rules()
	sent := 0
	for i := 0; i < 5; i++ { // ups-mqtt re-publishes every poll
		for _, n := range r.Translate("ups/office-ups/outage", []byte(`{"outage_started_at":"2026-10-31T20:59:00Z","battery_charge_pct":90}`), now) {
			if s.FirstSighting(n.DedupeKey, now) {
				sent++
			}
		}
	}
	if sent != 1 {
		t.Fatalf("sent %d, want 1", sent)
	}
	// survives restart
	s2, _ := OpenStore(s.path)
	if s2.FirstSighting("ups:office-ups:2026-10-31T20:59:00Z:ob", now) {
		t.Fatal("dedupe key lost across restart")
	}
}

func TestPrefs(t *testing.T) {
	p := DefaultPrefs()
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	door := Notification{Kind: KindDoorbell, Subject: "100"}
	ups := Notification{Kind: KindUPSOnBattery, Subject: "office-ups"}
	dish := Notification{Kind: KindApplianceDone, Subject: "kitchen_dishwasher"}

	if !p.Wants(door, at(12, 0)) || !p.Wants(ups, at(12, 0)) || p.Wants(dish, at(12, 0)) {
		t.Fatal("defaults: doorbell+ups on, appliances off")
	}
	p.Kinds[KindApplianceDone] = KindPref{Enabled: true, Only: []string{"utility_dishwasher"}}
	if p.Wants(dish, at(12, 0)) {
		t.Fatal("Only filter ignored")
	}
	p.Quiet = &QuietHours{Start: "22:30", End: "07:00"}
	if p.Wants(door, at(23, 0)) || !p.Wants(door, at(7, 0)) {
		t.Fatal("doorbell should be silenced only inside quiet hours")
	}
	if !p.Wants(ups, at(3, 0)) {
		t.Fatal("power cut should ring through quiet hours by default")
	}
	p.Quiet.Ring = []Kind{}
	if p.Wants(ups, at(3, 0)) {
		t.Fatal("explicit empty Ring list should silence everything")
	}
}

// newBrowserSub makes a real P-256 keypair, as a browser would.
func newBrowserSub(t *testing.T, endpoint string) Subscription {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	enc := base64.RawURLEncoding.EncodeToString
	return Subscription{Endpoint: endpoint, P256dh: enc(k.PublicKey().Bytes()), Auth: enc(auth)}
}

func TestDispatchEndToEnd(t *testing.T) {
	type hit struct {
		path, ttl, urgency, topic, enc, auth string
		n                                    int
	}
	var mu sync.Mutex
	var hits []hit
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, r.Header.Get("TTL"), r.Header.Get("Urgency"), r.Header.Get("Topic"),
			r.Header.Get("Content-Encoding"), r.Header.Get("Authorization"), len(b)})
		mu.Unlock()
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	s, _ := OpenStore(filepath.Join(t.TempDir(), "s.json"))
	s.User("u1", "martin")
	s.User("u2", "guest")
	s.Upsert("u1", newBrowserSub(t, srv.URL+"/iphone"), now)
	s.Upsert("u1", newBrowserSub(t, srv.URL+"/gone"), now)
	s.Upsert("u2", newBrowserSub(t, srv.URL+"/guest-ipad"), now)
	guest := s.User("u2", "")
	guest.Prefs.Kinds[KindDoorbell] = KindPref{Enabled: false}
	s.SetPrefs("u2", guest.Prefs)

	priv, pub, _ := webpush.GenerateVAPIDKeys()
	p := &Pusher{Store: s, VAPIDPub: pub, VAPIDPriv: priv, Subscriber: "https://bells.example",
		HTTPClient: srv.Client(), Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}

	n := rules().Translate("house/events/derived", []byte(`{"type":"intercom_ringing","evidence":{"call_id":"c1","from_extension":"100"}}`), now)[0]
	ds := p.Dispatch(context.Background(), n, "")

	if len(ds) != 2 {
		t.Fatalf("deliveries %+v: want 2 (guest opted out of doorbell)", ds)
	}
	for _, h := range hits {
		if h.path == "/guest-ipad" {
			t.Fatal("guest received doorbell despite opting out")
		}
		if h.ttl != "60" || h.urgency != "high" || h.topic != "doorbell" || h.enc != "aes128gcm" || h.n == 0 {
			t.Errorf("headers %+v", h)
		}
		if len(h.auth) < 10 || h.auth[:6] != "vapid " {
			t.Errorf("auth header %q", h.auth)
		}
	}
	if u := s.User("u1", ""); len(u.Subs) != 1 || u.Subs[0].LastOK.IsZero() {
		t.Fatalf("410 sub should be pruned and live sub marked OK: %+v", u.Subs)
	}
}

func TestPayloadIsDeclarative(t *testing.T) {
	b, _ := payloadFor(Notification{Title: "Doorbell", Body: "Front door", URL: "https://bells.example/", Topic: "doorbell"})
	want := `{"web_push":8030,"notification":{"title":"Doorbell","body":"Front door","navigate":"https://bells.example/","tag":"doorbell","lang":"en-GB"}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}
