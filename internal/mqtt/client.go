package mqtt

import (
	"errors"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Handler is invoked for each MQTT message that matches the engine's
// subscription set.
type Handler func(topic string, payload []byte, retained bool)

// Client is a thin wrapper around paho.Client that exposes the
// subset of the API the engine needs and is easy to fake in tests.
type Client interface {
	Connect() error
	Disconnect()
	Subscribe(topic string, qos byte, handler Handler) error
	Publish(topic string, qos byte, retained bool, payload []byte) error
	IsConnected() bool
}

// Config describes how to connect to the MQTT broker.
type Config struct {
	Broker   string
	ClientID string
	Username string
	Password string
}

// pahoClient implements Client backed by github.com/eclipse/paho.mqtt.golang.
type pahoClient struct {
	cfg Config
	mu  sync.Mutex
	c   paho.Client
}

// New creates a Client that connects to the given broker.
func New(cfg Config) Client {
	return &pahoClient{cfg: cfg}
}

func (p *pahoClient) Connect() error {
	opts := buildClientOptions(p.cfg)
	c := paho.NewClient(opts)
	// Store the client immediately. paho will retry the connection in
	// the background. We bound the initial wait so callers don't block
	// when the broker is unreachable at startup.
	p.mu.Lock()
	p.c = c
	p.mu.Unlock()
	tok := c.Connect()
	if !tok.WaitTimeout(2 * time.Second) {
		return errors.New("mqtt connect timeout; retrying in background")
	}
	return tok.Error()
}

// buildClientOptions returns the paho ClientOptions used by Connect().
// Extracted so unit tests can assert on broker, retry, keepalive and
// auth settings without opening a network connection.
func buildClientOptions(cfg Config) *paho.ClientOptions {
	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetCleanSession(true).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetOrderMatters(false)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	return opts
}

func (p *pahoClient) Disconnect() {
	p.mu.Lock()
	c := p.c
	p.mu.Unlock()
	if c != nil {
		c.Disconnect(250)
	}
}

func (p *pahoClient) Subscribe(topic string, qos byte, handler Handler) error {
	p.mu.Lock()
	c := p.c
	p.mu.Unlock()
	if c == nil {
		return errors.New("mqtt not connected")
	}
	tok := c.Subscribe(topic, qos, func(_ paho.Client, m paho.Message) {
		handler(m.Topic(), m.Payload(), m.Retained())
	})
	if !tok.WaitTimeout(2 * time.Second) {
		return errors.New("mqtt subscribe timed out; will retry on reconnect")
	}
	return tok.Error()
}

func (p *pahoClient) Publish(topic string, qos byte, retained bool, payload []byte) error {
	p.mu.Lock()
	c := p.c
	p.mu.Unlock()
	if c == nil {
		return errors.New("mqtt not connected")
	}
	tok := c.Publish(topic, qos, retained, payload)
	tok.Wait()
	return tok.Error()
}

func (p *pahoClient) IsConnected() bool {
	p.mu.Lock()
	c := p.c
	p.mu.Unlock()
	return c != nil && c.IsConnected()
}
