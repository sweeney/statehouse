package energy

import "time"

// Integrator computes power-time integration in kWh. It applies an
// integration-gap clamp: if more than maxGap elapses between two
// readings, the gap is *not* assumed to hold the previous wattage.
// A clamp event is recorded so callers can surface it.
type Integrator struct {
	maxGap time.Duration

	lastWatts *float64
	lastAt    time.Time
	total     float64 // kWh
	gaps      int     // number of times a gap was clamped
}

// NewIntegrator constructs an Integrator that refuses to integrate
// across any gap exceeding maxGap.
func NewIntegrator(maxGap time.Duration) *Integrator {
	return &Integrator{maxGap: maxGap}
}

// Update records a new (timestamp, watts) reading and accumulates
// energy using the trapezoid rule over the interval since the
// previous reading. If the interval exceeds maxGap, the interval is
// skipped and a gap is counted.
func (i *Integrator) Update(at time.Time, watts float64) {
	if i.lastWatts == nil {
		w := watts
		i.lastWatts = &w
		i.lastAt = at
		return
	}
	dt := at.Sub(i.lastAt)
	if dt <= 0 {
		// Out-of-order or duplicate timestamp; ignore the interval
		// but keep the most recent value.
		w := watts
		i.lastWatts = &w
		i.lastAt = at
		return
	}
	if i.maxGap > 0 && dt > i.maxGap {
		i.gaps++
		w := watts
		i.lastWatts = &w
		i.lastAt = at
		return
	}
	avgW := (*i.lastWatts + watts) / 2
	// kWh = W * hours / 1000.
	i.total += avgW * dt.Hours() / 1000
	w := watts
	i.lastWatts = &w
	i.lastAt = at
}

// Total returns the accumulated kWh.
func (i *Integrator) Total() float64 { return i.total }

// GapsClamped returns the number of intervals that were skipped due
// to exceeding the configured maxGap.
func (i *Integrator) GapsClamped() int { return i.gaps }

// Reset clears the integrator state.
func (i *Integrator) Reset() {
	i.lastWatts = nil
	i.lastAt = time.Time{}
	i.total = 0
	i.gaps = 0
}

// Snapshot captures the integrator total + gap counter at a point in
// time so callers can compute deltas without resetting the underlying
// integrator. Cycles use this so the integrator can keep clamping
// gaps that span the cycle boundary.
type Snapshot struct {
	Total       float64
	GapsClamped int
}

// Snapshot returns the current cumulative state.
func (i *Integrator) SnapshotState() Snapshot {
	return Snapshot{Total: i.total, GapsClamped: i.gaps}
}
