package mqtt

import (
	"encoding/json"
	"errors"
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
		Identity:    model.DeviceIdentity{IEEEAddress: "0x1", FriendlyName: "kitchen_dishwasher"},
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
	store.Upsert("k", model.Device{ID: "k", Class: device.ClassCyclePower, Identity: model.DeviceIdentity{FriendlyName: "k"}}, rt)
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
