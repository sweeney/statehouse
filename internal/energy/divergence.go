package energy

import "math"

// DivergencePct returns the absolute percentage difference between
// counter-reported and integrated kWh, normalised by the larger of
// the two. Returns 0 when both inputs are 0.
func DivergencePct(counterKWh, integratedKWh float64) float64 {
	a := math.Abs(counterKWh)
	b := math.Abs(integratedKWh)
	denom := math.Max(a, b)
	if denom == 0 {
		return 0
	}
	return math.Abs(a-b) / denom * 100
}
