package meter

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// flexFloat decodes an optional float64 that may arrive as a JSON number,
// a quoted numeric string ("8.013"), or null. Any other shape — object,
// array, bool, or a non-numeric string — decodes to absent (nil) WITHOUT
// returning an error.
//
// This is the adapter's tolerance to MQTT payload format drift: a single
// field changing type degrades only that field instead of failing the
// whole json.Unmarshal and dropping the entire reading. UnmarshalJSON is
// only invoked when the key is present, so an absent field stays nil.
type flexFloat struct{ v *float64 }

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	// Quoted value: a firmware update that starts emitting numbers as
	// strings should still parse. Unwrap the string, then parse its body.
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil // malformed string token; degrade to absent
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			f.v = &n
		}
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err == nil {
		f.v = &n
	}
	// Non-numeric shapes (object/array/bool) fall through as absent.
	return nil
}
