package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// corsSetup returns a Server whose handler chain is the real one — CORS wrapper
// above the mux — with the given allowlist. Tests go through handler() rather
// than newMux() deliberately: the whole point of this change is where the
// wrapper sits relative to auth and the mux, and a test that reached for the
// mux directly would not be testing that at all.
func corsSetup(t *testing.T, origins ...string) http.Handler {
	t.Helper()
	srv, _ := setup(t)
	srv.AllowedOrigins = origins
	h, err := srv.handler()
	if err != nil {
		t.Fatalf("handler(): %v", err)
	}
	return h
}

// corsSetupAuthed is corsSetup with auth switched on, so the tests that matter
// most — a preflight that must bypass auth, and a 401 that must still carry the
// CORS headers — exercise the real requireAuth path rather than the no-op.
func corsSetupAuthed(t *testing.T, origins ...string) http.Handler {
	t.Helper()
	srv, _ := setup(t)
	srv.IdentityURL = "https://id.example.com"
	srv.AllowedOrigins = origins
	h, err := srv.handler()
	if err != nil {
		t.Fatalf("handler(): %v", err)
	}
	return h
}

func do(h http.Handler, method, path, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		r.Header.Set("Access-Control-Request-Method", "GET")
		r.Header.Set("Access-Control-Request-Headers", "authorization")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func assertHeader(t *testing.T, w *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	if got := w.Header().Get(key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func assertNoHeader(t *testing.T, w *httptest.ResponseRecorder, key string) {
	t.Helper()
	if got := w.Header().Get(key); got != "" {
		t.Errorf("%s = %q, want the header to be absent", key, got)
	}
}

func assertVaryOrigin(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, v := range w.Header().Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Origin") {
				return
			}
		}
	}
	t.Errorf("Vary = %v, want it to include Origin", w.Header().Values("Vary"))
}

const allowlisted = "https://app.swee.net"

func testOrigins() []string { return []string{"https://*.swee.net", "http://localhost:*"} }

// ── Preflight ────────────────────────────────────────────────────────────────

// The headline case. A browser never attaches credentials to a preflight, so an
// OPTIONS that reaches requireAuth can never be satisfied; the wrapper answers
// it above auth.
func TestPreflightOnAuthenticatedRouteIsAnsweredWithoutAuth(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	w := do(h, http.MethodOptions, "/state", allowlisted)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
	assertHeader(t, w, "Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	assertHeader(t, w, "Access-Control-Allow-Headers", "Authorization, Content-Type")
	assertHeader(t, w, "Access-Control-Max-Age", "86400")
	assertVaryOrigin(t, w)

	// No challenge: a preflight that answers with WWW-Authenticate is a
	// preflight auth ran on, which is the bug being fixed.
	assertNoHeader(t, w, "WWW-Authenticate")
}

// A preflight from an origin that is not allowlisted gets a well-formed
// response with no CORS headers. The browser refuses the real request on the
// strength of their absence — which is the specified way to say no, and leaks
// nothing about whether the route exists.
func TestPreflightFromDisallowedOriginCarriesNoCORSHeaders(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	w := do(h, http.MethodOptions, "/state", "https://evil.example.com")

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	assertNoHeader(t, w, "Access-Control-Allow-Origin")
	assertNoHeader(t, w, "Access-Control-Allow-Methods")
	assertNoHeader(t, w, "Access-Control-Allow-Headers")
	// Vary is still set: the response would have differed for another origin,
	// and a cache in front of this must not serve it as if it were universal.
	assertVaryOrigin(t, w)
}

// Preflights are answered for every allowlisted route, not just the one the
// issue sampled.
func TestPreflightOnEveryRoute(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	for _, path := range []string{
		"/state", "/state/house", "/state/devices",
		"/state/devices/0xabc", "/state/activity", "/events/recent",
		"/metrics", "/config/devices", "/config/devices/0xabc",
	} {
		w := do(h, http.MethodOptions, path, allowlisted)
		if w.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s status = %d, want 204", path, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != allowlisted {
			t.Errorf("OPTIONS %s ACAO = %q, want %q", path, got, allowlisted)
		}
	}
	// The public routes answer a preflight with their wildcard instead.
	for _, path := range []string{"/healthz", "/openapi.json"} {
		w := do(h, http.MethodOptions, path, allowlisted)
		if w.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s status = %d, want 204", path, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != publicACAO {
			t.Errorf("OPTIONS %s ACAO = %q, want %q", path, got, publicACAO)
		}
	}
}

