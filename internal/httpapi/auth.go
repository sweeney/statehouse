// Package httpapi's authentication middleware.
//
// Two kinds of principal reach this API, and identity issues a different token
// for each:
//
//   - a *user*, signed in through the UI, carrying a token with a JOSE `typ` of
//     "JWT", a role and an `act` (is-active) flag; and
//   - a *service* authenticating with the client_credentials grant, carrying a
//     token with a `typ` of "at+jwt", a `client_id`, and optionally `aud` and
//     `scope`.
//
// identity/common parses them with two different functions that each refuse the
// other's token outright, so a middleware that calls only one of them locks the
// other principal out of the whole API. That was issue #69: every endpoint was
// unreachable by a sibling service, which is why countinghouse and greenhouse
// read the devices namespace directly instead.
//
// A service principal has no is-active flag and no role, so the check that
// bounds a user token has no equivalent. What stands in for it is
// ServiceTokenPolicy: a signed token from the configured issuer, optionally
// narrowed by audience, scope and a client allowlist.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/sweeney/identity/common/auth"
	"github.com/sweeney/statehouse/internal/config"
)

// JOSE `typ` values. identity stamps "at+jwt" on client_credentials tokens and
// "JWT" on user access tokens, and identity/common keys both of its parsers off
// exactly that distinction.
const (
	userTokenTyp    = "JWT"
	serviceTokenTyp = "at+jwt"
)

// RFC 6750 §3.1 error codes, used in the WWW-Authenticate challenge.
const (
	errCodeInvalidToken      = "invalid_token"
	errCodeInsufficientScope = "insufficient_scope"
)

// ServiceTokenPolicy decides what a client_credentials service token may do.
// The zero value accepts nothing; use ServiceTokenPolicyFromConfig or
// DefaultServiceTokenPolicy.
type ServiceTokenPolicy struct {
	// Enabled accepts service tokens at all.
	Enabled bool
	// RequiredAudience, when set, requires the token's `aud` to include it.
	RequiredAudience string
	// RequiredScope, when set, requires the token's `scope` to include it.
	RequiredScope string
	// AllowedClients, when non-empty, restricts callers to these client_ids,
	// matched exactly.
	AllowedClients []string
}

// DefaultServiceTokenPolicy is what applies when the config says nothing:
// service tokens are accepted, with no audience, scope or client restriction.
// See config.ServiceTokenConfig for why the permissive default is the safe one.
func DefaultServiceTokenPolicy() ServiceTokenPolicy {
	return ServiceTokenPolicy{Enabled: true}
}

// ServiceTokenPolicyFromConfig maps the YAML block onto the policy.
func ServiceTokenPolicyFromConfig(c config.ServiceTokenConfig) ServiceTokenPolicy {
	return ServiceTokenPolicy{
		Enabled:          c.IsEnabled(),
		RequiredAudience: c.RequiredAudience,
		RequiredScope:    c.RequiredScope,
		AllowedClients:   c.AllowedClients,
	}
}

// audienceOK reports whether aud satisfies the policy. aud is the space-joined
// form identity/common returns for a possibly multi-valued `aud` claim, and
// each value is matched whole — a prefix of the required audience is not a
// match, or "https://statehouse.swee.net.evil.example" would pass.
func (p ServiceTokenPolicy) audienceOK(aud string) bool {
	if p.RequiredAudience == "" {
		return true
	}
	for _, a := range strings.Fields(aud) {
		if a == p.RequiredAudience {
			return true
		}
	}
	return false
}

// clientAllowed reports whether clientID is permitted. An empty allowlist
// permits any client the identity service issued a token to.
func (p ServiceTokenPolicy) clientAllowed(clientID string) bool {
	if len(p.AllowedClients) == 0 {
		return true
	}
	for _, c := range p.AllowedClients {
		if c == clientID {
			return true
		}
	}
	return false
}

