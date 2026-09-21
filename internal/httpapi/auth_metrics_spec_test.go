package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// /metrics gained a nine-field `auth` object, and the spec is the thing a
// consumer generates a client from. These pin the two halves of that: the
// schema exists and agrees with the struct (via documentedSchemas, which drives
// the bidirectional checks in spec_fields_test.go), and the block is present or
// absent for the right reason.

// MetricsResponse must actually reference the schema; a correct AuthMetrics
// component nobody points at documents nothing.
func TestSpecMetricsResponseDocumentsTheAuthBlock(t *testing.T) {
	declared := specSchemaProperties(t, fetchSpec(t), "MetricsResponse")
	if _, ok := declared["auth"]; !ok {
		t.Error("MetricsResponse does not document the auth block /metrics emits")
	}
}

// End-to-end rather than struct-to-schema: this reads the keys off a real
// /metrics response, so it also covers the wiring between the two.
func TestMetricsAuthBlockKeysMatchTheSchema(t *testing.T) {
	srv, priv, kid := authSetup(t)
	w := getWith(t, srv, "/metrics", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var doc struct {
		Auth map[string]json.RawMessage `json:"auth"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if len(doc.Auth) == 0 {
		t.Fatal("/metrics served no auth block")
	}

	declared := specSchemaProperties(t, fetchSpec(t), "AuthMetrics")
	var undocumented, phantom []string
	for k := range doc.Auth {
		if _, ok := declared[k]; !ok {
			undocumented = append(undocumented, k)
		}
	}
	for k := range declared {
		if _, ok := doc.Auth[k]; !ok {
			phantom = append(phantom, k)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)
	if len(undocumented) > 0 {
		t.Errorf("/metrics emits auth keys the spec does not declare: %v", undocumented)
	}
	if len(phantom) > 0 {
		t.Errorf("AuthMetrics declares keys /metrics never emits: %v", phantom)
	}
}

// With no identity service configured, authMiddleware is a no-op wrapper and no
// counter is ever touched. A block of zeros there reads as "no rejections, all
// healthy" on a server where every endpoint is readable without a token — the
// one posture actually worth alerting on. Absent has to mean "auth is off",
// which is how the neighbouring jwks block already behaves.
func TestMetricsOmitsAuthWhenAuthIsDisabled(t *testing.T) {
	srv, _ := setup(t) // no IdentityURL: auth off entirely
	w := httptest.NewRecorder()
	newMux(srv).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if _, ok := doc["auth"]; ok {
		t.Errorf("auth block present on a server with authentication disabled: %s", doc["auth"])
	}
	// Same rule, already true for the neighbour — asserted so the two cannot
	// drift into disagreeing about what absence means.
	if _, ok := doc["jwks"]; ok {
		t.Errorf("jwks block present with authentication disabled: %s", doc["jwks"])
	}
}

func TestMetricsIncludesAuthWhenAuthIsConfigured(t *testing.T) {
	srv, priv, kid := authSetup(t)
	w := getWith(t, srv, "/metrics", signServiceJWT(t, priv, kid, validServiceClaims(srv.IdentityURL)))
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if _, ok := doc["auth"]; !ok {
		t.Error("auth block missing on a server with authentication configured")
	}
}
