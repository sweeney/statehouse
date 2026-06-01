package httpapi

import (
	"net/http"
	"strings"

	"github.com/sweeney/identity/common/auth"
)

func requireAuth(verifier *auth.JWKSVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reject := func() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+verifier.Issuer()+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			reject()
			return
		}
		claims, err := verifier.Parse(r.Context(), token)
		if err != nil {
			reject()
			return
		}
		if !claims.IsActive {
			reject()
			return
		}
		next.ServeHTTP(w, r)
	})
}