// ── Error responses ──────────────────────────────────────────────────────────

// The regression the issue singles out: without ACAO on the 401 a browser
// cannot read the response at all, so a client cannot tell "your token is
// stale" from "the service is down". It is invisible from the server side,
// which is exactly why it gets a test of its own.
func TestUnauthorisedResponseStillCarriesCORSHeaders(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	w := do(h, http.MethodGet, "/state", allowlisted)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
	assertVaryOrigin(t, w)
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("WWW-Authenticate missing: the 401 should still challenge")
	}
}

// A browser can only read WWW-Authenticate if it is explicitly exposed — it is
// not on the CORS-safelisted list. Without this the realm on a 401 is invisible
// to the client that needs it.
func TestUnauthorisedResponseExposesTheChallengeHeader(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	w := do(h, http.MethodGet, "/state", allowlisted)
	assertHeader(t, w, "Access-Control-Expose-Headers", "WWW-Authenticate")
}

// A 404 from a handler must carry the headers too, for the same reason.
func TestNotFoundResponseCarriesCORSHeaders(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	w := do(h, http.MethodGet, "/state/devices/does-not-exist", allowlisted)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
	assertVaryOrigin(t, w)
}

// So must a 404 from the mux itself, which no handler ever sees.
func TestMuxNotFoundCarriesCORSHeaders(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	w := do(h, http.MethodGet, "/no/such/route", allowlisted)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
}

// ── Unauthenticated routes ───────────────────────────────────────────────────

// /healthz proves the wrapper is above the mux rather than inside the auth
// path: auth never runs for this route, so a header appearing here can only
// have come from a layer that wraps everything.
func TestHealthzCarriesCORSHeaders(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	w := do(h, http.MethodGet, "/healthz", allowlisted)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", publicACAO)
}

// /healthz is a public route, so it answers every origin with the flat
// wildcard. It is unauthenticated, so anything that is not a browser can
// already read it; a browser liveness check is the same request, and making it
// the one unauthenticated route a page cannot read would be a difference with
// nothing behind it.
func TestHealthzIsPublicallyWildcarded(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	for _, o := range []string{"https://evil.example.com", "null", allowlisted, ""} {
		w := do(h, http.MethodGet, "/healthz", o)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d for origin %q, want 200", w.Code, o)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != publicACAO {
			t.Errorf("ACAO for %q = %q, want %q", o, got, publicACAO)
		}
	}
}

// ...and keeps the wildcard on a deployment that configures no allowlist at
// all, since it is the route's own policy rather than something the allowlist
// grants.
func TestHealthzKeepsWildcardWithNoAllowlist(t *testing.T) {
	h := corsSetup(t)
	w := do(h, http.MethodGet, "/healthz", "https://evil.example.com")
	assertHeader(t, w, "Access-Control-Allow-Origin", publicACAO)
}

// ── The public route ─────────────────────────────────────────────────────────

// The spec keeps its flat wildcard. It is unauthenticated, so it is already
// world-readable by anything that is not a browser — CORS restricts browser JS,
// it is not access control. Narrowing it to the allowlist would protect nothing
// and would break Swagger UI, Redoc, editor.swagger.io and every codegen tool
// that fetches a spec from an arbitrary origin.
func TestOpenAPIJSONKeepsWildcardForAnyOrigin(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	w := do(h, http.MethodGet, "/openapi.json", "https://editor.swagger.io")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", "*")
}

// The wildcard is the route's own policy, so it does not depend on the
// allowlist being configured at all. This is the case that would regress if the
// public-route rule were ever folded back into the allowlist path.
func TestOpenAPIJSONKeepsWildcardWithNoAllowlist(t *testing.T) {
	h := corsSetup(t)
	w := do(h, http.MethodGet, "/openapi.json", "https://editor.swagger.io")
	assertHeader(t, w, "Access-Control-Allow-Origin", "*")
}

// ...including with no Origin header at all, which is how the hand-set header
// in spec.go behaved and how a non-browser fetch of the spec arrives.
func TestOpenAPIJSONKeepsWildcardWithNoOrigin(t *testing.T) {
	h := corsSetup(t)
	w := do(h, http.MethodGet, "/openapi.json", "")
	assertHeader(t, w, "Access-Control-Allow-Origin", "*")
}

// An allowlisted origin does not get its own echo on the public route: one
// route, one answer, cacheable once.
func TestOpenAPIJSONWildcardEvenForAllowlistedOrigin(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	w := do(h, http.MethodGet, "/openapi.json", allowlisted)
	assertHeader(t, w, "Access-Control-Allow-Origin", "*")
}

