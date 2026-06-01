package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// specPaths returns the set of path keys defined in the OpenAPI spec JSON.
func specPaths(t *testing.T, body []byte) map[string]struct{} {
	t.Helper()
	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	out := make(map[string]struct{}, len(doc.Paths))
	for p := range doc.Paths {
		out[p] = struct{}{}
	}
	return out
}

// registeredPaths mirrors the routes registered in newMux.
// Keep this in sync with newMux — the path coverage test will catch drift.
var registeredPaths = []string{
	"/healthz",
	"/openapi.json",
	"/state",
	"/state/house",
	"/state/devices",
	"/state/devices/{id}",
	"/state/activity",
	"/events/recent",
	"/metrics",
	"/config/devices",
	"/config/devices/{id}",
}

func TestOpenAPIJSON_PublicNoAuth(t *testing.T) {
	s, _ := setup(t)
	s.IdentityURL = "https://id.example.com" // auth configured but must not apply to spec
	mux := newMux(s)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("want application/json; charset=utf-8, got %q", ct)
	}
}

func TestOpenAPIJSON_ValidJSON(t *testing.T) {
	s, _ := setup(t)
	mux := newMux(s)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
}

func TestOpenAPIJSON_SpecStructure(t *testing.T) {
	s, _ := setup(t)
	mux := newMux(s)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"openapi", "info", "paths", "components"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("spec missing top-level key %q", key)
		}
	}
}

func TestOpenAPIJSON_PathCoverage(t *testing.T) {
	s, _ := setup(t)
	mux := newMux(s)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	specSet := specPaths(t, w.Body.Bytes())

	wantSet := make(map[string]struct{}, len(registeredPaths))
	for _, p := range registeredPaths {
		wantSet[p] = struct{}{}
	}

	var missing, extra []string
	for p := range wantSet {
		if _, ok := specSet[p]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range specSet {
		if _, ok := wantSet[p]; !ok {
			extra = append(extra, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("paths registered in newMux but missing from openapi.yaml: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("paths in openapi.yaml but not registered in newMux: %v", extra)
	}
}
