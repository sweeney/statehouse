package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/history"
	"github.com/sweeney/statehouse/internal/httpapi"
	"github.com/sweeney/statehouse/internal/influx"
	"github.com/sweeney/statehouse/internal/mqtt"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

func main() {
	configPath := flag.String("config", "/etc/house-state-engine/config.yaml", "path to YAML config")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	logger.Info("starting", "config", *configPath, "broker", cfg.MQTT.Broker, "http", cfg.HTTP.Listen)

	store := state.NewStore()
	engine := state.NewEngine(cfg, store, testutil.RealClock{})

	hlog, err := history.Open(cfg.RecentLog.Path, cfg.RecentLog.RetentionHours, cfg.RecentLog.MaxSizeMB, 4096)
	if err != nil {
		logger.Warn("recent log open failed; continuing without disk persistence", "error", err)
		hlog, _ = history.Open("", cfg.RecentLog.RetentionHours, cfg.RecentLog.MaxSizeMB, 4096)
	}
	histSink := &history.Sink{Log: hlog}
	engine.AddCanonicalSink(histSink)
	engine.AddDerivedSink(histSink)

	mqttClient := mqtt.New(mqtt.Config{
		Broker:   cfg.MQTT.Broker,
		ClientID: cfg.MQTT.ClientID,
		Username: cfg.MQTT.Username,
		Password: cfg.MQTT.Password,
	})
	if err := mqttClient.Connect(); err != nil {
		logger.Warn("mqtt connect failed; will retry in background", "error", err)
	}

	subscriber := &mqtt.Z2MSubscriber{
		Engine: engine,
		Base:   cfg.MQTT.Zigbee2MQTTBase,
		Logger: logger,
	}
	for _, topic := range cfg.MQTT.Subscribe {
		if err := mqttClient.Subscribe(topic, 0, subscriber.HandleMessage); err != nil {
			logger.Warn("mqtt subscribe failed", "topic", topic, "error", err)
		}
	}

	publisher := &mqtt.Publisher{
		Client: mqttClient,
		Prefix: cfg.MQTT.PublishPrefix,
		Store:  store,
		Logger: logger,
	}
	engine.AddDerivedSink(publisher)

	influxWriter := influx.New(cfg.Influx.Enabled, influx.Config{
		URL:    cfg.Influx.URL,
		Org:    cfg.Influx.Org,
		Bucket: cfg.Influx.Bucket,
		Token:  cfg.Influx.Token,
	}, store, logger)
	engine.AddCanonicalSink(influxWriter)
	engine.AddDerivedSink(influxWriter)

	api := httpapi.New(cfg.HTTP.Listen, store, hlog, mqttClient, influxWriter, logger)
	engine.AddCanonicalSink(api)
	engine.AddDerivedSink(api)

	ctx, cancel := signalContext()
	defer cancel()

	// Tick: drives the availability debounce + house recompute.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				engine.Tick()
			}
		}
	}()

	// Periodic snapshot publication.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				publisher.PublishSnapshot()
			}
		}
	}()

	logger.Info("ready")
	if err := api.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("http server", "error", err)
	}

	logger.Info("shutting down")
	mqttClient.Disconnect()
	influxWriter.Close()
	if err := hlog.Close(); err != nil {
		logger.Warn("recent log close", "error", err)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
