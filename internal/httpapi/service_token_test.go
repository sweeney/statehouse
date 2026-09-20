package httpapi

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweeney/identity/common/auth"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// validServiceClaims returns the claim set identity stamps on a
// client_credentials token: a `client_id`, an expiry, and no `act` flag or
// role, because a service principal has neither.
func validServiceClaims(issuer string) map[string]any {
	return map[string]any{
		"iss":       issuer,
		"sub":       "svc-countinghouse",
		"client_id": "countinghouse",
		"iat":       time.Now().Add(-time.Minute).Unix(),
		"exp":       time.Now().Add(15 * time.Minute).Unix(),
		"jti":       "tok-0001",
	}
}

// signServiceJWT signs claims as a service token — the JOSE `typ` of "at+jwt"
// is the discriminator identity/common keys both its parsers off.
func signServiceJWT(t *testing.T, priv *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	return signTypedJWT(t, priv, kid, serviceTokenTyp, claims)
}

// getWith issues GET path with the given bearer token and returns the recorder.
func getWith(t *testing.T, srv *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	return w
}

// challengeParam pulls one auth-param out of a WWW-Authenticate header.
// It is deliberately a dumb substring scan: the test should fail if the
// header is not the simple quoted-string form RFC 6750 specifies.
func challengeParam(t *testing.T, w *httptest.ResponseRecorder, name string) string {
	t.Helper()
	hdr := w.Header().Get("WWW-Authenticate")
	marker := name + `="`
	i := strings.Index(hdr, marker)
	if i < 0 {
		return ""
	}
	rest := hdr[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated %s in WWW-Authenticate %q", name, hdr)
	}
	return rest[:j]
}

// ── The bug in the issue: a service token cannot reach the API at all ─────────

func TestServiceToken_ValidToken_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))
	w := getWith(t, srv, "/state", token)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Header().Get("WWW-Authenticate"))
	}
}

func TestServiceToken_ReachesEveryProtectedRoute(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))
	mux := newMux(srv)
	// The {id} routes 404 on an unknown device; what matters is that the
	// request was authorised rather than turned away at the door.
	for _, route := range []string{
		"/state", "/state/house", "/state/devices", "/state/devices/foo",
		"/state/activity", "/events/recent", "/metrics",
		"/config/devices", "/config/devices/foo",
	} {
		r := httptest.NewRequest(http.MethodGet, route, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Errorf("route %s: service token turned away with %d", route, w.Code)
		}
	}
}

// ── Service token validity ────────────────────────────────────────────────────

func TestServiceToken_Expired_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validServiceClaims(srv.IdentityURL)
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if got := challengeParam(t, w, "error"); got != "invalid_token" {
		t.Errorf("want error=invalid_token, got %q", got)
	}
	if desc := challengeParam(t, w, "error_description"); !strings.Contains(desc, "expired") {
		t.Errorf("want a description naming expiry, got %q", desc)
	}
}

func TestServiceToken_BadSignature_Returns401(t *testing.T) {
	srv, _, kid := authSetup(t)
	other := genTestKey(t)
	w := getWith(t, srv, "/state", signServiceJWT(t, other, kid, validServiceClaims(srv.IdentityURL)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestServiceToken_WrongIssuer_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims("https://evil.example.com")))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// A service token with no client_id names no principal, so there is nothing to
// log, allowlist or revoke. identity/common refuses it and so must we.
func TestServiceToken_MissingClientID_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validServiceClaims(srv.IdentityURL)
	delete(claims, "client_id")
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestServiceToken_NoExpiry_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validServiceClaims(srv.IdentityURL)
	delete(claims, "exp")
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// ── Routing on the (unverified) typ header ────────────────────────────────────

// The `typ` header picks which parser runs, and it is read before any signature
// check. That is safe only because typ is inside the signed JOSE header: a
// forged one routes to a parser that then rejects the token. This test pins
// that, by lifting the signature off a genuine user token onto a header
// claiming to be a service token.
func TestServiceToken_ForgedTypHeader_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	user := signJWT(t, priv, kid, validClaims(srv.IdentityURL))
	parts := strings.Split(user, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}
	forgedHdr, _ := json.Marshal(map[string]any{"alg": "ES256", "typ": serviceTokenTyp, "kid": kid})
	tampered := base64.RawURLEncoding.EncodeToString(forgedHdr) + "." + parts[1] + "." + parts[2]

	w := getWith(t, srv, "/state", tampered)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for a token whose typ was swapped after signing, got %d", w.Code)
	}
}

