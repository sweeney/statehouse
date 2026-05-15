package mqtt

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// publishQueueSize bounds the number of in-flight publish jobs queued
// for the worker. A bounded queue converts broker stalls from
// "unbounded goroutine + heap growth" (every paho dispatch goroutine
// piling up at p.mu) into "bounded queue + visible drop counter".
//
// var (not const) so tests can shrink it deterministically.
var publishQueueSize = 256

// Publisher is the EventSink that fans derived events out to MQTT
// topics under PublishPrefix. It also publishes per-device state and
// the periodic whole-house snapshot.
//
// Builders, when non-nil, wrap the raw model into the same DTO shape
// the HTTP API publishes (schema_version, summary, warnings, etc.).
// Set them via BuildSnapshot / BuildHouse / BuildDevice. If left nil,
// the publisher falls back to the raw model.Snapshot/House/Device so
// internal tests don't need to import the DTO package.
//
// Lifecycle: call Start() once after construction to enable the
// non-blocking publish queue, and Close() at shutdown to drain it.
// If Start is never called, OnDerivedEvent falls back to a
// synchronous publish under p.mu — convenient for unit tests that
// want to inspect publishes immediately without running the worker.
type Publisher struct {
	Client Client
	Prefix string
	Store  *state.Store
	Logger *slog.Logger

	BuildSnapshot func(snap model.Snapshot, now time.Time) any
	BuildHouse    func(h model.House) any
	BuildDevice   func(d model.Device, now time.Time) any

	// mu guards p.in for the Start/Close lifecycle, serialises the
	// non-blocking send in publishJSON against Close's close(in), and
	// (only when Start was never called) serialises the synchronous
	// fallback publish path.
	mu sync.Mutex

	in      chan publishJob
	wg      sync.WaitGroup
	dropped uint64
}

type publishJob struct {
	topic    string
	retained bool
	payload  []byte
}

// Start launches the publisher's worker goroutine. After Start, every
// publishJSON enqueues onto a bounded channel served by a single
// worker — the engine's emit hot path becomes non-blocking. The
// worker exits only when Close() closes the channel, so any
// jobs buffered at shutdown are drained before the worker returns.
func (p *Publisher) Start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.in != nil {
		p.mu.Unlock()
		return
	}
	ch := make(chan publishJob, publishQueueSize)
	p.in = ch
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		// Drain semantics: ranging over the channel exits when the
		// channel is closed AND empty. Close() relies on this to drain
		// in-flight jobs at shutdown.
		for j := range ch {
			if err := p.Client.Publish(j.topic, 0, j.retained, j.payload); err != nil {
				if p.Logger != nil {
					p.Logger.Warn("mqtt publish failed", "topic", j.topic, "error", err)
				}
			}
		}
	}()
}

// Close drains the publish queue and waits for the worker to exit.
// Safe to call multiple times; a no-op if Start was never called.
//
// Holding p.mu around close(ch) closes the race with publishJSON's
// non-blocking send: a closed-channel send would otherwise panic.
func (p *Publisher) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	ch := p.in
	p.in = nil
	if ch != nil {
		close(ch)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// Dropped returns the cumulative count of publish jobs dropped because
// the queue was full. Exposed for /metrics and tests.
func (p *Publisher) Dropped() uint64 {
	if p == nil {
		return 0
	}
	return atomic.LoadUint64(&p.dropped)
}

// OnDerivedEvent implements state.EventSink. For each derived event:
//   - publish the event itself on house/events/derived,
//   - publish the affected per-device snapshot on
//     house/state/devices/{id} (if applicable),
//   - publish the house snapshot on house/state/house when house state
//     changes.
func (p *Publisher) OnDerivedEvent(ev model.DerivedEvent) {
	if p == nil || p.Client == nil {
		return
	}
	now := time.Now()
	p.publishJSON(p.Prefix+"/events/derived", false, ev)
	if ev.DeviceID != "" {
		if d, ok := p.Store.Get(ev.DeviceID); ok {
			p.publishJSON(p.Prefix+"/state/devices/"+ev.DeviceID, true, p.devicePayload(d, now))
		}
	}
	switch ev.Type {
	case model.EvtHouseStateChanged:
		p.publishJSON(p.Prefix+"/state/house", true, p.housePayload(p.Store.House()))
	}
	if relevantForSnapshot(ev.Type) {
		p.publishJSON(p.Prefix+"/state/snapshot", true, p.snapshotPayload(p.Store.Snapshot(), now))
	}
}

// PublishSnapshot is exposed for periodic emission (independent of
// derived events).
func (p *Publisher) PublishSnapshot() {
	if p == nil || p.Client == nil || p.Store == nil {
		return
	}
	now := time.Now()
	p.publishJSON(p.Prefix+"/state/snapshot", true, p.snapshotPayload(p.Store.Snapshot(), now))
	p.publishJSON(p.Prefix+"/state/house", true, p.housePayload(p.Store.House()))
}

func (p *Publisher) snapshotPayload(snap model.Snapshot, now time.Time) any {
	if p.BuildSnapshot != nil {
		return p.BuildSnapshot(snap, now)
	}
	return snap
}

func (p *Publisher) housePayload(h model.House) any {
	if p.BuildHouse != nil {
		return p.BuildHouse(h)
	}
	return h
}

func (p *Publisher) devicePayload(d model.Device, now time.Time) any {
	if p.BuildDevice != nil {
		return p.BuildDevice(d, now)
	}
	return d
}

func (p *Publisher) publishJSON(topic string, retained bool, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("mqtt marshal failed", "topic", topic, "error", err)
		}
		return
	}
	// Hold p.mu across the (non-blocking) send so Close() cannot
	// race close(ch) with an in-flight send and trigger a panic.
	// The default arm makes the critical section O(1).
	p.mu.Lock()
	if p.in == nil {
		// Sync fallback when Start() was never called — used by unit
		// tests that want to inspect publishes immediately on return.
		// Production callers must call Start(); in that path p.in is
		// non-nil and we never reach here. The mutex is held across
		// Publish only on this test-only path.
		err := p.Client.Publish(topic, 0, retained, b)
		p.mu.Unlock()
		if err != nil && p.Logger != nil {
			p.Logger.Warn("mqtt publish failed", "topic", topic, "error", err)
		}
		return
	}
	select {
	case p.in <- publishJob{topic: topic, retained: retained, payload: b}:
		p.mu.Unlock()
	default:
		p.mu.Unlock()
		// Queue full. Drop and increment counter. Retained topics
		// self-heal on the next derived event of the same shape, and
		// the 30s periodic snapshot in main.go acts as a safety net.
		atomic.AddUint64(&p.dropped, 1)
		if p.Logger != nil {
			p.Logger.Warn("mqtt publish queue full; dropping", "topic", topic)
		}
	}
}

func relevantForSnapshot(t model.DerivedEventType) bool {
	switch t {
	case model.EvtDeviceActivityChanged,
		model.EvtCycleStarted, model.EvtCycleFinished,
		model.EvtContinuousCycleStarted, model.EvtContinuousCycleFinished,
		model.EvtMediaActive, model.EvtMediaInactive,
		model.EvtHouseStateChanged, model.EvtDeviceAvailabilityChanged:
		return true
	}
	return false
}
