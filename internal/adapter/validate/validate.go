package validate

import (
	"math"
	"regexp"
)

// FiniteInRange returns true when v is a finite number in [lo, hi].
func FiniteInRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi
}

var reIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Identifier reports whether s is a valid adapter device identifier.
func Identifier(s string) bool { return reIdentifier.MatchString(s) }

var reHexSerial = regexp.MustCompile(`^[0-9A-Fa-f]{6,32}$`)

// HexIdentifier reports whether s is a valid hex serial (e.g. meter MAC).
func HexIdentifier(s string) bool { return reHexSerial.MatchString(s) }
