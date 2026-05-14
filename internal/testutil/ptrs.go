package testutil

import "time"

// PtrF64 returns a pointer to the given float64 value. Useful for
// constructing config.Thresholds literals in tests.
func PtrF64(v float64) *float64 { return &v }

// PtrDur returns a pointer to the given time.Duration value. Useful for
// constructing config.Thresholds literals in tests.
func PtrDur(v time.Duration) *time.Duration { return &v }
