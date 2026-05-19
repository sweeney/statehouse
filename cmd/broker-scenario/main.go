// Command broker-scenario publishes a controlled appliance scenario against
// a running MQTT broker and captures all traffic to a JSONL fixture file.
//
// You manage the broker yourself. The tool connects to it, wires up an
// in-process engine, and publishes a dishwasher cycle. When the broker drops
// it annotates the fixture with a _capture/connection_lost marker; when it
// comes back it annotates with _capture/reconnected and publishes the Z2M
// retained-message replay.
//
// Typical workflow:
//
//  1. Start mosquitto:   mosquitto -p 11883
//  2. Run this tool:     broker-scenario -broker tcp://localhost:11883
//  3. Watch the output.  When prompted, kill the broker and restart it.
//  4. The tool finishes and writes the JSONL fixture.
//
// Usage:
//
//	broker-scenario [-broker addr] [-output path]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/sweeney/statehouse/internal/adapter/zigbee2mqtt"
	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/mqtt"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func main() {
	brokerAddr := flag.String("broker", "tcp://localhost:11883", "MQTT broker address")
	outPath := flag.String("output", "broker_reconnect_captured.jsonl", "JSONL output path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	sniff, err := newSniffer(*outPath)
	if err != nil {
		logger.Error("open output", "error", err)
		os.Exit(1)
	}
	defer sniff.close()

	reconnected := make(chan struct{}, 1)
	sniff.connectAndWatch(*brokerAddr, "broker-scenario-sniffer", reconnected)

	// Engine in-process.
	cfg := scenarioCfg(*brokerAddr)
	store := state.NewStore()
	engine := state.NewEngine(cfg, store, testutil.RealClock{})
	engine.AddDerivedSink(&printSink{log: logger})

	z2m := zigbee2mqtt.New(engine, "zigbee2mqtt", nil)
	engineClient := mqtt.New(mqtt.Config{Broker: *brokerAddr, ClientID: "broker-scenario-engine"})
	if err := engineClient.Connect(); err != nil {
		logger.Warn("engine connect", "error", err)
	}
	defer engineClient.Disconnect()
	if err := engineClient.Subscribe("zigbee2mqtt/#", 0, z2m.HandleMessage); err != nil {
		logger.Warn("engine subscribe", "error", err)
	}

	pub := mustConnect(*brokerAddr, "broker-scenario-pub")
	defer pub.Disconnect(0)

	// Phase 1: Z2M startup + dishwasher coming on.
	logger.Info("=== phase 1: initial messages ===")
	mustPublish(pub, "zigbee2mqtt/bridge/devices", bridgeDevicesJSON, true)
	sleep(200)
	mustPublish(pub, "zigbee2mqtt/kitchen_dishwasher", `{"power":0.5,"energy":100.000,"linkquality":90}`, true)
	sleep(600)
	mustPublish(pub, "zigbee2mqtt/kitchen_dishwasher", `{"power":1850,"energy":100.000,"linkquality":90}`, false)
	sleep(600)
	mustPublish(pub, "zigbee2mqtt/kitchen_dishwasher", `{"power":1820,"energy":100.000,"linkquality":89}`, false)
	sleep(4000) // wait for ActiveSustainedFor (3 s)
	mustPublish(pub, "zigbee2mqtt/kitchen_dishwasher", `{"power":1500,"energy":100.000,"linkquality":89}`, false)
	sleep(500)

	// Prompt the user to kill the broker.
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, ">>> Kill the broker now, then restart it.")
	fmt.Fprintln(os.Stderr, ">>> Waiting for reconnect...")
	fmt.Fprintln(os.Stderr, "")

	// Wait until paho detects the reconnect.
	select {
	case <-reconnected:
		logger.Info("reconnect detected — continuing scenario")
	case <-time.After(5 * time.Minute):
		logger.Error("timed out waiting for reconnect")
		os.Exit(1)
	}
	sleep(500) // let the engine's own paho client resubscribe

	// Phase 2: simulate Z2M's retained-message replay after reconnect.
	// Z2M republishes its retained topics when it reconnects to the broker;
	// since the broker restarted without persistence we replicate that here.
	logger.Info("=== phase 2: z2m retained replay ===")
	pub2 := mustConnect(*brokerAddr, "broker-scenario-pub2")
	defer pub2.Disconnect(0)
	mustPublish(pub2, "zigbee2mqtt/bridge/devices", bridgeDevicesJSON, true)
	sleep(300)
	mustPublish(pub2, "zigbee2mqtt/kitchen_dishwasher/availability", "online", true)
	sleep(300)
	mustPublish(pub2, "zigbee2mqtt/kitchen_dishwasher", `{"power":1500,"energy":100.000,"linkquality":89}`, true)
	sleep(1000)

	// Phase 3: cycle continues then finishes.
	// energy=100.008 reflects ~0.008 kWh accumulated during the full cycle
	// (matches the ~0.0082 kWh the integrator sees, keeping divergence <5%).
	logger.Info("=== phase 3: cycle continues and finishes ===")
	mustPublish(pub2, "zigbee2mqtt/kitchen_dishwasher", `{"power":1400,"energy":100.008,"linkquality":88}`, false)
	sleep(1000)
	mustPublish(pub2, "zigbee2mqtt/kitchen_dishwasher", `{"power":0.4,"energy":100.008,"linkquality":87}`, false)
	sleep(12000) // wait for InactiveSustainedFor (10 s)
	mustPublish(pub2, "zigbee2mqtt/kitchen_dishwasher", `{"power":0.3,"energy":100.008,"linkquality":87}`, false)
	sleep(2000)

	logger.Info("scenario complete", "fixture", *outPath)
}

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// -------------------------------------------------------------------------
// Sniffer
// -------------------------------------------------------------------------

