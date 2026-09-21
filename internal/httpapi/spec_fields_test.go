package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// specSchemaProperties returns the property names declared for one schema in the
// served OpenAPI document.
func specSchemaProperties(t *testing.T, body []byte, schema string) map[string]struct{} {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	s, ok := doc.Components.Schemas[schema]
	if !ok {
		t.Fatalf("spec has no %s schema", schema)
	}
	out := make(map[string]struct{}, len(s.Properties))
	for p := range s.Properties {
		out[p] = struct{}{}
	}
	return out
}

// jsonFieldNames returns the JSON keys a struct type can emit, following embedded
// structs and skipping `json:"-"`. The omitempty suffix is stripped: whether a key is
// omitted at runtime is a value question, not a schema one.
func jsonFieldNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			// encoding/json flattens an embedded struct, and a *T embed the same as
			// T. Unwrap the pointer before recursing: reflect panics on NumField for
			// a Ptr kind, and the one file whose job is to fire when a DTO changes
			// shape should not do it with a stack trace. An embedded non-struct
			// (a named string, say) is an ordinary field and falls through.
			if f.Anonymous {
				ft := f.Type
				for ft.Kind() == reflect.Ptr {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					out = append(out, jsonFieldNames(ft)...)
					continue
				}
			}
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// fetchSpec returns the served OpenAPI document. Read through the handler rather than
// the file so the test covers what consumers actually download.
func fetchSpec(t *testing.T) []byte {
	t.Helper()
	s, _ := setup(t)
	w := httptest.NewRecorder()
	newMux(s).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/openapi.json: want 200, got %d", w.Code)
	}
	return w.Body.Bytes()
}

// documentedSchemas maps a response schema name to the Go type that produces it.
// Add an entry when a DTO gains a schema; the two device DTOs are here because they
// are the ones consumers join on, and the ones that drifted.
var documentedSchemas = map[string]any{
	"DeviceResponse":        DeviceResponse{},
	"DeviceProfileResponse": DeviceProfileResponse{},
}

// The path coverage test catches a route that is missing from the spec, but nothing
// catches a FIELD that is. That is the drift that actually bites a consumer: the
// endpoint is documented, so a client generates types from the spec, and then finds
// the response carrying keys its types do not have.
//
// This change is a live example. `room` and `covers` existed on DeviceResponse and
// were documented, while DeviceProfileResponse carried neither, and the spec agreed
// with the code that it should not — so the two endpoints drifted for several releases
// with nothing failing. Pinning fields rather than paths is what makes that visible.
//
// Deliberately one-directional. The reverse — a documented field the struct does not
// have — is TestSpecDeclaresNoFieldTheServerCannotEmit, kept separate because the two
// failures mean different things and a reviewer reading a test name off a CI log
// should be able to tell them apart.
func TestSpecDocumentsEveryFieldTheServerEmits(t *testing.T) {
	spec := fetchSpec(t)

	for schema, dto := range documentedSchemas {
		declared := specSchemaProperties(t, spec, schema)
		var undocumented []string
		for _, field := range jsonFieldNames(reflect.TypeOf(dto)) {
			if _, ok := declared[field]; !ok {
				undocumented = append(undocumented, field)
			}
		}
		sort.Strings(undocumented)
		if len(undocumented) > 0 {
			t.Errorf("%s emits fields the spec does not declare: %v", schema, undocumented)
		}
	}
}

func TestSpecDeclaresNoFieldTheServerCannotEmit(t *testing.T) {
	spec := fetchSpec(t)

	for schema, dto := range documentedSchemas {
		emitted := make(map[string]struct{})
		for _, f := range jsonFieldNames(reflect.TypeOf(dto)) {
			emitted[f] = struct{}{}
		}
		var phantom []string
		for field := range specSchemaProperties(t, spec, schema) {
			if _, ok := emitted[field]; !ok {
				phantom = append(phantom, field)
			}
		}
		sort.Strings(phantom)
		if len(phantom) > 0 {
			t.Errorf("%s declares fields the server never emits: %v", schema, phantom)
		}
	}
}

// The placement vocabulary is the point of this change, so pin it by name too. The
// coverage tests above would still pass if someone deleted `floor` from both the DTO
// and the spec at once; this says the API is required to answer the question.
func TestBothDeviceSchemasDocumentTheFloorplanVocabulary(t *testing.T) {
	spec := fetchSpec(t)
	for schema := range documentedSchemas {
		declared := specSchemaProperties(t, spec, schema)
		for _, field := range []string{"room", "floor", "covers", "location"} {
			if _, ok := declared[field]; !ok {
				t.Errorf("%s does not document %q", schema, field)
			}
		}
	}
}
