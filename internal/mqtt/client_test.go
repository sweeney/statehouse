package mqtt

import (
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// These tests exercise the paho ClientOptions construction without
// opening a network connection. They protect against accidental
// regressions in keep-alive, retry, and auth settings.

func TestBuildClientOptions_Broker(t *testing.T) {
	opts := buildClientOptions(Config{Broker: "tcp://broker.local:1883", ClientID: "test"})
	servers := opts.Servers
	if len(servers) != 1 || servers[0].String() != "tcp://broker.local:1883" {
		t.Fatalf("expected single broker tcp://broker.local:1883, got %v", servers)
	}
}

func TestBuildClientOptions_ClientID(t *testing.T) {
	opts := buildClientOptions(Config{Broker: "tcp://localhost:1883", ClientID: "house-state-engine"})
	if opts.ClientID != "house-state-engine" {
		t.Fatalf("ClientID: got %q want house-state-engine", opts.ClientID)
	}
}

func TestBuildClientOptions_RetrySettings(t *testing.T) {
	opts := buildClientOptions(Config{Broker: "tcp://localhost:1883", ClientID: "x"})
	if !opts.AutoReconnect {
		t.Error("AutoReconnect must be true so the engine recovers after broker outages")
	}
	if !opts.ConnectRetry {
		t.Error("ConnectRetry must be true so startup tolerates an unreachable broker")
	}
	if opts.ConnectRetryInterval != 5*time.Second {
		t.Errorf("ConnectRetryInterval: got %v want 5s", opts.ConnectRetryInterval)
	}
}

func TestBuildClientOptions_Keepalive(t *testing.T) {
	opts := buildClientOptions(Config{Broker: "tcp://localhost:1883", ClientID: "x"})
	if opts.KeepAlive != 30 {
		// paho stores KeepAlive as int64 seconds, not a Duration.
		t.Errorf("KeepAlive: got %v want 30 (seconds)", opts.KeepAlive)
	}
	if opts.PingTimeout != 10*time.Second {
		t.Errorf("PingTimeout: got %v want 10s", opts.PingTimeout)
	}
}

func TestBuildClientOptions_CleanSessionOn(t *testing.T) {
	opts := buildClientOptions(Config{Broker: "tcp://localhost:1883", ClientID: "x"})
	// CleanSession=true means we don't carry queued messages across
	// engine restarts; the engine rebuilds state from retained topics.
	if !opts.CleanSession {
		t.Error("CleanSession must be true")
	}
}

func TestBuildClientOptions_NoAuthByDefault(t *testing.T) {
	opts := buildClientOptions(Config{Broker: "tcp://localhost:1883", ClientID: "x"})
	if opts.Username != "" {
		t.Errorf("Username should be unset by default, got %q", opts.Username)
	}
	if opts.Password != "" {
		t.Errorf("Password should be unset by default, got %q", opts.Password)
	}
}

func TestBuildClientOptions_AuthSetWhenProvided(t *testing.T) {
	opts := buildClientOptions(Config{
		Broker: "tcp://localhost:1883", ClientID: "x",
		Username: "user", Password: "pw",
	})
	if opts.Username != "user" || opts.Password != "pw" {
		t.Errorf("auth not applied: user=%q pw=%q", opts.Username, opts.Password)
	}
}

// TestSubscribeRecordsInSubsSlice verifies that Subscribe() appends an entry
// to the pahoClient.subs slice so that resubscribe() can replay it later.
// It exercises the recording path without opening a network connection by
// using a stub paho.Client.
func TestSubscribeRecordsInSubsSlice(t *testing.T) {
	p := &pahoClient{c: &stubPahoClient{}}

	handler := func(topic string, payload []byte, retained bool) {}
	if err := p.Subscribe("home/+/temperature", 1, handler); err != nil {
		t.Fatalf("Subscribe returned unexpected error: %v", err)
	}

	p.subsMu.Lock()
	n := len(p.subs)
	sub := p.subs[0]
	p.subsMu.Unlock()

	if n != 1 {
		t.Fatalf("expected 1 recorded subscription, got %d", n)
	}
	if sub.topic != "home/+/temperature" {
		t.Errorf("recorded topic: got %q want home/+/temperature", sub.topic)
	}
	if sub.qos != 1 {
		t.Errorf("recorded qos: got %d want 1", sub.qos)
	}
}

// TestResubscribeReplaysAllTopics verifies that resubscribe() calls
// paho Subscribe for every entry in the subs slice.  This is the
// invariant that guarantees topics survive a broker reconnect.
func TestResubscribeReplaysAllTopics(t *testing.T) {
	stub := &stubPahoClient{}
	p := &pahoClient{c: stub}

	h := func(string, []byte, bool) {}
	_ = p.Subscribe("a/b", 0, h)
	_ = p.Subscribe("c/d", 1, h)

	// Reset the stub's call log to simulate a fresh connection.
	stub.mu.Lock()
	stub.subscribed = nil
	stub.mu.Unlock()

	p.resubscribe(stub)

	stub.mu.Lock()
	got := append([]string(nil), stub.subscribed...)
	stub.mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("resubscribe: expected 2 subscribe calls, got %d (%v)", len(got), got)
	}
	want := map[string]bool{"a/b": true, "c/d": true}
	for _, topic := range got {
		if !want[topic] {
			t.Errorf("unexpected topic in resubscribe: %q", topic)
		}
	}
}

// stubPahoClient is a minimal paho.Client that records Subscribe calls
// and returns immediately-complete tokens.  Only the methods exercised
// by pahoClient are implemented; the rest panic so accidental use is
// obvious.
type stubPahoClient struct {
	mu         sync.Mutex
	subscribed []string
}

func (s *stubPahoClient) Subscribe(topic string, qos byte, _ paho.MessageHandler) paho.Token {
	s.mu.Lock()
	s.subscribed = append(s.subscribed, topic)
	s.mu.Unlock()
	return &doneToken{}
}

func (s *stubPahoClient) IsConnected() bool       { return true }
func (s *stubPahoClient) IsConnectionOpen() bool  { return true }
func (s *stubPahoClient) Connect() paho.Token     { return &doneToken{} }
func (s *stubPahoClient) Disconnect(quiesce uint) {}
func (s *stubPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) paho.Token {
	return &doneToken{}
}
func (s *stubPahoClient) SubscribeMultiple(filters map[string]byte, callback paho.MessageHandler) paho.Token {
	return &doneToken{}
}
func (s *stubPahoClient) Unsubscribe(topics ...string) paho.Token             { return &doneToken{} }
func (s *stubPahoClient) AddRoute(topic string, callback paho.MessageHandler) {}
func (s *stubPahoClient) OptionsReader() paho.ClientOptionsReader {
	return paho.NewClient(paho.NewClientOptions()).OptionsReader()
}

// doneToken is a paho.Token that is immediately complete with no error.
type doneToken struct{}

func (d *doneToken) Wait() bool                     { return true }
func (d *doneToken) WaitTimeout(time.Duration) bool { return true }
func (d *doneToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (d *doneToken) Error() error { return nil }
