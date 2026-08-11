package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

// The site tag identifies which property an instance reports for, and until
// now it was write-only: it reached Influx and stopped there. A consumer
// talking to two statehouse instances could not tell them apart from the API,
// only from data they had already written. /state is where a client asks an
// instance what it is, so the site belongs in that response.

func TestStateResponseCarriesSite(t *testing.T) {
	srv, _ := setup(t)
	srv.Site = "home"

	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Decode into a map so this asserts the wire format, not just the struct.
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := raw["site"]; got != "home" {
		t.Errorf("site = %v, want %q", got, "home")
	}
}

// A server with no site configured must omit the key rather than emit an
// empty string, so a consumer can distinguish "not reported" from a real
// value. In practice Config.Validate refuses to start without a site, so this
// is the shape tests and tooling see rather than production.
func TestStateResponseOmitsUnsetSite(t *testing.T) {
	srv, _ := setup(t)

	mux := newMux(srv)
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, ok := raw["site"]; ok {
		t.Errorf("site present as %v; an unset site must omit the key", v)
	}
}

// The MQTT publisher shares this DTO through the exported builder, and a
// retained MQTT snapshot is exactly where two properties would collide. The
// site has to travel with it, not just with the HTTP response.
func TestBuildSnapshotCarriesSite(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	snap := model.Snapshot{GeneratedAt: now, Devices: map[string]model.Device{}}

	got := BuildSnapshot(snap, nil, nil, now, nil, time.Time{}, "home")

	if got.Site != "home" {
		t.Errorf("Site = %q, want home", got.Site)
	}
}

// The spec is the contract the desktop and phone clients read, so a field that
// exists on the wire but not in the spec is a field consumers cannot rely on.
func TestOpenAPIJSON_DocumentsSite(t *testing.T) {
	s, _ := setup(t)
	mux := newMux(s)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}

	schema, ok := doc.Components.Schemas["SnapshotResponse"]
	if !ok {
		t.Fatalf("spec has no SnapshotResponse schema; schemas present: %v", schemaNames(doc.Components.Schemas))
	}
	site, ok := schema.Properties["site"]
	if !ok {
		t.Fatal("SnapshotResponse schema does not document the `site` property")
	}
	if site.Type != "string" {
		t.Errorf("site type = %q, want string", site.Type)
	}
	if site.Description == "" {
		t.Error("site property has no description")
	}
}

func schemaNames[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
