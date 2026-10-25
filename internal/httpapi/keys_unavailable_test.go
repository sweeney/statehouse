package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// When the identity service cannot be reached, a token cannot be checked at all.
// That is an outage, not a verdict on the token: answering 401 tells the client
// to sign its user out, so an identity outage would sign every user out of
// every service at once, over tokens nobody examined. It must be a 503, and a
// genuinely bad token must still be a 401.

// unavailableAuthSetup builds a Server whose identity service answers every
// JWKS request with a 500, so the verifier never holds a key.
func unavailableAuthSetup(t *testing.T) *Server {
	t.Helper()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "identity is down", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)
	srv, _ := setup(t)
	srv.IdentityURL = down.URL
	return srv
}

func TestAuth_KeysUnavailable_UserToken_Returns503(t *testing.T) {
	srv := unavailableAuthSetup(t)
	priv := genTestKey(t)
	w := getWith(t, srv, "/state", signJWT(t, priv, "testkey", validClaims(srv.IdentityURL)))
	assertKeysUnavailable(t, w)
}

func TestAuth_KeysUnavailable_ServiceToken_Returns503(t *testing.T) {
	srv := unavailableAuthSetup(t)
	priv := genTestKey(t)
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, "testkey", validServiceClaims(srv.IdentityURL)))
	assertKeysUnavailable(t, w)
}

// The outage is counted on its own, not folded into rejected_invalid_token_total,
// which would make an identity outage graph as a flood of bad tokens.
func TestAuth_KeysUnavailable_CountedSeparately(t *testing.T) {
	srv := unavailableAuthSetup(t)
	priv := genTestKey(t)
	getWith(t, srv, "/state", signJWT(t, priv, "testkey", validClaims(srv.IdentityURL)))

	m := srv.authMetricsSnapshot()
	if m.KeysUnavailable != 1 {
		t.Errorf("keys_unavailable_total = %d, want 1", m.KeysUnavailable)
	}
	if m.RejectedInvalidToken != 0 {
		t.Errorf("rejected_invalid_token_total = %d, want 0: an outage is not a bad token", m.RejectedInvalidToken)
	}
}

func assertKeysUnavailable(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (body %q)", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("503 carries no Retry-After")
	}
	// No challenge: the client's credentials are not in question, and a
	// WWW-Authenticate header invites it to go and get new ones.
	if h := w.Header().Get("WWW-Authenticate"); h != "" {
		t.Errorf("503 carries a WWW-Authenticate challenge: %q", h)
	}
}

// Every authenticated path can now answer 503, and a client generated off the
// spec should know to retry it rather than treat it as fatal.
func TestOpenAPISpec_ProtectedPathsDocument503(t *testing.T) {
	doc := specDoc(t)
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Fatal("spec has no paths")
	}
	public := map[string]bool{"/healthz": true, "/openapi.json": true}
	for name, raw := range paths {
		if public[name] {
			continue
		}
		item, _ := raw.(map[string]any)
		get, _ := item["get"].(map[string]any)
		responses, _ := get["responses"].(map[string]any)
		if _, ok := responses["503"]; !ok {
			t.Errorf("path %s: no 503 response documented", name)
		}
	}
}
