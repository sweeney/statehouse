package influx

import (
	"sync"

	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

// FakeWriteAPI is a hand-written test double that satisfies the local
// pointWriter interface. Tests assert on Points directly rather than
// driving a real InfluxDB.
//
// It is safe for concurrent use.
type FakeWriteAPI struct {
	mu sync.Mutex

	// Points contains every point passed to WritePoint, in order.
	Points []*write.Point

	// Flushed counts how many times Flush() was called.
	Flushed int

	// errCh feeds the writer's error-draining goroutine. Tests rarely
	// need to push into this; it defaults to an immediately-closed
	// channel so the goroutine exits cleanly.
	errCh chan error
}

// NewFakeWriteAPI returns a FakeWriteAPI ready for use. The error
// channel is closed so Writer's drain goroutine doesn't park forever.
func NewFakeWriteAPI() *FakeWriteAPI {
	ch := make(chan error)
	close(ch)
	return &FakeWriteAPI{errCh: ch}
}

// WritePoint records the point.
func (f *FakeWriteAPI) WritePoint(p *write.Point) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Points = append(f.Points, p)
}

// Flush counts a flush call.
func (f *FakeWriteAPI) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Flushed++
}

// Errors returns a closed channel; the writer's drain goroutine exits
// immediately. Tests that want to simulate async errors should
// replace this via InjectErrors.
func (f *FakeWriteAPI) Errors() <-chan error { return f.errCh }

// InjectErrors replaces the error channel with one the test owns. The
// returned send-channel can be used to push simulated async write
// errors. Close it to signal "no more errors".
func (f *FakeWriteAPI) InjectErrors() chan<- error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan error, 8)
	f.errCh = ch
	return ch
}

// PointsForMeasurement returns the recorded points whose Name matches.
func (f *FakeWriteAPI) PointsForMeasurement(name string) []*write.Point {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*write.Point
	for _, p := range f.Points {
		if p.Name() == name {
			out = append(out, p)
		}
	}
	return out
}

// Reset clears recorded state so a single fake can be reused across
// sub-tests.
func (f *FakeWriteAPI) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Points = nil
	f.Flushed = 0
}

// Compile-time assertion: FakeWriteAPI satisfies pointWriter.
var _ pointWriter = (*FakeWriteAPI)(nil)
