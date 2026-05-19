package mqtt

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// newPublisherTest returns a Publisher wired to a FakeClient and an
// empty Store seeded with one device record so per-device topics have
// something to publish.
func newPublisherTest(t *testing.T) (*Publisher, *FakeClient, *state.Store) {
	t.Helper()
	store := state.NewStore()
	rt := device.NewRuntime(device.Profile{Class: device.ClassCyclePower}, 30*time.Minute)
	store.Upsert("kitchen_dishwasher", model.Device{
		ID:          "kitchen_dishwasher",
		DisplayName: "Kitchen dishwasher",
		Class:       device.ClassCyclePower,
		Identity:    model.DeviceIdentity{Scheme: "zigbee", Primary: "0x1", Display: "kitchen_dishwasher"},
	}, rt)
	client := NewFakeClient()
	pub := &Publisher{Client: client, Prefix: "house", Store: store}
	return pub, client, store
}

func TestPublisher_DerivedEventPublishesToEventsTopic(t *testing.T) {
	pub, client, _ := newPublisherTest(t)
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_1",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtCycleStarted,
		DeviceID:  "kitchen_dishwasher",
	})
	got := client.PublishedOn("house/events/derived")
	if len(got) != 1 {
		t.Fatalf("expected one publish on house/events/derived, got %d", len(got))
	}
	if got[0].Retained {
		t.Fatalf("derived events must NOT be retained (they are not state)")
	}
	var decoded model.DerivedEvent
	if err := json.Unmarshal(got[0].Payload, &decoded); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}
	if decoded.Type != model.EvtCycleStarted || decoded.DeviceID != "kitchen_dishwasher" {
		t.Fatalf("unexpected decoded event: %+v", decoded)
	}
}

func TestPublisher_DeviceEventPublishesRetainedDeviceState(t *testing.T) {
	pub, client, _ := newPublisherTest(t)
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_2",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtDeviceActivityChanged,
		DeviceID:  "kitchen_dishwasher",
		Evidence:  map[string]any{"from": "idle", "to": "running"},
	})
	got := client.PublishedOn("house/state/devices/kitchen_dishwasher")
	if len(got) != 1 {
		t.Fatalf("expected one publish on per-device topic, got %d", len(got))
	}
	if !got[0].Retained {
		t.Fatalf("per-device state must be retained so late subscribers see current state")
	}
}

func TestPublisher_HouseStateChangedPublishesRetainedHouseAndSnapshot(t *testing.T) {
	pub, client, _ := newPublisherTest(t)
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_3",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtHouseStateChanged,
		Evidence:  map[string]any{"state": "active", "confidence": 0.85},
	})
	if got := client.PublishedOn("house/state/house"); len(got) != 1 || !got[0].Retained {
		t.Fatalf("expected one retained publish on house/state/house, got %+v", got)
	}
	if got := client.PublishedOn("house/state/snapshot"); len(got) != 1 || !got[0].Retained {
		t.Fatalf("expected one retained publish on house/state/snapshot, got %+v", got)
	}
	if got := client.PublishedOn("house/events/derived"); len(got) != 1 {
		t.Fatalf("expected one publish on house/events/derived, got %d", len(got))
	}
}

func TestPublisher_DiscoveryEventDoesNotPublishSnapshot(t *testing.T) {
	pub, client, _ := newPublisherTest(t)
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_4",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtDeviceDiscovered,
		DeviceID:  "kitchen_dishwasher",
	})
	// Discovery is bookkeeping: it should publish the per-device state
	// (retained) and the event itself, but not a fresh snapshot/house.
	if got := client.PublishedOn("house/state/snapshot"); len(got) != 0 {
		t.Fatalf("discovery should not trigger snapshot publish, got %+v", got)
	}
	if got := client.PublishedOn("house/state/house"); len(got) != 0 {
		t.Fatalf("discovery should not trigger house publish, got %+v", got)
	}
}

func TestPublisher_PublishErrorIsSwallowed(t *testing.T) {
	// The engine must not crash when MQTT publish fails (broker down,
	// queue full, etc.). The publisher logs and continues.
	pub, client, _ := newPublisherTest(t)
	client.PublishErr = errors.New("broker unreachable")
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_5",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtCycleStarted,
		DeviceID:  "kitchen_dishwasher",
	})
	if len(client.Published) != 0 {
		t.Fatalf("publish errors should not record anything in the fake, got %+v", client.Published)
	}
}

