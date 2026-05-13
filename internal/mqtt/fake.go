package mqtt

import (
	"sync"
)

// Published is one record captured by FakeClient.Publish. Tests assert
// on these instead of going through a real broker.
type Published struct {
	Topic    string
	QoS      byte
	Retained bool
	Payload  []byte
}

// Subscription is one record captured by FakeClient.Subscribe.
type Subscription struct {
	Topic   string
	QoS     byte
	Handler Handler
}

// FakeClient is a hand-written Client used in tests. It records every
// subscription and publish, lets the test trigger inbound messages via
// Deliver, and lets the test inject errors via the *Err fields.
//
// It's intentionally simple: tests inspect the recorded slices
// directly, no expectation DSL.
type FakeClient struct {
	mu sync.Mutex

	Published     []Published
	Subscriptions []Subscription

	// Connected controls IsConnected. Defaults to true so tests don't
	// have to set it for the common happy path.
	Connected bool

	// ConnectErr, SubscribeErr, PublishErr (if set) are returned by the
	// corresponding methods. Tests use these to exercise failure paths
	// without needing a flaky real broker.
	ConnectErr   error
	SubscribeErr error
	PublishErr   error

	// Disconnected records whether Disconnect() has been called.
	Disconnected bool
}

// NewFakeClient returns a FakeClient that reports as connected.
func NewFakeClient() *FakeClient {
	return &FakeClient{Connected: true}
}

func (f *FakeClient) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ConnectErr != nil {
		return f.ConnectErr
	}
	f.Connected = true
	return nil
}

func (f *FakeClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Disconnected = true
	f.Connected = false
}

func (f *FakeClient) Subscribe(topic string, qos byte, handler Handler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SubscribeErr != nil {
		return f.SubscribeErr
	}
	f.Subscriptions = append(f.Subscriptions, Subscription{Topic: topic, QoS: qos, Handler: handler})
	return nil
}

func (f *FakeClient) Publish(topic string, qos byte, retained bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PublishErr != nil {
		return f.PublishErr
	}
	// Defensively copy the payload — callers may reuse buffers.
	dup := make([]byte, len(payload))
	copy(dup, payload)
	f.Published = append(f.Published, Published{Topic: topic, QoS: qos, Retained: retained, Payload: dup})
	return nil
}

func (f *FakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Connected
}

// Deliver invokes every subscribed handler whose topic filter matches
// the given topic. Wildcards "+" (single segment) and "#" (multi
// segment, tail-only) are supported — same MQTT semantics as a real
// broker.
func (f *FakeClient) Deliver(topic string, payload []byte, retained bool) int {
	f.mu.Lock()
	subs := append([]Subscription(nil), f.Subscriptions...)
	f.mu.Unlock()
	matched := 0
	for _, s := range subs {
		if topicMatches(s.Topic, topic) {
			s.Handler(topic, payload, retained)
			matched++
		}
	}
	return matched
}

// PublishedOn returns every recorded Published whose Topic equals the
// given topic exactly. Useful in assertions.
func (f *FakeClient) PublishedOn(topic string) []Published {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Published
	for _, p := range f.Published {
		if p.Topic == topic {
			out = append(out, p)
		}
	}
	return out
}

// Reset clears all recorded state and injected errors so a single
// FakeClient can be reused across sub-tests.
func (f *FakeClient) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Published = nil
	f.Subscriptions = nil
	f.Connected = true
	f.ConnectErr = nil
	f.SubscribeErr = nil
	f.PublishErr = nil
	f.Disconnected = false
}

// topicMatches implements standard MQTT topic-filter matching.
func topicMatches(filter, topic string) bool {
	fp, tp := 0, 0
	fSeg, tSeg := segments(filter), segments(topic)
	for fp < len(fSeg) && tp < len(tSeg) {
		switch fSeg[fp] {
		case "#":
			return true
		case "+":
			// matches one segment
		default:
			if fSeg[fp] != tSeg[tp] {
				return false
			}
		}
		fp++
		tp++
	}
	// "#" left over at the end with no topic remaining still matches.
	if fp < len(fSeg) && fSeg[fp] == "#" {
		return true
	}
	return fp == len(fSeg) && tp == len(tSeg)
}

func segments(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// FaultClient wraps a Client and injects publish failures for a
// configured range of calls. It exists so the engine's resilience to
// publish errors can be tested deterministically without manipulating
// the FakeClient's PublishErr in-flight.
type FaultClient struct {
	Inner    Client
	mu       sync.Mutex
	calls    int
	FaultStart int  // first publish call index that fails (inclusive)
	FaultEnd   int  // last publish call index that fails (exclusive)
	FaultErr   error
}

func (f *FaultClient) Connect() error      { return f.Inner.Connect() }
func (f *FaultClient) Disconnect()         { f.Inner.Disconnect() }
func (f *FaultClient) IsConnected() bool   { return f.Inner.IsConnected() }
func (f *FaultClient) Subscribe(topic string, qos byte, h Handler) error {
	return f.Inner.Subscribe(topic, qos, h)
}
func (f *FaultClient) Publish(topic string, qos byte, retained bool, payload []byte) error {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.mu.Unlock()
	if i >= f.FaultStart && i < f.FaultEnd {
		if f.FaultErr != nil {
			return f.FaultErr
		}
		return errClientFault
	}
	return f.Inner.Publish(topic, qos, retained, payload)
}

// errClientFault is the default error FaultClient returns when no
// custom FaultErr is set.
var errClientFault = &simpleErr{"injected mqtt publish fault"}

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// Compile-time assertion: FakeClient and FaultClient satisfy Client.
var (
	_ Client = (*FakeClient)(nil)
	_ Client = (*FaultClient)(nil)
)
