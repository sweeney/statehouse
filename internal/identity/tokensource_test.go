package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *TokenSource) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ts := &TokenSource{
		BaseURL:      srv.URL,
		ClientID:     "statehouse",
		ClientSecret: "secret",
		HTTPClient:   srv.Client(),
	}
	return srv, ts
}

func TestTokenSource_FetchesToken(t *testing.T) {
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/token" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "client_credentials" ||
			r.FormValue("client_id") != "statehouse" ||
			r.FormValue("client_secret") != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "test-token",
			ExpiresIn:   900,
			TokenType:   "Bearer",
		})
	})

	tok, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "test-token" {
		t.Fatalf("got %q, want %q", tok, "test-token")
	}
}

func TestTokenSource_CachesToken(t *testing.T) {
	var calls atomic.Int32
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "cached-token",
			ExpiresIn:   900,
			TokenType:   "Bearer",
		})
	})

	for i := 0; i < 5; i++ {
		if _, err := ts.Token(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 fetch, got %d", n)
	}
}

func TestTokenSource_RefreshesNearExpiry(t *testing.T) {
	var calls atomic.Int32
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "token",
			ExpiresIn:   900,
			TokenType:   "Bearer",
		})
	})

	// Prime the cache then wind the expiry back into the buffer window.
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	ts.expiresAt = time.Now().Add(expiryBuffer - time.Second)
	ts.mu.Unlock()

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 fetches (initial + refresh), got %d", n)
	}
}

func TestTokenSource_ErrorPropagated(t *testing.T) {
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(oauthError{
			Error:            "invalid_client",
			ErrorDescription: "bad credentials",
		})
	})

	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "invalid_client"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestTokenSource_ConcurrentCallsSucceed(t *testing.T) {
	// With the unlock-before-fetch design, multiple goroutines may race to
	// fetch concurrently when the cache is empty. All calls must succeed.
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "token",
			ExpiresIn:   900,
			TokenType:   "Bearer",
		})
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ts.Token(context.Background()); err != nil {
				t.Errorf("concurrent call failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestTokenSource_NetworkError(t *testing.T) {
	ts := &TokenSource{
		BaseURL:      "http://127.0.0.1:1", // nothing listening
		ClientID:     "statehouse",
		ClientSecret: "secret",
	}
	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
	if !containsStr(err.Error(), "identity: token request") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenSource_NonJSONErrorBody(t *testing.T) {
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	})
	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !containsStr(err.Error(), "unexpected status 503") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenSource_EmptyAccessToken(t *testing.T) {
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "",
			ExpiresIn:   900,
			TokenType:   "Bearer",
		})
	})
	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected error for empty access_token, got nil")
	}
	if !containsStr(err.Error(), "empty access_token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenSource_ZeroExpiresInIsError(t *testing.T) {
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "token",
			ExpiresIn:   0,
			TokenType:   "Bearer",
		})
	})

	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected error for expires_in=0, got nil")
	}
	if !containsStr(err.Error(), "invalid expires_in") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenSource_Invalidate(t *testing.T) {
	var calls atomic.Int32
	_, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "token",
			ExpiresIn:   900,
			TokenType:   "Bearer",
		})
	})

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts.Invalidate()
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 fetches after Invalidate, got %d", n)
	}
}

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}
