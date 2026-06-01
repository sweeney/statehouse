package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── JWT test helpers ──────────────────────────────────────────────────────────

func genTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// encCoord encodes a P-256 coordinate as a 32-byte base64url string.
func encCoord(n *big.Int) string {
	b := make([]byte, 32)
	nb := n.Bytes()
	copy(b[32-len(nb):], nb)
	return base64.RawURLEncoding.EncodeToString(b)
}

func fakeJWKSServer(t *testing.T, pub *ecdsa.PublicKey, kid string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"keys": []map[string]any{{
				"kty": "EC", "use": "sig", "alg": "ES256",
				"kid": kid, "crv": "P-256",
				"x": encCoord(pub.X),
				"y": encCoord(pub.Y),
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signJWT(t *testing.T, priv *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	b64j := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := b64j(map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid})
	payload := b64j(claims)
	msg := header + "." + payload
	h := sha256.Sum256([]byte(msg))
	r, s, err := ecdsa.Sign(rand.Reader, priv, h[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	rb, sb := r.Bytes(), s.Bytes()
	copy(sig[32-len(rb):32], rb)
	copy(sig[64-len(sb):64], sb)
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// validClaims returns a minimal set of claims accepted by JWKSVerifier.
// Note: the common auth library uses "act" for the is_active flag.
func validClaims(issuer string) map[string]any {
	return map[string]any{
		"iss": issuer,
		"sub": "user-abc",
		"exp": time.Now().Add(15 * time.Minute).Unix(),
		"act": true,
	}
}

// authSetup builds a Server with IdentityURL pointing at a fake JWKS server.
func authSetup(t *testing.T) (srv *Server, priv *ecdsa.PrivateKey, kid string) {
	t.Helper()
	priv = genTestKey(t)
	kid = "testkey"
	fakeID := fakeJWKSServer(t, &priv.PublicKey, kid)
	srv, _ = setup(t)
	srv.IdentityURL = fakeID.URL
	return srv, priv, kid
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestAuth_NoToken_Returns401(t *testing.T) {
	srv, _, _ := authSetup(t)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/state", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuth_ValidToken_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signJWT(t, priv, kid, validClaims(srv.IdentityURL))
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestAuth_ExpiredToken_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validClaims(srv.IdentityURL)
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	token := signJWT(t, priv, kid, claims)
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuth_WrongIssuer_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validClaims("https://evil.example.com")
	token := signJWT(t, priv, kid, claims)
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuth_BadSignature_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	other := genTestKey(t)
	_ = priv
	token := signJWT(t, other, kid, validClaims(srv.IdentityURL))
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuth_InactiveUser_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validClaims(srv.IdentityURL)
	claims["act"] = false
	token := signJWT(t, priv, kid, claims)
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuth_HealthzSkipsAuth(t *testing.T) {
	srv, _, _ := authSetup(t)
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestAuth_AllProtectedRoutesRequireAuth(t *testing.T) {
	srv, _, _ := authSetup(t)
	mux := newMux(srv)
	routes := []string{
		"/state",
		"/state/house",
		"/state/devices",
		"/state/devices/foo",
		"/state/activity",
		"/events/recent",
		"/metrics",
		"/config/devices",
		"/config/devices/foo",
	}
	for _, route := range routes {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("route %s: want 401, got %d", route, w.Code)
		}
	}
}
