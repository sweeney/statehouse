package energy

// Strategy describes which path (counter/integration) is the primary
// source of truth for a device class. It lives here, not in the
// device package, so the energy package never depends on device.
type Strategy string

const (
	StrategyCounter     Strategy = "counter"
	StrategyIntegration Strategy = "integration"
)

// SelectKWh picks the appropriate energy total for a session, given
// the strategy hint and the two parallel measurements.
//
// Rules:
//   - StrategyCounter: prefer the reported counter delta. Fall back
//     to integration if counter is exactly zero (the counter never
//     moved during the session) and integration is non-zero.
//   - StrategyIntegration: prefer integration.
//   - Anything else: prefer the larger of the two values, which tends
//     to surface a useful number when one path is silent.
func SelectKWh(strategy Strategy, counterKWh, integratedKWh float64) (float64, string) {
	switch strategy {
	case StrategyCounter:
		if counterKWh > 0 {
			return counterKWh, "counter"
		}
		if integratedKWh > 0 {
			return integratedKWh, "integration"
		}
		return 0, "counter"
	case StrategyIntegration:
		if integratedKWh > 0 {
			return integratedKWh, "integration"
		}
		if counterKWh > 0 {
			return counterKWh, "counter"
		}
		return 0, "integration"
	}
	if counterKWh >= integratedKWh {
		return counterKWh, "counter"
	}
	return integratedKWh, "integration"
}
