package main

// push.go: fan a notification out to every subscription whose owner wants it.
//
// The payload is a Declarative Web Push message (WebKit, iOS/iPadOS 18.4+).
// Safari displays it with no JavaScript at all, which removes the failure mode
// that made early iOS web push flaky — a service worker that was not woken, or
// woke and failed to call showNotification. Browsers that do not understand
// the declarative form (Chrome/Firefox/Android, older iOS) deliver it to the
// service worker's push handler, which reads the same JSON. One payload, both
// paths.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type declarative struct {
	WebPush      int              `json:"web_push"` // must be the integer 8030
	Notification declarativeNotif `json:"notification"`
}

type declarativeNotif struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Navigate string `json:"navigate"` // required: where a tap goes
	Tag      string `json:"tag,omitempty"`
	Lang     string `json:"lang,omitempty"`
}

func payloadFor(n Notification) ([]byte, error) {
	return json.Marshal(declarative{
		WebPush: 8030,
		Notification: declarativeNotif{
			Title: n.Title, Body: n.Body, Navigate: n.URL,
			Tag: n.Topic, Lang: "en-GB",
		},
	})
}

type Pusher struct {
	Store      *Store
	VAPIDPub   string
	VAPIDPriv  string
	Subscriber string // https URL or mailto: — Apple rejects anything else with BadJwtToken
	HTTPClient *http.Client
	Log        *slog.Logger
	Now        func() time.Time
}

type Delivery struct {
	User   string `json:"user"`
	Sub    string `json:"sub"`
	Status int    `json:"status"`
	Err    string `json:"err,omitempty"`
}

// Dispatch delivers n to everyone who wants it. only, when non-empty, restricts
// to one user (the "send me a test" button) and skips preference checks.
func (p *Pusher) Dispatch(ctx context.Context, n Notification, only string) []Delivery {
	now := p.Now()
	body, err := payloadFor(n)
	if err != nil {
		p.Log.Error("payload", "err", err)
		return nil
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []Delivery
	)
	for _, u := range p.Store.Users() {
		if only != "" {
			if u.ID != only {
				continue
			}
		} else if !u.Prefs.Wants(n, now) {
			continue
		}
		for _, s := range u.Subs {
			wg.Add(1)
			go func(u User, s *Subscription) {
				defer wg.Done()
				d := p.send(ctx, u, s, n, body)
				mu.Lock()
				out = append(out, d)
				mu.Unlock()
			}(u, s)
		}
	}
	wg.Wait()
	return out
}

func (p *Pusher) send(ctx context.Context, u User, s *Subscription, n Notification, body []byte) Delivery {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	d := Delivery{User: u.Username, Sub: s.Label}
	resp, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
		Endpoint: s.Endpoint,
		Keys:     webpush.Keys{P256dh: s.P256dh, Auth: s.Auth},
	}, &webpush.Options{
		HTTPClient:      p.HTTPClient,
		Subscriber:      p.Subscriber,
		VAPIDPublicKey:  p.VAPIDPub,
		VAPIDPrivateKey: p.VAPIDPriv,
		TTL:             int(n.TTL / time.Second),
		Urgency:         webpush.Urgency(n.Urgency),
		Topic:           n.Topic,
	})
	now := p.Now()
	if err != nil {
		d.Err = err.Error()
		p.Store.RecordResult(s.ID, false, d.Err, now)
		p.Log.Warn("push failed", "user", u.Username, "sub", s.Label, "err", err)
		return d
	}
	defer resp.Body.Close()
	d.Status = resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.Store.RecordResult(s.ID, true, "", now)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// RFC 8030 §7.3: the subscription is dead. Forget it; the app will
		// re-subscribe and re-post next time it is opened.
		p.Store.RemoveSub(u.ID, s.ID)
		d.Err = "subscription expired — removed"
		p.Log.Info("pruned subscription", "user", u.Username, "sub", s.Label, "status", resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		d.Err = fmt.Sprintf("%d %s", resp.StatusCode, b)
		p.Store.RecordResult(s.ID, false, d.Err, now)
		p.Log.Warn("push rejected", "user", u.Username, "sub", s.Label, "status", resp.StatusCode, "body", string(b))
	}
	return d
}
