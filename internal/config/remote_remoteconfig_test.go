package config

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rotatingTokenSource issues "stale-token" until invalidated, then "test-token".
type rotatingTokenSource struct{ invalidated int }

func (r *rotatingTokenSource) Token(context.Context) (string, error) {
	if r.invalidated == 0 {
		return "stale-token", nil
	}
	return "test-token", nil
}
func (r *rotatingTokenSource) Invalidate() { r.invalidated++ }

// countingErrTokenSource fails every Token call and counts them.
type countingErrTokenSource struct{ calls atomic.Int32 }

func (c *countingErrTokenSource) Token(context.Context) (string, error) {
	c.calls.Add(1)
	return "", errors.New("identity: invalid_client")
}
func (c *countingErrTokenSource) Invalidate() {}

// hangingTokenSource blocks until the context ends, as a hung identity does.
type hangingTokenSource struct{ calls atomic.Int32 }

func (h *hangingTokenSource) Token(ctx context.Context) (string, error) {
	h.calls.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}
func (h *hangingTokenSource) Invalidate() {}

var allNamespaces = []string{"statehouse_classes", "devices_test", "statehouse_behaviour"}

// A token that expired between being minted and being used is retried at once
// with a fresh one. Before remoteconfig the 401 only invalidated it, and the
// service ran on local config alone until the next SIGHUP.
func TestFetcher_401RetriesWithFreshToken(t *testing.T) {
	mux := http.NewServeMux()
	serveNamespace(mux, "devices_test", map[string]any{
		"lamp": map[string]any{"class": "binary_state_device"},
	})
	f := newTestFetcher(t, mux)
	f.Tokens = &rotatingTokenSource{}
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f.ApplyRemote(context.Background(), &cfg)

	if _, ok := cfg.Devices["lamp"]; !ok {
		t.Error("the retry with a fresh token should have applied the remote device")
	}
	if !f.Statuses()["devices_test"].OK {
		t.Error("status should be OK after a successful retry")
	}
}

// Exactly one retry: a persistent 401 is neither given up on at once nor retried
// until the deadline.
func TestFetcher_Persistent401IsRetriedOnce(t *testing.T) {
	var requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/devices_test", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	f := newTestFetcher(t, mux)
	f.Tokens = &trackingTokenSource{token: "stale-token"}
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f.ApplyRemote(context.Background(), &cfg)

	if n := requests.Load(); n != 2 {
		t.Errorf("a persistent 401 got %d requests, want 2 (the first and one retry)", n)
	}
}

// statehouse used to record no status at all when the token could not be had, so
// /healthz showed the previous fetch's ok:true while running on local config. Now
// the cause is recorded against every namespace, after asking identity once.
func TestFetcher_TokenFailureRecordedForEveryNamespace(t *testing.T) {
	src := &countingErrTokenSource{}
	f := &Fetcher{BaseURL: "https://config.test", Tokens: src}
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f.ApplyRemote(context.Background(), &cfg)

	if n := src.calls.Load(); n != 1 {
		t.Errorf("Token called %d times in one ApplyRemote, want 1", n)
	}
	st := f.Statuses()
	for _, ns := range allNamespaces {
		if s := st[ns]; s.OK || !strings.Contains(s.Error, "invalid_client") {
			t.Errorf("status[%s] = %+v, want the token error", ns, s)
		}
	}
}

// A hung identity uses up the deadline once, and every namespace names the token
// as the cause.
func TestFetcher_HungIdentityRecordsTheSameCauseForEveryNamespace(t *testing.T) {
	src := &hangingTokenSource{}
	f := &Fetcher{BaseURL: "https://config.test", Tokens: src}
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	f.ApplyRemote(ctx, &cfg)

	if n := src.calls.Load(); n != 1 {
		t.Errorf("Token called %d times in one ApplyRemote, want 1", n)
	}
	st := f.Statuses()
	want := st["statehouse_classes"].Error
	if want == "" {
		t.Fatalf("classes status = %+v, want a recorded failure", st["statehouse_classes"])
	}
	for _, ns := range allNamespaces {
		if s := st[ns]; s.OK || s.Error != want {
			t.Errorf("status[%s] = %+v, want the same token failure (%q)", ns, s, want)
		}
	}
}

// The error on /healthz names the namespace; statehouse's used not to.
func TestFetcher_ErrorNamesTheNamespace(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/statehouse_classes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	f := newTestFetcher(t, mux)
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f.ApplyRemote(context.Background(), &cfg)

	if s := f.Statuses()["statehouse_classes"]; !strings.Contains(s.Error, "statehouse_classes") {
		t.Errorf("status error = %q, want it to name the namespace", s.Error)
	}
}

// An empty base URL is explicit local mode: nothing requested, no token asked
// for, no status recorded.
func TestFetcher_EmptyBaseURLIsLocalMode(t *testing.T) {
	src := &countingErrTokenSource{}
	f := &Fetcher{Tokens: src}
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f.ApplyRemote(context.Background(), &cfg)

	if n := src.calls.Load(); n != 0 {
		t.Errorf("Token called %d times with no base URL, want 0", n)
	}
	if s := f.Statuses(); len(s) != 0 {
		t.Errorf("statuses = %v, want none", s)
	}
}

// The User-Agent names statehouse, so config's logs could say who is reading.
func TestFetcher_SendsUserAgent(t *testing.T) {
	var got atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/statehouse_classes", func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("User-Agent"))
		w.Write([]byte(`{}`)) //nolint:errcheck
	})
	f := newTestFetcher(t, mux)
	f.UserAgent = "statehouse/abc123"
	cfg := Default()
	cfg.Site.DevicesNamespace = "devices_test"
	f.ApplyRemote(context.Background(), &cfg)

	if ua, _ := got.Load().(string); ua != "statehouse/abc123" {
		t.Errorf("User-Agent = %q", ua)
	}
}