func TestPublisher_PublishSnapshotPublishesHouseAndSnapshot(t *testing.T) {
	pub, client, _ := newPublisherTest(t)
	pub.PublishSnapshot()
	if got := client.PublishedOn("house/state/snapshot"); len(got) != 1 || !got[0].Retained {
		t.Fatalf("expected retained snapshot publish, got %+v", got)
	}
	if got := client.PublishedOn("house/state/house"); len(got) != 1 || !got[0].Retained {
		t.Fatalf("expected retained house publish, got %+v", got)
	}
}

func TestPublisher_NilClientIsSafe(t *testing.T) {
	// Publisher is wired before MQTT is necessarily up; constructing it
	// with a nil Client must not crash on event delivery.
	pub := &Publisher{Client: nil, Prefix: "house", Store: state.NewStore()}
	pub.OnDerivedEvent(model.DerivedEvent{Type: model.EvtCycleStarted})
	pub.PublishSnapshot()
}

func TestPublisher_SurvivesFaultClientWindow(t *testing.T) {
	// Wrap a FakeClient in a FaultClient that fails the first three
	// publish attempts. A single EvtCycleStarted produces three
	// publishes (event, per-device state, snapshot), so the first
	// event is fully eaten by the fault window and the second comes
	// through cleanly. This is the "broker flapped briefly" scenario
	// that boiler-sensor's faultReader pattern inspired.
	store := state.NewStore()
	rt := device.NewRuntime(device.Profile{Class: device.ClassCyclePower}, 30*time.Minute)
	store.Upsert("k", model.Device{ID: "k", Class: device.ClassCyclePower, Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "k", Display: "k"}}, rt)
	inner := NewFakeClient()
	fault := &FaultClient{Inner: inner, FaultStart: 0, FaultEnd: 3}
	pub := &Publisher{Client: fault, Prefix: "house", Store: store}

	for i := 0; i < 2; i++ {
		pub.OnDerivedEvent(model.DerivedEvent{
			ID:        "evt",
			Timestamp: time.Now().UTC(),
			Type:      model.EvtCycleStarted,
			DeviceID:  "k",
		})
	}
	got := inner.PublishedOn("house/events/derived")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 derived publish to survive (second event), got %d (total inner records: %d)", len(got), len(inner.Published))
	}
}

// TestPublisher_BuildersWrapPayloads verifies the DTO builder hooks are
// invoked and their output is what lands on retained topics. This is the
// contract that lets the MQTT snapshot carry schema_version / summary /
// warnings the same way the HTTP /state response does.
func TestPublisher_BuildersWrapPayloads(t *testing.T) {
	pub, client, _ := newPublisherTest(t)
	pub.BuildSnapshot = func(snap model.Snapshot, now time.Time) any {
		return map[string]any{"schema_version": "v1", "devices": len(snap.Devices)}
	}
	pub.BuildHouse = func(h model.House, now time.Time) any {
		return map[string]any{"shape": "house_dto"}
	}
	pub.BuildDevice = func(d model.Device, now time.Time) any {
		return map[string]any{"id": d.ID, "shape": "device_dto"}
	}

	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtHouseStateChanged,
		DeviceID:  "kitchen_dishwasher",
	})

	got := client.PublishedOn("house/state/snapshot")
	if len(got) != 1 {
		t.Fatalf("expected 1 snapshot publish, got %d", len(got))
	}
	if !contains(got[0].Payload, "schema_version") {
		t.Errorf("snapshot payload missing builder output: %s", string(got[0].Payload))
	}

	got = client.PublishedOn("house/state/house")
	if len(got) != 1 || !contains(got[0].Payload, "house_dto") {
		t.Errorf("house payload not from BuildHouse: %s", string(got[0].Payload))
	}

	got = client.PublishedOn("house/state/devices/kitchen_dishwasher")
	if len(got) != 1 || !contains(got[0].Payload, "device_dto") {
		t.Errorf("device payload not from BuildDevice: %s", string(got[0].Payload))
	}
}

// blockingClient implements Client but parks every Publish call on a
// channel until the test releases it. Used to simulate a stalled
// broker without involving timing.
type blockingClient struct {
	gate     chan struct{}
	calls    atomic.Uint64
	released atomic.Uint64
}

func newBlockingClient() *blockingClient {
	return &blockingClient{gate: make(chan struct{})}
}

