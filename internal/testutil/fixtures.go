package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// FixtureEvent is one line from a fixture JSONL file: a timestamped
// MQTT topic/payload pair. Payload is kept as raw JSON because some
// payloads are strings (e.g. "online") and others are objects/arrays.
type FixtureEvent struct {
	Timestamp time.Time       `json:"ts"`
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
}

// PayloadBytes returns the payload as raw bytes suitable for the MQTT
// subscriber. JSON string payloads (e.g. "online") are unquoted so
// they look like the wire format Z2M actually sends.
func (e FixtureEvent) PayloadBytes() []byte {
	if len(e.Payload) > 0 && e.Payload[0] == '"' {
		var s string
		if err := json.Unmarshal(e.Payload, &s); err == nil {
			return []byte(s)
		}
	}
	return []byte(e.Payload)
}

// LoadFixture reads a JSONL fixture from disk.
func LoadFixture(path string) ([]FixtureEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fixture: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var out []FixtureEvent
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev FixtureEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("parse fixture line: %w", err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
