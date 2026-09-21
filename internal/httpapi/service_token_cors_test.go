package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Service tokens and CORS landed as independent changes, so nothing exercised
// them together. These go through handler() — the real chain, CORS wrapper
// above auth above the mux — rather than newMux(), because the seam between
// the two is the only thing they are about.

// authedCORSStack builds the full handler chain against a fake JWKS server,
// returning it with a key to sign test tokens.
func authedCORSStack(t *testing.T, policy ServiceTokenPolicy, origins ...string) (http.Handler, *Server, func(map[string]any) string) {
	t.Helper()
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens = policy
	srv.AllowedOrigins = origins
	h, err := srv.handler()
	if err != nil {
		t.Fatalf("handler(): %v", err)
	}
	sign := func(claims map[string]any) string { return signServiceJWT(t, priv, kid, claims) }
	return h, srv, sign
}

// A service token has to work through the real chain, not just the mux. The
// CORS wrapper runs first and short-circuits OPTIONS; nothing about that may
// swallow a GET that carries a valid service token.
func TestServiceToken_PassesThroughTheCORSWrapper(t *testing.T) {
	h, srv, sign := authedCORSStack(t, DefaultServiceTokenPolicy(), testOrigins()...)

	r := httptest.NewRequest(http.MethodGet, "/config/devices", nil)
	r.Header.Set("Authorization", "Bearer "+sign(validServiceClaims(srv.IdentityURL)))
	r.Header.Set("Origin", allowlisted)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
	if got := srv.authMetricsSnapshot().ServiceTokensAccepted; got != 1 {
		t.Errorf("service_tokens_accepted_total = %d, want 1", got)
	}
}

// The 403 is new here, and the reasoning that put CORS headers on the 401
// applies to it unchanged: without Access-Control-Allow-Origin the browser
// refuses to expose the response, so a page cannot tell "this token lacks the
// scope" from "the service is down" — and insufficient_scope is precisely the
// one a client must not retry.
func TestForbiddenResponseStillCarriesCORSHeaders(t *testing.T) {
	h, srv, sign := authedCORSStack(t,
		ServiceTokenPolicy{Enabled: true, RequiredScope: "statehouse:read"},
		testOrigins()...)

	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+sign(validServiceClaims(srv.IdentityURL)))
	r.Header.Set("Origin", allowlisted)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
	assertVaryOrigin(t, w)
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("the 403 should still carry a challenge")
	}
}

// Exposing WWW-Authenticate is what makes the RFC 6750 error codes readable
// from browser JS at all — it is not on the CORS-safelist. The 401 case is
// covered in cors_test.go; this pins the 403, where the code is the whole
// difference between "get a new token" and "stop".
func TestForbiddenResponseExposesTheChallengeHeader(t *testing.T) {
	h, srv, sign := authedCORSStack(t,
		ServiceTokenPolicy{Enabled: true, AllowedClients: []string{"greenhouse"}},
		testOrigins()...)

	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+sign(validServiceClaims(srv.IdentityURL)))
	r.Header.Set("Origin", allowlisted)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	assertHeader(t, w, "Access-Control-Expose-Headers", exposedHeaders)
}