func (b *blockingClient) Connect() error    { return nil }
func (b *blockingClient) Disconnect()       {}
func (b *blockingClient) IsConnected() bool { return true }
func (b *blockingClient) Subscribe(string, byte, Handler) error {
	return nil
}
func (b *blockingClient) Publish(string, byte, bool, []byte) error {
	b.calls.Add(1)
	<-b.gate
	b.released.Add(1)
	return nil
}
func (b *blockingClient) release() { close(b.gate) }

// TestPublisher_AsyncDoesNotBlockOnStalledBroker reproduces issue #50:
// before the fix, every OnDerivedEvent call against a stalled broker
// would park a paho dispatch goroutine on Publisher.mu, with each one
// holding marshalled snapshot bytes alive on the heap. After the fix,
// OnDerivedEvent should enqueue and return immediately regardless of
// broker liveness, and an overflow should be counted not deadlocked.
func TestPublisher_AsyncDoesNotBlockOnStalledBroker(t *testing.T) {
	prev := publishQueueSize
	publishQueueSize = 4
	defer func() { publishQueueSize = prev }()

	store := state.NewStore()
	rt := device.NewRuntime(device.Profile{Class: device.ClassCyclePower}, 30*time.Minute)
	store.Upsert("k", model.Device{
		ID:       "k",
		Class:    device.ClassCyclePower,
		Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "k", Display: "k"},
	}, rt)

	bc := newBlockingClient()
	pub := &Publisher{Client: bc, Prefix: "house", Store: store}
	pub.Start()

	// Fire 100 derived events against the stalled client. Each call
	// must return promptly — the budget here is generous (1s for the
	// whole batch); the actual cost should be microseconds.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			pub.OnDerivedEvent(model.DerivedEvent{
				ID:        "evt",
				Timestamp: time.Now().UTC(),
				Type:      model.EvtCycleStarted,
				DeviceID:  "k",
			})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("OnDerivedEvent batch blocked despite stalled client")
	}

	// With a 4-slot queue and >>4 publishes attempted while the worker
	// is parked on the first call, the drop counter must be non-zero.
	if got := pub.Dropped(); got == 0 {
		t.Fatalf("expected publish drops on overflow, got 0 (queue=%d)", publishQueueSize)
	}

	// Release the broker and tear down. The worker must drain and exit.
	bc.release()
	pub.Close()
}

// TestPublisher_AsyncCloseDrainsQueue ensures Close() waits for the
// worker to drain in-flight jobs rather than orphaning them.
func TestPublisher_AsyncCloseDrainsQueue(t *testing.T) {
	store := state.NewStore()
	rt := device.NewRuntime(device.Profile{Class: device.ClassCyclePower}, 30*time.Minute)
	store.Upsert("k", model.Device{
		ID:       "k",
		Class:    device.ClassCyclePower,
		Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "k", Display: "k"},
	}, rt)
	fc := NewFakeClient()
	pub := &Publisher{Client: fc, Prefix: "house", Store: store}
	pub.Start()

	for i := 0; i < 5; i++ {
		pub.OnDerivedEvent(model.DerivedEvent{
			ID:        "evt",
			Timestamp: time.Now().UTC(),
			Type:      model.EvtCycleStarted,
			DeviceID:  "k",
		})
	}
	pub.Close()

	// All non-dropped jobs should have made it to the fake client by
	// the time Close returns.
	if got := len(fc.PublishedOn("house/events/derived")); got != 5 {
		t.Fatalf("expected 5 derived publishes after Close drain, got %d", got)
	}
}

// TestPublisher_AsyncCloseConcurrentSendIsRaceSafe stresses the
// reported race between Close() closing the queue and publishJSON's
// non-blocking send. Run under `go test -race` and many publishers
// firing during shutdown: a send-on-closed-channel would panic.
func TestPublisher_AsyncCloseConcurrentSendIsRaceSafe(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		store := state.NewStore()
		rt := device.NewRuntime(device.Profile{Class: device.ClassCyclePower}, 30*time.Minute)
		store.Upsert("k", model.Device{
			ID:       "k",
			Class:    device.ClassCyclePower,
			Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "k", Display: "k"},
		}, rt)
		fc := NewFakeClient()
		pub := &Publisher{Client: fc, Prefix: "house", Store: store}
		pub.Start()

		var wg sync.WaitGroup
		// Many concurrent producers racing against Close.
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					pub.OnDerivedEvent(model.DerivedEvent{
						ID:        "evt",
						Timestamp: time.Now().UTC(),
						Type:      model.EvtCycleStarted,
						DeviceID:  "k",
					})
				}
			}()
		}
		// Close while producers are still firing — any closed-channel
		// send would panic here.
		time.Sleep(time.Microsecond) // let some producers start
		pub.Close()
		wg.Wait()
	}
}

