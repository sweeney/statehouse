// bellhouse: household push notifications for the swee.net ecosystem, with no
// native app. MQTT in (statehouse, ups-mqtt), Web Push out (Apple, Google,
// Mozilla push services), per-user preferences, a PWA to manage it.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sweeney/identity/common/auth"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"` // https://bells.swee.net — must be https for iOS
	DataDir   string `yaml:"data_dir"`
	MQTT      struct {
		Broker   string `yaml:"broker"`
		ClientID string `yaml:"client_id"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"mqtt"`
	Identity struct {
		BaseURL  string `yaml:"base_url"`  // unset = auth off (local dev)
		ClientID string `yaml:"client_id"` // public PKCE client for the PWA
	} `yaml:"identity"`
	NotifyClients []string    `yaml:"notify_clients"` // service client_ids allowed to POST /api/notify
	Rules         RulesConfig `yaml:"rules"`
}

func main() {
	cfgPath := flag.String("config", "bellhouse.yaml", "config file")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	var cfg Config
	b, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Error("read config", "err", err)
		os.Exit(1)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		log.Error("parse config", "err", err)
		os.Exit(1)
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8787"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/bellhouse"
	}
	if cfg.PublicURL == "" {
		log.Error("public_url is required: it is the push 'navigate' origin and the VAPID subject")
		os.Exit(1)
	}
	cfg.Rules.PublicURL = cfg.PublicURL

	pub, priv, err := loadOrCreateVAPID(cfg.DataDir + "/vapid.json")
	if err != nil {
		log.Error("vapid", "err", err)
		os.Exit(1)
	}
	store, err := OpenStore(cfg.DataDir + "/state.json")
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	pusher := &Pusher{
		Store: store, VAPIDPub: pub, VAPIDPriv: priv,
		Subscriber: cfg.PublicURL, HTTPClient: &http.Client{Timeout: 20 * time.Second},
		Log: log, Now: time.Now,
	}

	api := &API{Store: store, Pusher: pusher, IdentityURL: cfg.Identity.BaseURL,
		ClientID: cfg.Identity.ClientID, NotifyClients: cfg.NotifyClients, PublicURL: cfg.PublicURL, Log: log}
	if cfg.Identity.BaseURL != "" {
		api.Verifier, err = auth.NewJWKSVerifier(auth.JWKSVerifierConfig{
			IssuerURL: cfg.Identity.BaseURL, Issuer: cfg.Identity.BaseURL,
		})
		if err != nil {
			log.Error("identity", "err", err)
			os.Exit(1)
		}
	} else {
		log.Warn("identity.base_url unset: authentication is OFF, every caller is user 'dev'")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rules := NewRules(cfg.Rules)
	if cfg.MQTT.Broker != "" {
		startMQTT(ctx, cfg, rules, store, pusher, log)
	}

	srv := &http.Server{Addr: cfg.Listen, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Info("listening", "addr", cfg.Listen, "public_url", cfg.PublicURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http", "err", err)
		os.Exit(1)
	}
}

func startMQTT(ctx context.Context, cfg Config, rules *Rules, store *Store, pusher *Pusher, log *slog.Logger) {
	work := make(chan Notification, 64)
	go func() {
		for n := range work {
			ds := pusher.Dispatch(ctx, n, "")
			log.Info("dispatched", "kind", n.Kind, "subject", n.Subject, "deliveries", len(ds))
		}
	}()

	opts := mqtt.NewClientOptions().AddBroker(cfg.MQTT.Broker).
		SetClientID(orDefault(cfg.MQTT.ClientID, "bellhouse")).
		SetUsername(cfg.MQTT.Username).SetPassword(cfg.MQTT.Password).
		SetAutoReconnect(true).SetConnectRetry(true)
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		filters := map[string]byte{}
		for _, t := range rules.cfg.Topics() {
			filters[t] = 1
		}
		c.SubscribeMultiple(filters, func(_ mqtt.Client, m mqtt.Message) {
			for _, n := range rules.Translate(m.Topic(), m.Payload(), time.Now()) {
				// Dedupe before queueing: retained topics replay on every
				// reconnect, and ups-mqtt re-publishes an outage every poll.
				if !store.FirstSighting(n.DedupeKey, time.Now()) {
					continue
				}
				select {
				case work <- n:
				default:
					log.Warn("dispatch queue full, dropping", "kind", n.Kind)
				}
			}
		})
		log.Info("mqtt connected", "topics", strings.Join(rules.cfg.Topics(), " "))
	})
	c := mqtt.NewClient(opts)
	c.Connect()
	go func() { <-ctx.Done(); c.Disconnect(250) }()
}

// VAPID keys must be stable for the life of every subscription: rotating them
// silently orphans every device, which then has to re-subscribe.
func loadOrCreateVAPID(path string) (pub, priv string, err error) {
	var k struct{ Public, Private string }
	if b, err := os.ReadFile(path); err == nil {
		if err := yamlOrJSON(b, &k); err != nil {
			return "", "", err
		}
		return k.Public, k.Private, nil
	}
	k.Private, k.Public, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return "", "", err
	}
	b, _ := yaml.Marshal(k)
	return k.Public, k.Private, os.WriteFile(path, b, 0o600)
}

func yamlOrJSON(b []byte, v any) error { return yaml.Unmarshal(b, v) } // YAML ⊃ JSON

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