// A token with no typ header at all is not a service token, so it takes the
// user path — where it fails the is-active check because a service principal
// has no `act` claim. Pinned so the routing rule stays "typ says at+jwt",
// not "it smells service-ish".
func TestServiceToken_NoTypHeader_TakesUserPath(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signTypedJWT(t, priv, kid, "", validServiceClaims(srv.IdentityURL))
	w := getWith(t, srv, "/state", token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestIsServiceToken(t *testing.T) {
	hdr := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return base64.RawURLEncoding.EncodeToString(b) + ".payload.sig"
	}
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"service typ", hdr(map[string]any{"alg": "ES256", "typ": "at+jwt"}), true},
		{"user typ", hdr(map[string]any{"alg": "ES256", "typ": "JWT"}), false},
		{"no typ", hdr(map[string]any{"alg": "ES256"}), false},
		// identity/common compares typ exactly, so we route on an exact match
		// too — anything else would send a token down a path its own parser
		// would then refuse.
		{"uppercase typ", hdr(map[string]any{"typ": "AT+JWT"}), false},
		{"no dot", "notajwt", false},
		{"header not base64", "!!!.payload.sig", false},
		{"header not json", base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".p.s", false},
		{"empty", "", false},
		{"typ not a string", hdr(map[string]any{"typ": 7}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isServiceToken(c.token); got != c.want {
				t.Errorf("isServiceToken(%q) = %v, want %v", c.token, got, c.want)
			}
		})
	}
}

// Padded base64 is not what identity emits, but a header that decodes either
// way costs nothing and avoids routing a legitimate token to the wrong parser.
func TestIsServiceToken_TolerateseBase64Padding(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"alg": "ES256", "typ": serviceTokenTyp})
	padded := base64.URLEncoding.EncodeToString(b)
	if !strings.HasSuffix(padded, "=") {
		t.Skip("header happened to encode without padding")
	}
	if !isServiceToken(padded + ".payload.sig") {
		t.Errorf("padded header should still be recognised as a service token")
	}
}

// ── Policy: service tokens can be switched off ────────────────────────────────

func TestServiceToken_Disabled_Returns401WithReason(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.Enabled = false
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	// The whole complaint in the issue was a 401 that never said the token
	// type was the problem.
	if desc := challengeParam(t, w, "error_description"); !strings.Contains(desc, "service token") {
		t.Errorf("want a description naming service tokens, got %q", desc)
	}
}

func TestServiceToken_Disabled_LeavesUserTokensAlone(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.Enabled = false
	w := getWith(t, srv, "/state", signJWT(t, priv, kid, validClaims(srv.IdentityURL)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// ── Policy: audience ──────────────────────────────────────────────────────────

func TestServiceToken_RequiredAudience_Match_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	claims := validServiceClaims(srv.IdentityURL)
	claims["aud"] = "https://statehouse.swee.net"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestServiceToken_RequiredAudience_MatchesOneOfMany_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	claims := validServiceClaims(srv.IdentityURL)
	claims["aud"] = []string{"https://greenhouse.swee.net", "https://statehouse.swee.net"}
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestServiceToken_RequiredAudience_Mismatch_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	claims := validServiceClaims(srv.IdentityURL)
	claims["aud"] = "https://greenhouse.swee.net"
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims))
	// A token minted for a sibling service is not a token for us: 401, not
	// 403 — the credential is wrong, not the privilege level.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if got := challengeParam(t, w, "error"); got != "invalid_token" {
		t.Errorf("want error=invalid_token, got %q", got)
	}
	if desc := challengeParam(t, w, "error_description"); !strings.Contains(desc, "audience") {
		t.Errorf("want a description naming the audience, got %q", desc)
	}
}