// TestPublisher_AsyncCloseDrainsAfterFlood is the production
// shutdown shape: the worker exits only when Close() closes the
// channel, after the buffered jobs have drained. No ctx involved.
func TestPublisher_AsyncCloseDrainsAfterFlood(t *testing.T) {
	store := state.NewStore()
	rt := device.NewRuntime(device.Profile{Class: device.ClassCyclePower}, 30*time.Minute)
	store.Upsert("k", model.Device{
		ID:       "k",
		Class:    device.ClassCyclePower,
		Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "k", Display: "k"},
	}, rt)
	fc := NewFakeClient()
	pub := &Publisher{Client: fc, Prefix: "house", Store: store}
	pub.Start()

	for i := 0; i < 7; i++ {
		pub.OnDerivedEvent(model.DerivedEvent{
			ID:        "evt",
			Timestamp: time.Now().UTC(),
			Type:      model.EvtCycleStarted,
			DeviceID:  "k",
		})
	}
	pub.Close()

	// All 7 derived publishes (plus their per-device retained
	// publishes) must have landed by the time Close returns.
	if got := len(fc.PublishedOn("house/events/derived")); got != 7 {
		t.Fatalf("expected 7 derived publishes after Close drain, got %d", got)
	}
}

func contains(b []byte, sub string) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return true
		}
	}
	return false
}

// TestPublisher_PublishSnapshotRefreshesAllDeviceTopics is a regression test
// for the passive-sensor retained-message gap. Passive sensors
// (environmental_sensor, ups_sensor, energy_meter) emit a derived event only
// once — on the initial unknown→reporting transition. After that, readings
// update the store but produce no events, so the publisher never re-fires the
// per-device retained topic. If that retained message is cleared (e.g. by a
// broker cleanup or after a fresh broker install) it is never republished.
//
// The fix: PublishSnapshot sweeps every device in the store so the 30-second
// ticker acts as a heartbeat for retained device topics regardless of class.
func TestPublisher_PublishSnapshotRefreshesAllDeviceTopics(t *testing.T) {
	store := state.NewStore()
	rt := device.NewRuntime(device.Profile{Class: device.ClassEnvironmentalSensor}, 30*time.Minute)
	store.Upsert("climate_weatherstation", model.Device{
		ID:    "climate_weatherstation",
		Class: device.ClassEnvironmentalSensor,
		Identity: model.DeviceIdentity{
			Scheme:  "climate",
			Primary: "home",
			Display: "home",
		},
	}, rt)
	client := NewFakeClient()
	pub := &Publisher{Client: client, Prefix: "house", Store: store}

	// Simulate the single discovery event that fires on first startup.
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_discover",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtDeviceDiscovered,
		DeviceID:  "climate_weatherstation",
	})
	if got := client.PublishedOn("house/state/devices/climate_weatherstation"); len(got) == 0 {
		t.Fatal("per-device topic must be published on discovery (pre-condition)")
	}

	// Simulate broker retains being cleared (e.g. manual cleanup, broker
	// restart). Subsequent sensor readings arrive but generate no new derived
	// events — activity stays at "reporting" — so without a sweep the
	// retained message is never refreshed.
	client.Reset()

	pub.PublishSnapshot()

	got := client.PublishedOn("house/state/devices/climate_weatherstation")
	if len(got) == 0 {
		t.Error("PublishSnapshot must refresh per-device retained topics; passive sensors never republish via derived events")
	}
	if len(got) > 0 && !got[0].Retained {
		t.Error("per-device device topic published by PublishSnapshot must be retained")
	}
}

func TestPublisher_RecordedTopicShapeMatchesContract(t *testing.T) {
	// Catch any accidental topic-shape regression. The README documents
	// these as the public contract.
	pub, client, _ := newPublisherTest(t)
	pub.OnDerivedEvent(model.DerivedEvent{
		ID:        "evt_6",
		Timestamp: time.Now().UTC(),
		Type:      model.EvtHouseStateChanged,
		Evidence:  map[string]any{"state": "active"},
	})
	seen := map[string]bool{}
	for _, p := range client.Published {
		seen[p.Topic] = true
	}
	for _, want := range []string{"house/events/derived", "house/state/house", "house/state/snapshot"} {
		if !seen[want] {
			t.Errorf("missing publish on %q; saw %v", want, seen)
		}
	}
}
