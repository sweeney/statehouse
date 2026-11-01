package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sweeney/identity/common/auth"
)

//go:embed web
var webFS embed.FS

type ctxKey struct{}

type principal struct {
	UserID, Username string
	Service          string // client_id for service tokens
}

type API struct {
	Store       *Store
	Pusher      *Pusher
	Verifier    *auth.JWKSVerifier // nil = dev mode, auth off
	IdentityURL string
	ClientID    string // public PKCE client for the web app
	// NotifyClients may POST /api/notify with a service token.
	NotifyClients []string
	PublicURL     string
	Log           *slog.Logger
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/config", a.config)

	mux.Handle("GET /api/me", a.user(a.me))
	mux.Handle("PUT /api/me/prefs", a.user(a.putPrefs))
	mux.Handle("POST /api/me/subscriptions", a.user(a.postSub))
	mux.Handle("DELETE /api/me/subscriptions/{id}", a.user(a.deleteSub))
	mux.Handle("POST /api/me/test", a.user(a.test))

	mux.Handle("POST /api/notify", a.service(a.notify))

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The service worker must never be served stale, or a fix to it
		// never reaches an installed iPhone app.
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
	return mux
}

func (a *API) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"vapid_public_key": a.Pusher.VAPIDPub,
		"identity_url":     a.IdentityURL,
		"client_id":        a.ClientID,
		"auth":             a.Verifier != nil,
		"catalog":          Catalog,
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (a *API) user(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal{UserID: "dev", Username: "dev"}
		if a.Verifier != nil {
			c, err := a.Verifier.Parse(r.Context(), bearer(r))
			if err != nil || !c.IsActive {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				writeJSON(w, 401, map[string]string{"error": "invalid_token"})
				return
			}
			p = principal{UserID: c.UserID, Username: c.Username}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func (a *API) service(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal{Service: "dev"}
		if a.Verifier != nil {
			c, err := a.Verifier.ParseServiceToken(r.Context(), bearer(r))
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				writeJSON(w, 401, map[string]string{"error": "invalid_token"})
				return
			}
			if !contains(a.NotifyClients, c.ClientID) {
				writeJSON(w, 403, map[string]string{"error": "insufficient_scope"})
				return
			}
			p = principal{Service: c.ClientID}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func who(r *http.Request) principal { return r.Context().Value(ctxKey{}).(principal) }

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	writeJSON(w, 200, a.Store.User(p.UserID, p.Username))
}

func (a *API) putPrefs(w http.ResponseWriter, r *http.Request) {
	var prefs Prefs
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&prefs); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	for k := range prefs.Kinds {
		if _, ok := catalogEntry(k); !ok {
			writeJSON(w, 400, map[string]string{"error": "unknown kind " + string(k)})
			return
		}
	}
	if err := a.Store.SetPrefs(who(r).UserID, prefs); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(204)
}

// postSub takes PushSubscription.toJSON() plus an optional label. The app calls
// this on every open, which doubles as the liveness heartbeat.
func (a *API) postSub(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil ||
		!strings.HasPrefix(in.Endpoint, "https://") || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeJSON(w, 400, map[string]string{"error": "expected PushSubscription JSON"})
		return
	}
	p := who(r)
	a.Store.User(p.UserID, p.Username) // ensure the record exists
	sub, err := a.Store.Upsert(p.UserID, Subscription{
		Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, Label: in.Label,
	}, time.Now())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, sub)
}

func (a *API) deleteSub(w http.ResponseWriter, r *http.Request) {
	if !a.Store.RemoveSub(who(r).UserID, r.PathValue("id")) {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	w.WriteHeader(204)
}

func (a *API) test(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	n := Notification{
		Kind: KindExternal, Title: "Test notification",
		Body: "If you can read this on your lock screen, push works.",
		URL:  a.abs("/"), Topic: "test", Urgency: UrgencyHigh, TTL: 5 * time.Minute,
	}
	writeJSON(w, 200, a.Pusher.Dispatch(r.Context(), n, p.UserID))
}

// notify lets a sibling service (binday: "bins out tonight") send through the
// same preferences, under the "external" kind, narrowable by subject.
func (a *API) notify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		URL     string `json:"url"`
		Subject string `json:"subject"`
		Urgent  bool   `json:"urgent"`
		TTLSecs int    `json:"ttl_seconds"`
		Dedupe  string `json:"dedupe_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil || in.Title == "" {
		writeJSON(w, 400, map[string]string{"error": "title required"})
		return
	}
	src := who(r).Service
	n := Notification{
		Kind: KindExternal, Subject: src, Title: in.Title, Body: in.Body, URL: in.URL,
		Topic: topicKey("ext", src), Urgency: UrgencyNormal, TTL: time.Duration(in.TTLSecs) * time.Second,
	}
	if in.Urgent {
		n.Urgency = UrgencyHigh
	}
	if n.TTL == 0 {
		n.TTL = 12 * time.Hour
	}
	n.URL = a.abs(n.URL)
	if in.Dedupe != "" {
		if !a.Store.FirstSighting("ext:"+src+":"+in.Dedupe, time.Now()) {
			writeJSON(w, 200, map[string]any{"deduped": true})
			return
		}
	}
	writeJSON(w, 200, a.Pusher.Dispatch(r.Context(), n, ""))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// abs makes a path absolute against the public URL: a declarative push's
// navigate must be a full URL on the web app's origin.
func (a *API) abs(u string) string {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	if u == "" {
		u = "/"
	}
	return strings.TrimRight(a.PublicURL, "/") + u
}