func TestServiceToken_RequiredAudience_AbsentClaim_Returns401(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for a token with no aud at all, got %d", w.Code)
	}
}

func TestServiceToken_AudienceUnenforcedByDefault(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validServiceClaims(srv.IdentityURL)
	claims["aud"] = "https://something.else"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusOK {
		t.Fatalf("want 200 when no audience is configured, got %d", w.Code)
	}
}

// An audience is matched whole. A token for "https://statehouse.swee.net.evil"
// must not satisfy a requirement of "https://statehouse.swee.net".
func TestServiceToken_AudiencePrefixIsNotAMatch(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	claims := validServiceClaims(srv.IdentityURL)
	claims["aud"] = "https://statehouse.swee.net.evil.example"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// The verifier's own RequiredAudience would apply to user tokens too, which is
// why the service audience is enforced here instead. If that ever changes, the
// UI's user tokens — which carry no aud — start 401ing, so pin it.
func TestUserToken_UnaffectedByServiceAudienceRequirement(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	if w := getWith(t, srv, "/state", signJWT(t, priv, kid, validClaims(srv.IdentityURL))); w.Code != http.StatusOK {
		t.Fatalf("want 200 for a user token with no aud, got %d", w.Code)
	}
}

// ── Policy: scope ─────────────────────────────────────────────────────────────

func TestServiceToken_RequiredScope_Present_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	claims := validServiceClaims(srv.IdentityURL)
	claims["scope"] = "statehouse:read"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestServiceToken_RequiredScope_AmongOthers_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	claims := validServiceClaims(srv.IdentityURL)
	claims["scope"] = "config:read statehouse:read greenhouse:read"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestServiceToken_RequiredScope_Missing_Returns403(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	claims := validServiceClaims(srv.IdentityURL)
	claims["scope"] = "greenhouse:read"
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims))
	// Authenticated but under-privileged is 403 with insufficient_scope
	// (RFC 6750 §3.1), not 401 — retrying with the same token is pointless,
	// and the client needs to know that.
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	if got := challengeParam(t, w, "error"); got != "insufficient_scope" {
		t.Errorf("want error=insufficient_scope, got %q", got)
	}
	if got := challengeParam(t, w, "scope"); got != "statehouse:read" {
		t.Errorf("want the required scope advertised, got %q", got)
	}
}

