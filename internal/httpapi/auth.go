package httpapi

import (
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
	// AllowedClients, when non-empty, restricts callers to these client_ids.
	AllowedClients []string
}

// DefaultServiceTokenPolicy is what applies when the config says nothing:
// service tokens are accepted, with no audience, scope or client restriction.
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
type authenticator struct {
	parser  auth.TokenParser
	realm   string
	service ServiceTokenPolicy
	logger  *slog.Logger
	metrics *authMetrics
}

// isServiceToken reports whether the token's JOSE header declares the service
// token `typ`.
//
// Not implemented yet — see the service-token tests.
func isServiceToken(string) bool { return false }

func requireAuth(a *authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reject := func() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+a.realm+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			a.metrics.rejectedMissing.Add(1)
			reject()
			return
		}
		claims, err := a.parser.Parse(r.Context(), token)
		if err != nil {
			a.metrics.rejectedInvalid.Add(1)
			reject()
			return
		}
		if !claims.IsActive {
			a.metrics.rejectedInactive.Add(1)
			reject()
			return
		}
		a.metrics.userAccepted.Add(1)
		next.ServeHTTP(w, r)
	})
}
