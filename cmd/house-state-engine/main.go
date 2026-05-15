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

	"github.com/sweeney/statehouse/internal/adapter"
	"github.com/sweeney/statehouse/internal/adapter/boiler"
	"github.com/sweeney/statehouse/internal/adapter/climate"
	"github.com/sweeney/statehouse/internal/adapter/meter"
	"github.com/sweeney/statehouse/internal/adapter/ups"
	"github.com/sweeney/statehouse/internal/adapter/zigbee2mqtt"
	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/history"
	"github.com/sweeney/statehouse/internal/httpapi"
	"github.com/sweeney/statehouse/internal/influx"
	"github.com/sweeney/statehouse/internal/model"
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

	// Adapter registry. Each adapter knows its protocol's quirks and
	// translates them into engine calls; the engine itself knows
	// nothing about Z2M, Tasmota, Shelly, etc. To add a new source,
	// add an adapter and append it here.
	var adapters []adapter.Adapter
	if cfg.Adapters.Zigbee2MQTT.IsEnabled() {
		adapters = append(adapters, zigbee2mqtt.New(engine, cfg.Adapters.Zigbee2MQTT.BaseTopic, logger))
	}
	if cfg.Adapters.Boiler.IsEnabled() {
		adapters = append(adapters, boiler.New(engine, cfg.Adapters.Boiler.BaseTopic, logger))
	}
	if cfg.Adapters.UPS.IsEnabled() {
		adapters = append(adapters, ups.New(engine, cfg.Adapters.UPS.BaseTopic, logger))
	}
	if cfg.Adapters.Climate.IsEnabled() {
		adapters = append(adapters, climate.New(engine, cfg.Adapters.Climate.BaseTopic, logger))
	}
	if cfg.Adapters.Meter.IsEnabled() {
		adapters = append(adapters, meter.New(engine, cfg.Adapters.Meter.BaseTopic, logger))
	}
	for _, a := range adapters {
		for _, filter := range a.Subscriptions() {
			if err := mqttClient.Subscribe(filter, 0, a.HandleMessage); err != nil {
				logger.Warn("mqtt subscribe failed", "adapter", a.Name(), "topic", filter, "error", err)
			}
		}
		logger.Info("adapter ready", "name", a.Name(), "subscriptions", a.Subscriptions())
	}

	// Look up per-class staleness override for DTO building.
	stalenessFor := func(class string) *int {
		if c, ok := cfg.DeviceClasses[class]; ok {
			return c.StalenessSeconds
		}
		return nil
	}
	publisher := &mqtt.Publisher{
		Client: mqttClient,
		Prefix: cfg.MQTT.PublishPrefix,
		Store:  store,
		Logger: logger,
		BuildSnapshot: func(snap model.Snapshot, now time.Time) any {
			return httpapi.BuildSnapshot(snap, now, stalenessFor)
		},
		BuildHouse: func(h model.House) any {
			return httpapi.BuildHouseResponse(h)
		},
		BuildDevice: func(d model.Device, now time.Time) any {
			return httpapi.BuildDeviceResponse(d, now, stalenessFor(d.Class))
		},
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

	api := httpapi.New(cfg.HTTP.Listen, store, hlog, mqttClient, influxWriter, logger, cfg.DeviceClasses)
	engine.AddCanonicalSink(api)
	engine.AddDerivedSink(api)

	ctx, cancel := signalContext()
	defer cancel()

	// Run the publisher with a non-blocking bounded queue so broker
	// stalls don't park paho dispatch goroutines on the publisher
	// mutex (issue #50).
	publisher.Start(ctx)

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
	publisher.Close()
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
