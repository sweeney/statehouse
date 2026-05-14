package validate

import (
	"math"
	"testing"
)

func TestFiniteInRange(t *testing.T) {
	tests := []struct {
		name string
		v    float64
		lo   float64
		hi   float64
		want bool
	}{
		// NaN and Inf rejection
		{"NaN", math.NaN(), 0, 100, false},
		{"PosInf", math.Inf(1), 0, 100, false},
		{"NegInf", math.Inf(-1), 0, 100, false},
		// Inclusive bounds
		{"at lo", 0, 0, 100, true},
		{"at hi", 100, 0, 100, true},
		{"below lo", -0.001, 0, 100, false},
		{"above hi", 100.001, 0, 100, false},
		// Mid-range
		{"mid", 50, 0, 100, true},
		// Negative ranges
		{"neg range mid", -30, -50, 0, true},
		{"neg range lo", -50, -50, 0, true},
		{"neg range hi", 0, -50, 0, true},
		{"neg range out", -51, -50, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FiniteInRange(tt.v, tt.lo, tt.hi)
			if got != tt.want {
				t.Errorf("FiniteInRange(%v, %v, %v) = %v, want %v", tt.v, tt.lo, tt.hi, got, tt.want)
			}
		})
	}
}

func TestIdentifier(t *testing.T) {
	valid := []string{
		"home",
		"a",
		"sensor_1",
		"my-device",
		"ABC123",
		"a_b-c",
		// 64 chars — at the limit
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for _, s := range valid {
		if !Identifier(s) {
			t.Errorf("Identifier(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		// 65 chars — over the limit
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"has space",
		"has.dot",
		"has/slash",
		"has@at",
	}
	for _, s := range invalid {
		if Identifier(s) {
			t.Errorf("Identifier(%q) = true, want false", s)
		}
	}
}

func TestHexIdentifier(t *testing.T) {
	valid := []string{
		"001122AABBCC",
		"aabbcc",
		"0123456789abcdefABCDEF01",
		// 6 chars — at minimum
		"ABCDEF",
		// 32 chars — at maximum
		"00112233445566778899AABBCCDDEEFF",
	}
	for _, s := range valid {
		if !HexIdentifier(s) {
			t.Errorf("HexIdentifier(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		// 5 chars — below minimum
		"ABCDE",
		// 33 chars — above maximum
		"00112233445566778899AABBCCDDEEFF0",
		"GGGGGG",
		"ZZZzzz",
		"not-hex",
		"123 456",
	}
	for _, s := range invalid {
		if HexIdentifier(s) {
			t.Errorf("HexIdentifier(%q) = true, want false", s)
		}
	}
}
