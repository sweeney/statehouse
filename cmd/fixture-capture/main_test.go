package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFormatPayload_JSONObjectPassthrough(t *testing.T) {
	in := []byte(`{"state":"ON","power":1850}`)
	got := formatPayload(in)
	if string(got) != string(in) {
		t.Fatalf("expected passthrough of valid JSON object, got %s", got)
	}
}

func TestFormatPayload_JSONArrayPassthrough(t *testing.T) {
	in := []byte(`[{"ieee_address":"0xabc"}]`)
	got := formatPayload(in)
	if string(got) != string(in) {
		t.Fatalf("expected passthrough of valid JSON array, got %s", got)
	}
}

func TestFormatPayload_NumberPassthrough(t *testing.T) {
	// Bare JSON numbers are valid JSON values too; preserve them.
	in := []byte("42.5")
	got := formatPayload(in)
	if string(got) != string(in) {
		t.Fatalf("expected JSON number passthrough, got %s", got)
	}
}

func TestFormatPayload_PlainStringIsQuoted(t *testing.T) {
	// Z2M availability often arrives as a bare "online"/"offline"
	// string with no quotes. It's not valid JSON; we wrap it.
	got := formatPayload([]byte("online"))
	if string(got) != `"online"` {
		t.Fatalf(`expected "online" (JSON-quoted), got %s`, got)
	}
}

func TestFormatPayload_EmptyIsEmptyString(t *testing.T) {
	got := formatPayload(nil)
	if string(got) != `""` {
		t.Fatalf("expected empty JSON string, got %s", got)
	}
}

func TestFormatPayload_QuotedJSONStringIsPreserved(t *testing.T) {
	// If something sends a JSON-encoded string (already quoted), we
	// preserve it as-is rather than double-quoting.
	got := formatPayload([]byte(`"online"`))
	if string(got) != `"online"` {
		t.Fatalf(`expected preserved JSON-quoted string, got %s`, got)
	}
}

func TestSplitTopics(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"zigbee2mqtt/#", []string{"zigbee2mqtt/#"}},
		{"a/b, c/d ,e/#", []string{"a/b", "c/d", "e/#"}},
		{"", nil},
		{" ,, ", nil},
	}
	for _, c := range cases {
		got := splitTopics(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitTopics(%q) len = %d want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitTopics(%q)[%d] = %q want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestLineWriter_WritesOneLineJSONAndFlushesEach(t *testing.T) {
	var buf bytes.Buffer
	lw := newLineWriter(&buf, nil)
	rec := record{
		Timestamp: time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC),
		Topic:     "zigbee2mqtt/kettle",
		Payload:   json.RawMessage(`{"power":2000}`),
	}
	if err := lw.write(rec); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lw.write(rec); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := buf.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), got)
	}
	for i, line := range lines {
		var parsed record
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", i, err, line)
		}
		if parsed.Topic != "zigbee2mqtt/kettle" {
			t.Errorf("line %d topic = %q", i, parsed.Topic)
		}
	}
}

func TestLineWriter_OutputMatchesFixtureShape(t *testing.T) {
	// The captured record must be deserializable by the same fixture
	// loader the tests use. Verify by encoding then decoding through
	// the testutil shape.
	var buf bytes.Buffer
	lw := newLineWriter(&buf, nil)
	_ = lw.write(record{
		Timestamp: time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC),
		Topic:     "zigbee2mqtt/x",
		Payload:   formatPayload([]byte(`{"power":2000}`)),
	})

	// Decode using the same struct shape the testutil loader uses.
	type fixtureLine struct {
		Timestamp time.Time       `json:"ts"`
		Topic     string          `json:"topic"`
		Payload   json.RawMessage `json:"payload"`
	}
	var f fixtureLine
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &f); err != nil {
		t.Fatalf("captured line not parseable as fixture: %v (%q)", err, buf.String())
	}
	if f.Topic != "zigbee2mqtt/x" {
		t.Fatalf("topic wrong: %q", f.Topic)
	}
	if string(f.Payload) != `{"power":2000}` {
		t.Fatalf("payload wrong: %q", f.Payload)
	}
}
