package httpapi

import (
	"net/http"

	"github.com/sweeney/statehouse/internal/origin"
)

// CORS preflight and response-header policy for browser clients.
//
// The wrapper sits above the mux — above auth, above every route — because two
// of the three things it has to get right are impossible anywhere else:
//
//   - A browser never attaches credentials to a preflight. That is specified
//     behaviour, not a client bug, so an OPTIONS request that reaches
//     requireAuth can never be satisfied. Preflights are answered here.
//   - Error responses need the headers too. Without Access-Control-Allow-Origin
//     on a 401 the browser refuses to expose the response at all, so a client
//     cannot tell "your token expired" from "the service is down". Setting the
//     headers on the way in means every response carries them, including the
//     ones no handler of ours ever sees, such as the mux's own 404.
//
// The third is that /healthz is not behind auth, so a layer inside the auth
// path could never cover it.

const (
	// allowedMethods is the read-only surface this API actually offers. It is
	// not derived from the routes because every route is a GET; when that stops
	// being true this is the line that has to change with it.
	allowedMethods = "GET, OPTIONS"

	// allowedHeaders is what a preflight may ask for. Authorization is the
	// Bearer token; Content-Type is here because a client that sets it on a GET
	// (some fetch wrappers do unconditionally) would otherwise fail preflight
	// for no reason.
	//
	// It is a fixed list rather than an echo of Access-Control-Request-Headers.
	// Echoing would approve anything asked for, which makes the header
	// meaningless and hides from the operator what is actually being sent.
	allowedHeaders = "Authorization, Content-Type"

	// exposedHeaders lets browser JS read WWW-Authenticate. Only a short
	// safelist (Content-Type, Cache-Control and a few others) is readable
	// cross-origin by default, so without this the realm on a 401 is invisible
	// to exactly the client that needs it.
	exposedHeaders = "WWW-Authenticate"

	// maxAge matches what config.swee.net and id.swee.net already send, so the
	// three services agree. Browsers cap it well below this (Chromium at 2h,
	// Firefox at 24h) and the value is only ever a ceiling, so agreeing costs
	// nothing and removes a difference nobody wants to explain later.
	maxAge = "86400"

	// publicACAO is the flat wildcard sent for publicRoutes.
	publicACAO = "*"
)

// publicRoutes are answered with Access-Control-Allow-Origin: * regardless of
// the configured allowlist.
//
// /openapi.json is the whole list, and it is here because the route is
// unauthenticated: the spec is already world-readable by anything that is not a
// browser, so narrowing it to the allowlist would protect nothing while breaking
// Swagger UI, Redoc, editor.swagger.io and every codegen tool that fetches a
// spec from an arbitrary origin. This is the same reasoning under which
// config.swee.net serves its public namespaces with a wildcard, and it replaces
// the header spec.go used to set by hand — same bytes on the wire, one place to
// reason about it.
//
// /healthz is deliberately NOT here, though it is also unauthenticated. The
// spec is a static document that says what the API is; /healthz reports live
// operational detail about one deployment — version, uptime, goroutine count,
// which remote namespaces are failing and the error text explaining why. A
// wildcard would let any page anyone happens to visit read that, which is worth
// declining for a fingerprint of a specific house even though a non-browser
// client can already fetch it.
var publicRoutes = map[string]struct{}{
	"/openapi.json": {},
}

// corsDecision is what the policy says about one request: the value to echo,
// whether anything is echoed at all, and whether that answer depended on the
// request's Origin — which is precisely what Vary: Origin exists to tell a
// cache. Keeping the three together means the Vary can never drift out of step
// with the header it describes.
type corsDecision struct {
	value      string
	allowed    bool
	varyOrigin bool
}

func decideCORS(policy origin.Policy, r *http.Request) corsDecision {
	if _, public := publicRoutes[r.URL.Path]; public {
		// One answer for every origin, so there is nothing to vary on and a
		// cache in front of this stores the spec once rather than once per
		// origin that asks for it.
		return corsDecision{value: publicACAO, allowed: true}
	}
	value, ok := policy.Allow(r.Header.Get("Origin"))
	// Vary whether or not the origin matched. The response *would* have
	// differed for a different origin, and that is what a cache needs to know:
	// Cloudflare sits in front of this service, so a missing Vary here would
	// mean one origin's headers served to another.
	return corsDecision{value: value, allowed: ok, varyOrigin: true}
}

// withCORS wraps h with the cross-origin policy described above.
//
// An empty policy leaves the allowlisted routes exactly as they were before
// CORS existed: no Access-Control-* headers on any of them. The public routes
// keep their wildcard either way, because that is the route's own policy rather
// than something the allowlist grants.
func withCORS(policy origin.Policy, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := decideCORS(policy, r)
		if d.varyOrigin {
			w.Header().Add("Vary", "Origin")
		}
		if d.allowed {
			w.Header().Set("Access-Control-Allow-Origin", d.value)
			// Timing-Allow-Origin is a separate opt-in that CORS does not
			// imply: without it a cross-origin consumer's
			// PerformanceResourceTiming entry has every phase (DNS, TCP, TLS,
			// TTFB) and both transfer sizes zeroed, leaving only total
			// duration — so a dashboard cannot tell a slow query from a slow
			// network. countinghouse and greenhouse both send it; statehouse
			// sends it to the origins it already answers rather than to
			// everyone, so the two headers cannot disagree about who is
			// trusted.
			w.Header().Set("Timing-Allow-Origin", d.value)
			w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
		}

		// Answered for every method-OPTIONS request, not only those carrying
		// Access-Control-Request-Method. A preflight from a disallowed origin
		// gets a well-formed 204 with no CORS headers, which is the specified
		// way to refuse: the browser blocks the real request on the strength of
		// their absence, and the response reveals nothing about whether the
		// route exists. A non-preflight OPTIONS gets the same 204, which is a
		// truthful answer for a read-only API that has no other use for the
		// method.
		if r.Method == http.MethodOptions {
			if d.allowed {
				w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
				w.Header().Set("Access-Control-Max-Age", maxAge)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
