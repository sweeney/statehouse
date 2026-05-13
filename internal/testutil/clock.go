package testutil

import (
	"sync"
	"time"
)

// Clock is the abstraction used by state machines so that tests can
// run deterministically without sleeping.
type Clock interface {
	Now() time.Time
}

// RealClock is a production clock backed by time.Now.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

// FakeClock is a settable clock for tests.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a FakeClock at the given start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{t: start.UTC()}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Set advances or rewinds the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t.UTC()
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
