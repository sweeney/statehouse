package mqtt

import (
	"testing"
	"time"
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