// authMetrics counts authentication outcomes. Every field is a counter, and
// none of them records anything about who called — see authMetricsJSON.
type authMetrics struct {
	userAccepted            atomic.Uint64
	serviceAccepted         atomic.Uint64
	rejectedMissing         atomic.Uint64
	rejectedInvalid         atomic.Uint64
	rejectedInactive        atomic.Uint64
	rejectedServiceDisabled atomic.Uint64
	rejectedAudience        atomic.Uint64
	rejectedClient          atomic.Uint64
	rejectedScope           atomic.Uint64
}

// authMetricsJSON is the /metrics view of authMetrics.
//
// Counters only. A per-client breakdown would turn /metrics into a log of which
// services called and when, on an endpoint whose whole point is to be cheap to
// scrape and safe to graph; the client_id of an accepted call goes to the debug
// log instead, where it is bounded by the log's own retention.
type authMetricsJSON struct {
	UserTokensAccepted            uint64 `json:"user_tokens_accepted_total"`
	ServiceTokensAccepted         uint64 `json:"service_tokens_accepted_total"`
	RejectedMissingCredentials    uint64 `json:"rejected_missing_credentials_total"`
	RejectedInvalidToken          uint64 `json:"rejected_invalid_token_total"`
	RejectedInactiveUser          uint64 `json:"rejected_inactive_user_total"`
	RejectedServiceTokensDisabled uint64 `json:"rejected_service_tokens_disabled_total"`
	RejectedAudience              uint64 `json:"rejected_audience_total"`
	RejectedClientNotAllowed      uint64 `json:"rejected_client_not_allowed_total"`
	RejectedInsufficientScope     uint64 `json:"rejected_insufficient_scope_total"`
}

func (m *authMetrics) snapshot() authMetricsJSON {
	return authMetricsJSON{
		UserTokensAccepted:            m.userAccepted.Load(),
		ServiceTokensAccepted:         m.serviceAccepted.Load(),
		RejectedMissingCredentials:    m.rejectedMissing.Load(),
		RejectedInvalidToken:          m.rejectedInvalid.Load(),
		RejectedInactiveUser:          m.rejectedInactive.Load(),
		RejectedServiceTokensDisabled: m.rejectedServiceDisabled.Load(),
		RejectedAudience:              m.rejectedAudience.Load(),
		RejectedClientNotAllowed:      m.rejectedClient.Load(),
		RejectedInsufficientScope:     m.rejectedScope.Load(),
	}
}

// authMetricsSnapshot exposes the server's authentication counters.
func (s *Server) authMetricsSnapshot() authMetricsJSON { return s.authm.snapshot() }

// authenticator holds everything the middleware needs to decide a request.
// It is read-only once built and safe for concurrent use; the only mutable
// state is the atomic counter block, which is shared with the Server.
type authenticator struct {
	// parser is the auth.TokenParser interface rather than *auth.JWKSVerifier
	// so the challenge-shape tests need no JWKS server.
	parser  auth.TokenParser
	realm   string
	service ServiceTokenPolicy
	logger  *slog.Logger
	metrics *authMetrics
}

// requireAuth authenticates the Bearer token on the request, passing it to next
// only if it is a valid user token for an active user, or a service token the
// policy permits.
func requireAuth(a *authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			a.metrics.rejectedMissing.Add(1)
			// RFC 6750 §3.1: no credentials means no error code. One would
			// describe a token the client never sent.
			a.challenge(w, http.StatusUnauthorized, "", "", "")
			return
		}
		if isServiceToken(token) {
			a.serveService(w, r, token, next)
			return
		}
		a.serveUser(w, r, token, next)
	})
}

