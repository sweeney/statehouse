package energy

// Counter tracks a monotonic energy counter and yields its delta over
// a session. It tolerates the device reporting a smaller value than
// before (e.g. plug reset) by treating the new value as a new
// baseline and not adding negative deltas.
type Counter struct {
	baseline *float64
	latest   *float64
}

// SetBaseline marks the start of a new session at the given counter
// value.
func (c *Counter) SetBaseline(v float64) {
	val := v
	c.baseline = &val
	c.latest = &val
}

// Update records the latest counter reading. If the reading is below
// the baseline (counter reset), the baseline is rolled forward.
func (c *Counter) Update(v float64) {
	val := v
	if c.baseline == nil {
		c.baseline = &val
	}
	if v < *c.baseline {
		c.baseline = &val
	}
	c.latest = &val
}

// Delta returns the energy consumed since the baseline was set, or
// zero if no baseline has been recorded.
func (c *Counter) Delta() float64 {
	if c.baseline == nil || c.latest == nil {
		return 0
	}
	d := *c.latest - *c.baseline
	if d < 0 {
		return 0
	}
	return d
}

// Reset clears the counter completely.
func (c *Counter) Reset() {
	c.baseline = nil
	c.latest = nil
}

// RebaselineAtLatest moves the baseline to the current latest reading
// without losing the latest value. This is used at cycle start: the
// counter total at the moment activity began becomes the new zero.
func (c *Counter) RebaselineAtLatest() {
	if c.latest == nil {
		return
	}
	v := *c.latest
	c.baseline = &v
}

// Latest returns the most recent counter reading and whether one has
// been observed.
func (c *Counter) Latest() (float64, bool) {
	if c.latest == nil {
		return 0, false
	}
	return *c.latest, true
}
