// Command fixture-capture subscribes to configured MQTT topics and
// writes every received message as one JSONL line in the shape
// internal/testdata/fixtures/*.jsonl expects:
//
//	{"ts":"...","topic":"...","payload":<json>}
//
// It is intended for capturing real Zigbee2MQTT traffic for fixture
// generation — including across deliberate broker restarts, so the
// engine's reconnect path can be tested against a real-world sequence.
//
// Synthetic marker records are emitted on disconnect and reconnect:
//
//	{"ts":"...","topic":"_capture/connection_lost","payload":{"error":"..."}}
//	{"ts":"...","topic":"_capture/reconnected","payload":{}}
//
// Fixture replay can ignore these (topic starts with "_capture/") or,
// in reconnect-specific tests, assert on them.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

type record struct {
	Timestamp time.Time       `json:"ts"`
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
}

// formatPayload returns the payload as a JSON value. If the bytes
// already parse as JSON we use them directly; otherwise we wrap them
// as a JSON string. This matches what the existing fixtures look like
// — JSON objects/arrays for device payloads, strings for availability.
func formatPayload(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`""`)
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	enc, err := json.Marshal(string(b))
	if err != nil {
		// json.Marshal of a string can only fail on encoding errors
		// we don't reasonably expect; fall back to an empty string.
		return json.RawMessage(`""`)
	}
	return json.RawMessage(enc)
}

// lineWriter serialises writes from MQTT callbacks (called on paho's
// goroutines) and flushes after every line so killed processes don't
// drop captured messages.
type lineWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
	f  *os.File // nil when writing to stdout
}

func newLineWriter(out io.Writer, f *os.File) *lineWriter {
	return &lineWriter{w: bufio.NewWriter(out), f: f}
}

func (l *lineWriter) write(r record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	if _, err := l.w.Write([]byte("\n")); err != nil {
		return err
	}
	if err := l.w.Flush(); err != nil {
		return err
	}
	if l.f != nil {
		// Push to disk so SIGKILL doesn't lose a few seconds of capture.
		_ = l.f.Sync()
	}
	return nil
}

func (l *lineWriter) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.w.Flush(); err != nil {
		return err
	}
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

func main() {
	broker := flag.String("broker", "tcp://localhost:1883", "MQTT broker URL")
	clientID := flag.String("client-id", "statehouse-fixture-capture", "MQTT client ID")
	username := flag.String("username", "", "MQTT username (optional)")
	password := flag.String("password", "", "MQTT password (optional)")
	topics := flag.String("topics", "zigbee2mqtt/#", "Comma-separated list of MQTT topic filters to subscribe to")
	output := flag.String("output", "", `Output JSONL file; "-" or empty writes to stdout`)
	duration := flag.Duration("duration", 0, "Capture duration (0 = until SIGINT/SIGTERM)")
	markReconnects := flag.Bool("mark-reconnects", true, "Emit _capture/connection_lost and _capture/reconnected marker records")
	qos := flag.Int("qos", 0, "Subscribe QoS (0, 1, 2)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	out, file, err := openOutput(*output)
	if err != nil {
		logger.Error("open output", "error", err)
		os.Exit(1)
	}
	writer := newLineWriter(out, file)

	topicList := splitTopics(*topics)
	if len(topicList) == 0 {
		logger.Error("no topics configured")
		os.Exit(1)
	}

	// Track whether the first connect has happened so we can tell the
	// initial connect apart from a recovery.
	var (
		mu               sync.Mutex
		hadFirstConnect  bool
		messageCount     int
		reconnectCount   int
		lastLostAt       time.Time
	)

	emit := func(r record) {
		if err := writer.write(r); err != nil {
			logger.Warn("write record", "error", err)
		}
	}

	onMessage := func(_ paho.Client, m paho.Message) {
		mu.Lock()
		messageCount++
		mu.Unlock()
		emit(record{
			Timestamp: time.Now().UTC(),
			Topic:     m.Topic(),
			Payload:   formatPayload(m.Payload()),
		})
	}

	onConnect := func(c paho.Client) {
		mu.Lock()
		first := !hadFirstConnect
		hadFirstConnect = true
		mu.Unlock()
		if first {
			logger.Info("connected", "broker", *broker)
		} else {
			mu.Lock()
			reconnectCount++
			rc := reconnectCount
			down := time.Since(lastLostAt)
			mu.Unlock()
			logger.Info("reconnected", "attempt", rc, "downtime", down.Truncate(time.Millisecond))
			if *markReconnects {
				emit(record{
					Timestamp: time.Now().UTC(),
					Topic:     "_capture/reconnected",
					Payload:   json.RawMessage(fmt.Sprintf(`{"downtime_ms":%d}`, down.Milliseconds())),
				})
			}
		}
		for _, t := range topicList {
			tok := c.Subscribe(t, byte(*qos), onMessage)
			if !tok.WaitTimeout(5 * time.Second) {
				logger.Warn("subscribe timeout", "topic", t)
				continue
			}
			if err := tok.Error(); err != nil {
				logger.Warn("subscribe failed", "topic", t, "error", err)
			} else {
				logger.Info("subscribed", "topic", t, "qos", *qos)
			}
		}
	}

	onLost := func(_ paho.Client, err error) {
		mu.Lock()
		lastLostAt = time.Now()
		mu.Unlock()
		logger.Warn("connection lost; paho will retry", "error", err)
		if *markReconnects {
			payload, _ := json.Marshal(map[string]string{"error": err.Error()})
			emit(record{
				Timestamp: time.Now().UTC(),
				Topic:     "_capture/connection_lost",
				Payload:   payload,
			})
		}
	}

	opts := paho.NewClientOptions().
		AddBroker(*broker).
		SetClientID(*clientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetCleanSession(true).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetOrderMatters(false).
		SetOnConnectHandler(onConnect).
		SetConnectionLostHandler(onLost)
	if *username != "" {
		opts.SetUsername(*username)
		opts.SetPassword(*password)
	}

	client := paho.NewClient(opts)
	tok := client.Connect()
	// Don't block forever — paho keeps retrying in the background.
	if !tok.WaitTimeout(3 * time.Second) {
		logger.Warn("initial connect did not complete in 3s; continuing — paho will keep retrying")
	} else if err := tok.Error(); err != nil {
		logger.Warn("initial connect error; paho will keep retrying", "error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *duration > 0 {
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigCh:
		logger.Info("received signal; shutting down", "signal", s)
	case <-ctx.Done():
		logger.Info("duration elapsed; shutting down")
	}

	client.Disconnect(250)
	if err := writer.close(); err != nil {
		logger.Warn("close output", "error", err)
	}

	mu.Lock()
	total := messageCount
	reconnects := reconnectCount
	mu.Unlock()
	logger.Info("capture complete", "messages", total, "reconnects", reconnects)
}

func openOutput(path string) (io.Writer, *os.File, error) {
	if path == "" || path == "-" {
		return os.Stdout, nil, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}

func splitTopics(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
