package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// siteBlock is the minimum a config needs to pass Validate, so these tests can
// assert on the auth block alone.
const siteBlock = "site:\n  id: home\n  devices_namespace: devices_home\n"

// Omitting the auth block must leave service tokens accepted. The whole point
// of issue #69 is that sibling services cannot call the API at all, and a
// default that needed an opt-in edit on every host would leave them stuck.
func TestServiceTokens_EnabledByDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, siteBlock))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Auth.ServiceTokens.IsEnabled() {
		t.Error("service tokens should be accepted when the auth block is absent")
	}
}

func TestServiceTokens_EnabledFlagRoundTrips(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"absent block", siteBlock, true},
		{"empty auth block", siteBlock + "auth: {}\n", true},
		{"empty service_tokens block", siteBlock + "auth:\n  service_tokens: {}\n", true},
		{"explicit true", siteBlock + "auth:\n  service_tokens:\n    enabled: true\n", true},
		{"explicit false", siteBlock + "auth:\n  service_tokens:\n    enabled: false\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, c.yaml))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := cfg.Auth.ServiceTokens.IsEnabled(); got != c.want {
				t.Errorf("IsEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestServiceTokens_PolicyFieldsParse(t *testing.T) {
	cfg, err := Load(writeConfig(t, siteBlock+`auth:
  service_tokens:
    enabled: true
    required_audience: https://statehouse.swee.net
    required_scope: statehouse:read
    allowed_clients:
      - countinghouse
      - greenhouse
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st := cfg.Auth.ServiceTokens
	if st.RequiredAudience != "https://statehouse.swee.net" {
		t.Errorf("RequiredAudience = %q", st.RequiredAudience)
	}
	if st.RequiredScope != "statehouse:read" {
		t.Errorf("RequiredScope = %q", st.RequiredScope)
	}
	if len(st.AllowedClients) != 2 || st.AllowedClients[0] != "countinghouse" {
		t.Errorf("AllowedClients = %v", st.AllowedClients)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() rejected a well-formed auth block: %v", err)
	}
}

// identity/common joins a multi-valued `aud` with spaces before handing it
// back, so an audience containing a space can never be matched unambiguously.
// Better to refuse the config than to silently lock every service out.
func TestValidate_RejectsAudienceContainingSpace(t *testing.T) {
	cfg, err := Load(writeConfig(t, siteBlock+"auth:\n  service_tokens:\n    required_audience: \"one two\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted an audience containing a space")
	}
	if !strings.Contains(err.Error(), "required_audience") {
		t.Errorf("error should name the field, got %v", err)
	}
}

// Scope is one scope token, matched whole against the space-delimited list in
// the token. "a b" would match nothing.
func TestValidate_RejectsScopeContainingSpace(t *testing.T) {
	cfg, err := Load(writeConfig(t, siteBlock+"auth:\n  service_tokens:\n    required_scope: \"read write\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted a scope containing a space")
	}
	if !strings.Contains(err.Error(), "required_scope") {
		t.Errorf("error should name the field, got %v", err)
	}
}

// An empty entry in the allowlist matches no client_id but does turn the
// allowlist on, which reads as "allow nothing" — almost certainly a typo.
func TestValidate_RejectsEmptyAllowedClient(t *testing.T) {
	cfg, err := Load(writeConfig(t, siteBlock+"auth:\n  service_tokens:\n    allowed_clients:\n      - countinghouse\n      - \"\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an empty allowed_clients entry")
	}
}

func TestValidate_AcceptsAbsentAuthBlock(t *testing.T) {
	cfg, err := Load(writeConfig(t, siteBlock))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// The shipped example is what an operator copies, so it must both document the
// auth block and still describe a startable service.
//
// It is parsed directly rather than through Load, because Load also reads
// influx.token_file — a path that exists on the deployed host and nowhere else.
func TestExampleConfigDocumentsServiceTokens(t *testing.T) {
	raw, err := os.ReadFile("../../config/config.example.yaml")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse example config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("example config does not validate: %v", err)
	}
	if !cfg.Auth.ServiceTokens.IsEnabled() {
		t.Error("example config should leave service tokens enabled")
	}
	if !strings.Contains(string(raw), "service_tokens:") {
		t.Error("example config should document the service_tokens block")
	}
}
