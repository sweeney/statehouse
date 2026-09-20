package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sweeney/statehouse/internal/config"
)

// specDoc fetches and decodes /openapi.json.
func specDoc(t *testing.T) map[string]any {
	t.Helper()
	s, _ := setup(t)
	w := httptest.NewRecorder()
	newMux(s).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	return doc
}

// Every authenticated path can now answer 403 (insufficient scope, or a client
// that is not on the allowlist), and a consumer generating a client off the
// spec needs to know that before it meets one in production.
func TestOpenAPISpec_ProtectedPathsDocument403(t *testing.T) {
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
		if _, ok := responses["403"]; !ok {
			t.Errorf("path %s: no 403 response documented", name)
		}
		if _, ok := responses["401"]; !ok {
			t.Errorf("path %s: no 401 response documented", name)
		}
	}
}

// The spec is the contract a sibling service reads before it writes a client.
// If it still says "user token", nobody learns service tokens are accepted.
func TestOpenAPISpec_DocumentsServiceTokens(t *testing.T) {
	doc := specDoc(t)
	components, _ := doc["components"].(map[string]any)
	schemes, _ := components["securitySchemes"].(map[string]any)
	bearer, _ := schemes["bearerAuth"].(map[string]any)
	desc, _ := bearer["description"].(string)
	if !strings.Contains(strings.ToLower(desc), "client_credentials") {
		t.Errorf("bearerAuth description should explain service tokens, got %q", desc)
	}
}

// ── config → policy ───────────────────────────────────────────────────────────

func TestServiceTokenPolicyFromConfig(t *testing.T) {
	no := false
	cases := []struct {
		name string
		in   config.ServiceTokenConfig
		want ServiceTokenPolicy
	}{
		{
			name: "zero config accepts service tokens",
			in:   config.ServiceTokenConfig{},
			want: ServiceTokenPolicy{Enabled: true},
		},
		{
			name: "explicit disable",
			in:   config.ServiceTokenConfig{Enabled: &no},
			want: ServiceTokenPolicy{Enabled: false},
		},
		{
			name: "restrictions carried across",
			in: config.ServiceTokenConfig{
				RequiredAudience: "https://statehouse.swee.net",
				RequiredScope:    "statehouse:read",
				AllowedClients:   []string{"countinghouse"},
			},
			want: ServiceTokenPolicy{
				Enabled:          true,
				RequiredAudience: "https://statehouse.swee.net",
				RequiredScope:    "statehouse:read",
				AllowedClients:   []string{"countinghouse"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ServiceTokenPolicyFromConfig(c.in)
			if got.Enabled != c.want.Enabled ||
				got.RequiredAudience != c.want.RequiredAudience ||
				got.RequiredScope != c.want.RequiredScope ||
				strings.Join(got.AllowedClients, ",") != strings.Join(c.want.AllowedClients, ",") {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