// serveUser handles a user access token. Unchanged in substance from before
// service tokens existed: a valid signature from the configured issuer, and an
// account that is still active.
func (a *authenticator) serveUser(w http.ResponseWriter, r *http.Request, token string, next http.Handler) {
	claims, err := a.parser.Parse(r.Context(), token)
	if err != nil {
		a.metrics.rejectedInvalid.Add(1)
		a.rejectInvalid(w, err)
		return
	}
	if !claims.IsActive {
		a.metrics.rejectedInactive.Add(1)
		a.challenge(w, http.StatusUnauthorized, errCodeInvalidToken, "user account is not active", "")
		return
	}
	a.metrics.userAccepted.Add(1)
	next.ServeHTTP(w, r)
}

// serveService handles a client_credentials token.
//
// The checks run credential-first: whether the token is ours (signature,
// issuer, expiry, audience) before whether the principal is permitted (client
// allowlist, scope). Ordering it the other way would answer 403 — "your token
// is fine, you just lack privilege" — to someone replaying a token minted for
// another service.
func (a *authenticator) serveService(w http.ResponseWriter, r *http.Request, token string, next http.Handler) {
	if !a.service.Enabled {
		a.metrics.rejectedServiceDisabled.Add(1)
		a.challenge(w, http.StatusUnauthorized, errCodeInvalidToken,
			"service token presented but this server does not accept service tokens", "")
		return
	}

	claims, err := a.parser.ParseServiceToken(r.Context(), token)
	if err != nil {
		a.metrics.rejectedInvalid.Add(1)
		a.rejectInvalid(w, err)
		return
	}

	if !a.service.audienceOK(claims.Audience) {
		a.metrics.rejectedAudience.Add(1)
		// Logged as a policy rejection: reaching here needs a token this
		// issuer signed, so it means a real client is misconfigured — or a
		// token for a sibling service is being replayed here.
		a.logger.Warn("service token rejected: audience",
			"client_id", claims.ClientID, "jti", claims.JTI, "required_audience", a.service.RequiredAudience)
		a.challenge(w, http.StatusUnauthorized, errCodeInvalidToken,
			"token audience does not include this server", "")
		return
	}

	if !a.service.clientAllowed(claims.ClientID) {
		a.metrics.rejectedClient.Add(1)
		a.logger.Warn("service token rejected: client not allowed",
			"client_id", claims.ClientID, "jti", claims.JTI)
		// 403, not 401: the credential is good, the principal is not
		// permitted. RFC 6750 defines only invalid_request, invalid_token and
		// insufficient_scope, and of those insufficient_scope is the one that
		// means "this token does not carry enough privilege for this
		// resource", which is exactly the case.
		a.challenge(w, http.StatusForbidden, errCodeInsufficientScope,
			"client is not permitted to call this API", "")
		return
	}

	if a.service.RequiredScope != "" && !claims.HasScope(a.service.RequiredScope) {
		a.metrics.rejectedScope.Add(1)
		a.logger.Warn("service token rejected: scope",
			"client_id", claims.ClientID, "jti", claims.JTI, "required_scope", a.service.RequiredScope)
		a.challenge(w, http.StatusForbidden, errCodeInsufficientScope,
			"token does not carry the scope this API requires", a.service.RequiredScope)
		return
	}

	a.metrics.serviceAccepted.Add(1)
	a.logger.Debug("service token accepted", "client_id", claims.ClientID, "jti", claims.JTI)
	next.ServeHTTP(w, r)
}

// rejectInvalid answers a token that failed parsing, distinguishing expiry
// because it is the one failure a correct client hits in normal operation and
// the one whose fix — fetch a new token — differs from all the others.
//
// Logged at debug, not warn: anyone can post an unsigned string at this
// endpoint, so a warn here is a log-flooding vector. The policy rejections
// above are warn precisely because reaching them requires a valid signature.
func (a *authenticator) rejectInvalid(w http.ResponseWriter, err error) {
	desc := "token is malformed, or signed by a key this server does not trust"
	if errors.Is(err, auth.ErrTokenExpired) {
		desc = "token expired"
	}
	a.logger.Debug("token rejected", "reason", desc)
	a.challenge(w, http.StatusUnauthorized, errCodeInvalidToken, desc, "")
}