func TestServiceToken_RequiredScope_NoScopeClaim_Returns403(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

// Scopes are space-delimited whole tokens: "statehouse:readonly" does not
// contain "statehouse:read".
func TestServiceToken_RequiredScope_PrefixIsNotAMatch(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	claims := validServiceClaims(srv.IdentityURL)
	claims["scope"] = "statehouse:readonly"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestUserToken_UnaffectedByServiceScopeRequirement(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	if w := getWith(t, srv, "/state", signJWT(t, priv, kid, validClaims(srv.IdentityURL))); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// ── Policy: client allowlist ──────────────────────────────────────────────────

func TestServiceToken_AllowedClients_Listed_Passes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.AllowedClients = []string{"greenhouse", "countinghouse"}
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestServiceToken_AllowedClients_NotListed_Returns403(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.AllowedClients = []string{"greenhouse"}
	w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	if desc := challengeParam(t, w, "error_description"); !strings.Contains(desc, "client") {
		t.Errorf("want a description naming the client, got %q", desc)
	}
}

func TestServiceToken_AllowedClients_Empty_AllowsAnyClient(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.AllowedClients = nil
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// The allowlist is matched exactly — no case folding, no prefixes. A client
// registered as "countinghouse-staging" must not inherit "countinghouse".
func TestServiceToken_AllowedClients_MatchIsExact(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.AllowedClients = []string{"countinghouse"}
	for _, id := range []string{"Countinghouse", "countinghouse-staging", " countinghouse"} {
		claims := validServiceClaims(srv.IdentityURL)
		claims["client_id"] = id
		if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusForbidden {
			t.Errorf("client_id %q: want 403, got %d", id, w.Code)
		}
	}
}

// The order matters: a token that is both for the wrong audience and from an
// unlisted client is a bad credential first. Reporting 403 would tell an
// attacker holding someone else's token that the token itself was fine.
func TestServiceToken_AudienceCheckedBeforeClientAllowlist(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredAudience = "https://statehouse.swee.net"
	srv.ServiceTokens.AllowedClients = []string{"greenhouse"}
	claims := validServiceClaims(srv.IdentityURL)
	claims["aud"] = "https://elsewhere.example"
	if w := getWith(t, srv, "/state", signServiceJWT(t, priv, kid, claims)); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// ── Challenge shape (RFC 6750) ────────────────────────────────────────────────

// RFC 6750 §3.1: a request with no credentials at all gets a bare challenge.
// An error code there would describe a token the client never sent.
func TestChallenge_MissingCredentials_OmitsErrorCode(t *testing.T) {
	srv, _, _ := authSetup(t)
	w := getWith(t, srv, "/state", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	hdr := w.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(hdr, "Bearer realm=") {
		t.Errorf("want a Bearer challenge, got %q", hdr)
	}
	if strings.Contains(hdr, "error=") {
		t.Errorf("want no error code when no credentials were sent, got %q", hdr)
	}
}

func TestChallenge_InvalidToken_CarriesErrorCode(t *testing.T) {
	srv, _, _ := authSetup(t)
	w := getWith(t, srv, "/state", "not-a-jwt")
	if got := challengeParam(t, w, "error"); got != "invalid_token" {
		t.Errorf("want error=invalid_token, got %q", got)
	}
}

func TestChallenge_InactiveUser_CarriesErrorCode(t *testing.T) {
	srv, priv, kid := authSetup(t)
	claims := validClaims(srv.IdentityURL)
	claims["act"] = false
	w := getWith(t, srv, "/state", signJWT(t, priv, kid, claims))
	if got := challengeParam(t, w, "error"); got != "invalid_token" {
		t.Errorf("want error=invalid_token, got %q", got)
	}
	if desc := challengeParam(t, w, "error_description"); !strings.Contains(desc, "active") {
		t.Errorf("want a description naming the active check, got %q", desc)
	}
}

func TestChallenge_NonBearerScheme_Returns401(t *testing.T) {
	srv, _, _ := authSetup(t)
	for _, hdr := range []string{"Basic dXNlcjpwdw==", "Bearer", "Bearer ", "", "Token abc"} {
		r := httptest.NewRequest(http.MethodGet, "/state", nil)
		if hdr != "" {
			r.Header.Set("Authorization", hdr)
		}
		w := httptest.NewRecorder()
		newMux(srv).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: want 401, got %d", hdr, w.Code)
		}
	}
}

// RFC 7235 §2.1 makes the auth scheme case-insensitive. Rejecting "bearer"
// fails a correct client for no reason.
func TestBearerScheme_IsCaseInsensitive(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		r := httptest.NewRequest(http.MethodGet, "/state", nil)
		r.Header.Set("Authorization", scheme+" "+token)
		w := httptest.NewRecorder()
		newMux(srv).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("scheme %q: want 200, got %d", scheme, w.Code)
		}
	}
}

func TestBearerScheme_ToleratesExtraWhitespace(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer   "+token+" ")
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

// ── Header injection ──────────────────────────────────────────────────────────

// realm is operator-supplied (it is the configured issuer). A quote or newline
// in it must not be able to forge extra auth-params or split the response.
func TestChallenge_RealmIsQuoteSafe(t *testing.T) {
	a := &authenticator{
		parser:  stubParser{},
		realm:   "https://id\"evil\r\nX-Injected: yes",
		service: ServiceTokenPolicy{Enabled: true},
		metrics: &authMetrics{},
	}
	w := httptest.NewRecorder()
	requireAuth(a, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/state", nil))

	hdr := w.Header().Get("WWW-Authenticate")
	if strings.Count(hdr, `"`) != 2 {
		t.Errorf("realm should contribute exactly one quoted value, got %q", hdr)
	}
	if strings.ContainsAny(hdr, "\r\n") {
		t.Errorf("challenge must not carry CR/LF, got %q", hdr)
	}
	if w.Header().Get("X-Injected") != "" {
		t.Errorf("header injection succeeded: %v", w.Header())
	}
}

// stubParser rejects everything; it exists so challenge-shape tests need no
// JWKS server.
type stubParser struct{}

func (stubParser) Parse(context.Context, string) (*auth.TokenClaims, error) {
	return nil, auth.ErrTokenInvalid
}
func (stubParser) ParseServiceToken(context.Context, string) (*auth.ServiceTokenClaims, error) {
	return nil, auth.ErrTokenInvalid
}

// ── Metrics ───────────────────────────────────────────────────────────────────

func TestAuthMetrics_CountOutcomes(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	mux := newMux(srv)

	do := func(token string) {
		r := httptest.NewRequest(http.MethodGet, "/state", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}

	scoped := validServiceClaims(srv.IdentityURL)
	scoped["scope"] = "statehouse:read"

	do(signJWT(t, priv, kid, validClaims(srv.IdentityURL)))               // user accepted
	do(signServiceJWT(t, priv, kid, scoped))                              // service accepted
	do("")                                                                // missing credentials
	do("garbage")                                                         // invalid token
	do(signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))) // no scope

	got := srv.authMetricsSnapshot()
	for _, c := range []struct {
		name string
		got  uint64
	}{
		{"user_tokens_accepted_total", got.UserTokensAccepted},
		{"service_tokens_accepted_total", got.ServiceTokensAccepted},
		{"rejected_missing_credentials_total", got.RejectedMissingCredentials},
		{"rejected_invalid_token_total", got.RejectedInvalidToken},
		{"rejected_insufficient_scope_total", got.RejectedInsufficientScope},
	} {
		if c.got != 1 {
			t.Errorf("%s = %d, want 1", c.name, c.got)
		}
	}
}

func TestMetricsEndpoint_ExposesAuthCounters(t *testing.T) {
	srv, priv, kid := authSetup(t)
	token := signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))
	w := getWith(t, srv, "/metrics", token)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var doc struct {
		Auth map[string]uint64 `json:"auth"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if doc.Auth == nil {
		t.Fatal("metrics has no auth block")
	}
	if doc.Auth["service_tokens_accepted_total"] != 1 {
		t.Errorf("service_tokens_accepted_total = %d, want 1", doc.Auth["service_tokens_accepted_total"])
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

// The middleware is shared by every in-flight request. Run under -race.
func TestServiceToken_ConcurrentRequests(t *testing.T) {
	srv, priv, kid := authSetup(t)
	mux := newMux(srv)
	token := signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL))

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/state", nil)
			// Half valid, half junk, so both counter paths are contended.
			if i%2 == 0 {
				r.Header.Set("Authorization", "Bearer "+token)
			} else {
				r.Header.Set("Authorization", "Bearer "+fmt.Sprintf("junk-%d", i))
			}
			mux.ServeHTTP(httptest.NewRecorder(), r)
		}(i)
	}
	wg.Wait()

	if got := srv.authMetricsSnapshot().ServiceTokensAccepted; got != 16 {
		t.Errorf("service_tokens_accepted_total = %d, want 16", got)
	}
}

// ── Policy plumbing ───────────────────────────────────────────────────────────

// A Server built by New must accept service tokens without the caller
// remembering to opt in — a zero-valued policy would silently keep the bug.
func TestNew_DefaultsToAcceptingServiceTokens(t *testing.T) {
	srv, _ := setup(t)
	if !srv.ServiceTokens.Enabled {
		t.Error("New() should default to accepting service tokens")
	}
	if srv.ServiceTokens.RequiredAudience != "" || srv.ServiceTokens.RequiredScope != "" ||
		len(srv.ServiceTokens.AllowedClients) != 0 {
		t.Errorf("default policy should be unrestricted, got %+v", srv.ServiceTokens)
	}
}

// ── Disclosure and short-circuiting ───────────────────────────────────────────

// A 401 body is the one response an unauthenticated stranger can always read.
// Nothing about the token they presented may come back in it — a reflected
// credential turns any log, proxy cache or error tracker that captures response
// bodies into a place tokens accumulate.
func TestChallenge_NeverReflectsTheToken(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.AllowedClients = []string{"greenhouse"}

	marker := "d15c105ab1e-secret-marker"
	claims := validServiceClaims(srv.IdentityURL)
	claims["client_id"] = "countinghouse-" + marker
	claims["jti"] = marker

	for name, token := range map[string]string{
		"malformed": "Bearer-" + marker,
		"rejected":  signServiceJWT(t, priv, kid, claims),
		"wrong issuer": signServiceJWT(t, priv, kid,
			map[string]any{"iss": "https://evil." + marker, "client_id": "x",
				"exp": time.Now().Add(time.Minute).Unix()}),
	} {
		t.Run(name, func(t *testing.T) {
			w := getWith(t, srv, "/state", token)
			if strings.Contains(w.Body.String(), marker) {
				t.Errorf("response body reflects the credential: %q", w.Body.String())
			}
			for k, vs := range w.Header() {
				for _, v := range vs {
					if strings.Contains(v, marker) {
						t.Errorf("header %s reflects the credential: %q", k, v)
					}
				}
			}
		})
	}
}

// Every rejection must stop at the middleware. A handler that still ran would
// have already read house state, whatever status code was written after it.
func TestRejectedRequests_NeverReachTheHandler(t *testing.T) {
	srv, priv, kid := authSetup(t)
	srv.ServiceTokens.RequiredScope = "statehouse:read"
	srv.ServiceTokens.AllowedClients = []string{"countinghouse"}

	expiredUser := validClaims(srv.IdentityURL)
	expiredUser["exp"] = time.Now().Add(-time.Minute).Unix()
	inactive := validClaims(srv.IdentityURL)
	inactive["act"] = false
	wrongClient := validServiceClaims(srv.IdentityURL)
	wrongClient["client_id"] = "greenhouse"

	tokens := map[string]string{
		"no credentials": "",
		"garbage":        "garbage",
		"expired user":   signJWT(t, priv, kid, expiredUser),
		"inactive user":  signJWT(t, priv, kid, inactive),
		"no scope":       signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)),
		"wrong client":   signServiceJWT(t, priv, kid, wrongClient),
	}

	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			called := false
			a := &authenticator{
				parser:  srv.mustVerifier(t),
				realm:   srv.IdentityURL,
				service: srv.ServiceTokens,
				logger:  slog.New(slog.DiscardHandler),
				metrics: &authMetrics{},
			}
			h := requireAuth(a, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			r := httptest.NewRequest(http.MethodGet, "/state", nil)
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if called {
				t.Errorf("handler ran for a rejected request (status %d)", w.Code)
			}
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Errorf("want 401 or 403, got %d", w.Code)
			}
		})
	}
}

// mustVerifier builds a verifier against the server's fake JWKS endpoint.
func (s *Server) mustVerifier(t *testing.T) *auth.JWKSVerifier {
	t.Helper()
	v, err := auth.NewJWKSVerifier(auth.JWKSVerifierConfig{
		IssuerURL: s.IdentityURL,
		Issuer:    s.IdentityURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