// The wildcard is set exactly once. Two values in one header is a malformed
// response that browsers reject outright, and is what would happen if the
// hand-set line in spec.go survived alongside the wrapper.
func TestOpenAPIJSONSetsACAOExactlyOnce(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	w := do(h, http.MethodGet, "/openapi.json", allowlisted)
	if got := w.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 {
		t.Errorf("Access-Control-Allow-Origin = %v, want exactly one value", got)
	}
}

// A preflight of the spec route is answered like any other.
func TestOpenAPIJSONPreflight(t *testing.T) {
	h := corsSetupAuthed(t, testOrigins()...)
	w := do(h, http.MethodOptions, "/openapi.json", "https://editor.swagger.io")

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", "*")
	assertHeader(t, w, "Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
}

// ── Origin matching through the HTTP layer ───────────────────────────────────

func TestDisallowedOriginGetsNoHeadersOnARealRequest(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	for _, o := range []string{
		"https://evil.example.com",
		"https://evil-swee.net", // the suffix-matching trap
		"https://swee.net",      // the apex is not the wildcard
		"http://app.swee.net",   // wrong scheme
		"null",
	} {
		w := do(h, http.MethodGet, "/state", o)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /state from %s status = %d, want 200 (auth is off here)", o, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("ACAO for %s = %q, want absent", o, got)
		}
		assertVaryOrigin(t, w)
	}
}

func TestLocalhostAnyPortIsAllowed(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	for _, o := range []string{"http://localhost:3000", "http://localhost:5173", "http://localhost"} {
		w := do(h, http.MethodGet, "/state", o)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != o {
			t.Errorf("ACAO for %s = %q, want %q", o, got, o)
		}
	}
}

// ── No allowlist configured ──────────────────────────────────────────────────

// An unedited config behaves exactly as the service did before this change: no
// origin is allowlisted, so no allowlisted route emits a CORS header. Turning
// CORS on is an explicit act.
func TestNoAllowedOriginsMeansNoCORSHeaders(t *testing.T) {
	h := corsSetup(t)
	for _, path := range []string{"/state", "/metrics", "/state/devices", "/events/recent"} {
		w := do(h, http.MethodGet, path, allowlisted)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s ACAO = %q, want absent", path, got)
		}
	}
}

// ── The allow-all policy ─────────────────────────────────────────────────────

// An operator who wants what countinghouse and greenhouse do can ask for it
// with one entry, and gets a flat wildcard rather than a per-origin echo.
func TestAllowAllPolicyEmitsFlatWildcard(t *testing.T) {
	h := corsSetup(t, "*")
	w := do(h, http.MethodGet, "/state", "https://anywhere.example.com")
	assertHeader(t, w, "Access-Control-Allow-Origin", "*")

	pre := do(h, http.MethodOptions, "/state", "null")
	assertHeader(t, pre, "Access-Control-Allow-Origin", "*")
}

// ── Timing-Allow-Origin ──────────────────────────────────────────────────────

// Timing-Allow-Origin is a separate opt-in that CORS does not imply: without it
// a cross-origin consumer's PerformanceResourceTiming entry has every phase
// (DNS, TCP, TLS, TTFB) and both transfer sizes zeroed, leaving only total
// duration — so a dashboard cannot tell a slow query from a slow network.
// countinghouse and greenhouse both send it; statehouse sends it to the same
// origins it already answers, rather than to everyone.
func TestTimingAllowOriginTracksACAO(t *testing.T) {
	h := corsSetup(t, testOrigins()...)

	w := do(h, http.MethodGet, "/state", allowlisted)
	assertHeader(t, w, "Timing-Allow-Origin", allowlisted)

	spec := do(h, http.MethodGet, "/openapi.json", "https://editor.swagger.io")
	assertHeader(t, spec, "Timing-Allow-Origin", "*")

	denied := do(h, http.MethodGet, "/state", "https://evil.example.com")
	assertNoHeader(t, denied, "Timing-Allow-Origin")
}

// ── Requests with no Origin ──────────────────────────────────────────────────