// challenge writes a WWW-Authenticate header and the error response.
//
// errCode, desc and scope are omitted when empty. The descriptions are a fixed
// set of constants from this file: no part of the presented token, and nothing
// about the caller, is ever reflected back — a 401 body is the one response an
// unauthenticated stranger can always read.
func (a *authenticator) challenge(w http.ResponseWriter, status int, errCode, desc, scope string) {
	var b strings.Builder
	b.WriteString(`Bearer realm="`)
	b.WriteString(quoteSafe(a.realm))
	b.WriteString(`"`)
	if errCode != "" {
		b.WriteString(`, error="`)
		b.WriteString(quoteSafe(errCode))
		b.WriteString(`"`)
	}
	if desc != "" {
		b.WriteString(`, error_description="`)
		b.WriteString(quoteSafe(desc))
		b.WriteString(`"`)
	}
	if scope != "" {
		b.WriteString(`, scope="`)
		b.WriteString(quoteSafe(scope))
		b.WriteString(`"`)
	}
	w.Header().Set("WWW-Authenticate", b.String())

	body := "unauthorized"
	if status == http.StatusForbidden {
		body = "forbidden"
	}
	http.Error(w, body, status)
}

// quoteSafe makes s safe to embed in a quoted-string auth-param.
//
// The descriptions here are constants, but realm is the configured issuer —
// operator input. RFC 7230 forbids a bare CR or LF in a header value and Go's
// net/http would reject one at write time, but a stray quote would silently
// end the realm and let the rest of the string pose as further auth-params. A
// challenge is not the place to trust a config string, so drop the three
// characters that could restructure it rather than escaping them.
func quoteSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '"', r == '\\':
			return -1
		case r < 0x20, r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// bearerToken extracts the credential from an Authorization header value.
//
// RFC 7235 §2.1 makes the auth-scheme case-insensitive and allows more than one
// space before the credential; a client that sends "bearer" is correct, and
// rejecting it would be a 401 nobody could debug from the outside.
func bearerToken(header string) (string, bool) {
	const scheme = "Bearer"
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	rest := header[len(scheme):]
	// Require at least one space, so "BearerXYZ" is not read as the token XYZ.
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	token := strings.Trim(rest, " \t")
	return token, token != ""
}

// isServiceToken reports whether the token's JOSE header declares the service
// token `typ`, and so which of identity/common's two parsers should see it.
//
// The header is read without verifying the signature. That is safe, and only
// because of what it is used for: `typ` is inside the signed JOSE header, so a
// token whose typ has been altered fails signature verification in whichever
// parser this routes it to. Nothing here grants anything — it only chooses the
// function that will do the checking.
//
// The comparison is exact rather than case-folded, mirroring identity/common's
// own `typ == "at+jwt"` test: routing on a looser rule than the parser applies
// would send a token down a path that then refuses it.
func isServiceToken(token string) bool {
	seg, _, ok := strings.Cut(token, ".")
	if !ok || seg == "" {
		return false
	}
	raw, err := decodeJOSESegment(seg)
	if err != nil {
		return false
	}
	var hdr struct {
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return false
	}
	return hdr.Typ == serviceTokenTyp
}

// maxJOSEHeaderBytes caps how much of a caller-supplied header segment is
// decoded and unmarshalled. A JOSE header is a handful of short fields; this is
// generous by an order of magnitude and stops an unauthenticated caller from
// making the router decode a megabyte of base64 per request.
const maxJOSEHeaderBytes = 4096

// decodeJOSESegment base64url-decodes a JWT segment. JWTs are unpadded
// (RFC 7515 §2), but a padded segment costs nothing to accept and a token
// misrouted over padding would be a confusing 401.
func decodeJOSESegment(seg string) ([]byte, error) {
	if len(seg) > maxJOSEHeaderBytes {
		return nil, errors.New("jose header too long")
	}
	if raw, err := base64.RawURLEncoding.DecodeString(seg); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(seg)
}
