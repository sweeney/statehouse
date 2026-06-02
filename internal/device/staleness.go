package device

// StalenessSecondsForClass returns the staleness threshold in seconds for class.
// override, when non-nil, takes precedence over the class default.
func StalenessSecondsForClass(class string, override *int) float64 {
	if override != nil {
		return float64(*override)
	}
	switch class {
	case ClassShortBurst, ClassCyclePower, ClassContinuous, ClassMedia:
		return 900
	case ClassBinaryState:
		return 3600
	default:
		return 3600
	}
}