// A same-origin browser request and every server-side client send no Origin at
// all. Nothing is echoed for them, and nothing about the response changes.
func TestNoOriginHeaderGetsNoEcho(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	w := do(h, http.MethodGet, "/state", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	assertNoHeader(t, w, "Access-Control-Allow-Origin")
}

// ── Header injection ─────────────────────────────────────────────────────────

// The echoed value comes from a request header. A non-browser client can put
// anything in it, so the matcher must refuse everything that is not an origin
// — nothing here may reach the response.
func TestHostileOriginValuesAreNeverEchoed(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	for _, o := range []string{
		"https://app.swee.net evil",
		"https://app.swee.net/../../evil",
		"https://evil.com@app.swee.net",
		"https://app.swee.net.evil.com",
		"https://APP.SWEE.NET.evil.com",
		"*",
		"https://*.swee.net",
	} {
		w := do(h, http.MethodGet, "/state", o)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("ACAO for %q = %q, want absent", o, got)
		}
	}
}

// ── Construction errors ──────────────────────────────────────────────────────

// A bad allowlist is refused at construction rather than silently skipped, so a
// typo cannot quietly leave an origin unallowlisted or a service wide open.
func TestHandlerRejectsABadAllowlist(t *testing.T) {
	srv, _ := setup(t)
	srv.AllowedOrigins = []string{"https://app.swee.net", "not-an-origin"}
	if _, err := srv.handler(); err == nil {
		t.Fatal("handler() = nil error for a malformed allowlist, want a rejection")
	}
}

// ── Deployments that never enable CORS ───────────────────────────────────────

// Vary: Origin is only sent where the response can actually vary by origin.
// With no allowlist it never can, and adding the header anyway would fragment
// the edge cache key on an attacker-controllable value for no benefit —
// Cloudflare sits in front of this service, and Origin is unbounded on any
// non-browser client.
func TestNoAllowedOriginsSendsNoVary(t *testing.T) {
	h := corsSetup(t)
	for _, path := range []string{"/state", "/metrics", "/no/such/route"} {
		w := do(h, http.MethodGet, path, allowlisted)
		if got := w.Header().Values("Vary"); len(got) != 0 {
			t.Errorf("%s Vary = %v, want the header to be absent with CORS off", path, got)
		}
	}
}

// A public route's answer is the same for every origin, so it does not vary
// either — and a cache can store it once rather than once per origin that asks.
func TestPublicRoutesSendNoVary(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	for _, path := range []string{"/openapi.json", "/healthz"} {
		w := do(h, http.MethodGet, path, allowlisted)
		if got := w.Header().Values("Vary"); len(got) != 0 {
			t.Errorf("%s Vary = %v, want the header to be absent on a public route", path, got)
		}
	}
}

// With no allowlist configured, OPTIONS is dispatched to the mux exactly as it
// was before this change. No handler checks r.Method, so these routes really
// did serve a full response to an OPTIONS — the CORS layer must not quietly
// take that away from a deployment that never opted in.
func TestNoAllowedOriginsLeavesOPTIONSToTheMux(t *testing.T) {
	h := corsSetup(t)
	for _, path := range []string{"/healthz", "/openapi.json", "/state"} {
		w := do(h, http.MethodOptions, path, allowlisted)
		if w.Code != http.StatusOK {
			t.Errorf("OPTIONS %s status = %d, want 200 (dispatched to the mux)", path, w.Code)
		}
		if w.Body.Len() == 0 {
			t.Errorf("OPTIONS %s returned an empty body, want the handler's response", path)
		}
	}
}

// Once an allowlist exists, the CORS layer owns OPTIONS for every route.
func TestOPTIONSIsShortCircuitedOnceCORSIsEnabled(t *testing.T) {
	h := corsSetup(t, testOrigins()...)
	for _, path := range []string{"/healthz", "/openapi.json", "/state"} {
		w := do(h, http.MethodOptions, path, allowlisted)
		if w.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s status = %d, want 204", path, w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf("OPTIONS %s returned a body, want none", path)
		}
	}
}

// ── HEAD ─────────────────────────────────────────────────────────────────────

// HEAD is served by the mux today, so it belongs in Access-Control-Allow-
// Methods: without it HEAD is the one request a browser cannot make that a
// non-browser client can, and the failure surfaces as a preflight rejection
// that explains nothing.
func TestHEADIsAnAllowedMethod(t *testing.T) {
	h := corsSetup(t, testOrigins()...)

	pre := do(h, http.MethodOptions, "/state", allowlisted)
	assertHeader(t, pre, "Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")

	w := do(h, http.MethodHead, "/state", allowlisted)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD /state status = %d, want 200", w.Code)
	}
	assertHeader(t, w, "Access-Control-Allow-Origin", allowlisted)
}