type sniffRecord struct {
	Timestamp time.Time       `json:"ts"`
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Retained  bool            `json:"retained,omitempty"`
}

type sniffer struct {
	mu      sync.Mutex
	f       *os.File
	w       *bufio.Writer
	initial bool
}

func newSniffer(path string) (*sniffer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &sniffer{f: f, w: bufio.NewWriter(f), initial: true}, nil
}

func (s *sniffer) write(topic string, payload []byte, retained bool) {
	rec := sniffRecord{
		Timestamp: time.Now().UTC(),
		Topic:     topic,
		Payload:   formatPayload(payload),
		Retained:  retained,
	}
	b, _ := json.Marshal(rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Write(b)
	s.w.WriteByte('\n')
	s.w.Flush()
}

func (s *sniffer) writeMarker(topic string, payload any) {
	raw, _ := json.Marshal(payload)
	rec := sniffRecord{Timestamp: time.Now().UTC(), Topic: topic, Payload: json.RawMessage(raw)}
	b, _ := json.Marshal(rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Write(b)
	s.w.WriteByte('\n')
	s.w.Flush()
}

func (s *sniffer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Flush()
	s.f.Close()
}

func (s *sniffer) connectAndWatch(brokerAddr, clientID string, reconnected chan<- struct{}) {
	opts := paho.NewClientOptions().
		AddBroker(brokerAddr).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetCleanSession(true).
		SetOrderMatters(false).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			slog.Info("sniffer: connection lost", "error", err)
			s.writeMarker("_capture/connection_lost", map[string]string{"error": err.Error()})
		}).
		SetOnConnectHandler(func(c paho.Client) {
			c.Subscribe("#", 0, func(_ paho.Client, m paho.Message) {
				if len(m.Topic()) > 0 && m.Topic()[0] == '_' {
					return
				}
				s.write(m.Topic(), m.Payload(), m.Retained())
			})
			s.mu.Lock()
			first := s.initial
			s.initial = false
			s.mu.Unlock()
			if !first {
				slog.Info("sniffer: reconnected")
				s.writeMarker("_capture/reconnected", map[string]any{})
				select {
				case reconnected <- struct{}{}:
				default:
				}
			}
		})

	c := paho.NewClient(opts)
	tok := c.Connect()
	tok.WaitTimeout(5 * time.Second)
}

// -------------------------------------------------------------------------
// Publisher helpers
// -------------------------------------------------------------------------

func mustConnect(brokerAddr, clientID string) paho.Client {
	opts := paho.NewClientOptions().
		AddBroker(brokerAddr).
		SetClientID(clientID).
		SetCleanSession(true)
	c := paho.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		panic("connect timeout: " + clientID)
	}
	if err := tok.Error(); err != nil {
		panic("connect error: " + err.Error())
	}
	return c
}

func mustPublish(c paho.Client, topic, payload string, retained bool) {
	tok := c.Publish(topic, 0, retained, []byte(payload))
	tok.WaitTimeout(2 * time.Second)
}

func formatPayload(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`""`)
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	enc, _ := json.Marshal(string(b))
	return json.RawMessage(enc)
}

// -------------------------------------------------------------------------
// Engine config — short thresholds so the full cycle runs in ~30 s.
// -------------------------------------------------------------------------

const bridgeDevicesJSON = `[{"ieee_address":"0x00158d0000aabbcc","friendly_name":"kitchen_dishwasher","type":"EndDevice"}]`

func scenarioCfg(brokerAddr string) config.Config {
	active := 3 * time.Second
	inactive := 10 * time.Second
	idleW := 5.0
	activeW := 20.0

	cfg := config.Default()
	cfg.MQTT.Broker = brokerAddr
	cfg.MQTT.ClientID = "broker-scenario-engine"
	cfg.HTTP.Listen = ""
	cfg.RecentLog.Path = ""
	cfg.Influx.Enabled = false
	cfg.Availability.OfflineDebounce = 5 * time.Second
	cfg.Energy.MaxIntegrationGap = 30 * time.Minute
	cfg.Energy.DivergenceWarningPct = 101 // suppress: counter delta is fixed, integration varies with wall time
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"cycle_power_device": {
			NameHints: []string{"dishwasher"},
			DefaultThresholds: config.Thresholds{
				IdleBelowW:           &idleW,
				ActiveAboveW:         &activeW,
				ActiveSustainedFor:   &active,
				InactiveSustainedFor: &inactive,
			},
			EnergyStrategy: "counter",
		},
	}
	return cfg
}

// -------------------------------------------------------------------------
// Derived event printer
// -------------------------------------------------------------------------

type printSink struct{ log *slog.Logger }

func (p *printSink) OnDerivedEvent(ev model.DerivedEvent) {
	p.log.Info("derived event",
		"type", string(ev.Type),
		"device", ev.DeviceID,
		"summary", ev.Summary,
	)
}
