package influx

import (
	"testing"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api/write"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
)

// fieldMap turns a *write.Point's FieldList into a name->value map.
// It uses the local write.Point API rather than serializing through
// the line protocol so tests don't depend on textual formatting.
func fieldMap(p *write.Point) map[string]any {
	out := make(map[string]any)
	for _, f := range p.FieldList() {
		out[f.Key] = f.Value
	}
	return out
}

// tagMap turns a *write.Point's TagList into a name->value map.
func tagMap(p *write.Point) map[string]string {
	out := make(map[string]string)
	for _, t := range p.TagList() {
		out[t.Key] = t.Value
	}
	return out
}

// seedDevice puts one device into the store so the writer's Store
// lookups succeed.
func seedDevice(t *testing.T, store *state.Store, id, class, location string) {
	t.Helper()
	rt := device.NewRuntime(device.Profile{Class: class}, 30*time.Minute)
	store.Upsert(id, model.Device{
		ID:       id,
		Class:    class,
		Location: location,
		Identity: model.DeviceIdentity{Scheme: "zigbee", Primary: "0x1", Display: id},
	}, rt)
}

func newWriterTest(t *testing.T) (*Writer, *FakeWriteAPI, *state.Store) {
	t.Helper()
	store := state.NewStore()
	api := NewFakeWriteAPI()
	w := NewWithAPI(api, store, nil)
	return w, api, store
}

func TestWriter_DisabledIsNoop(t *testing.T) {
	api := NewFakeWriteAPI()
	w := &Writer{Enabled: false, api: api, Store: state.NewStore()}
	w.OnCanonicalEvent(model.CanonicalEvent{DeviceID: "x", Attribute: "power_w", Value: 1.0, Timestamp: time.Now()})
	w.OnDerivedEvent(model.DerivedEvent{Type: model.EvtCycleFinished, DeviceID: "x"})
	if len(api.Points) != 0 {
		t.Fatalf("disabled writer must not write any points, got %d", len(api.Points))
	}
}

func TestWriter_PowerSampleGoesToDevicePower(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "kitchen_dishwasher", device.ClassCyclePower, "kitchen")
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	w.OnCanonicalEvent(model.CanonicalEvent{
		Timestamp: ts,
		DeviceID:  "kitchen_dishwasher",
		Attribute: "power_w",
		Value:     1840.2,
		Unit:      "W",
	})

	got := api.PointsForMeasurement("device_power")
	if len(got) != 1 {
		t.Fatalf("expected 1 device_power point, got %d", len(got))
	}
	p := got[0]
	if !p.Time().Equal(ts) {
		t.Errorf("timestamp mismatch: got %v want %v", p.Time(), ts)
	}
	tags := tagMap(p)
	if tags["device_id"] != "kitchen_dishwasher" || tags["class"] != device.ClassCyclePower || tags["location"] != "kitchen" {
		t.Errorf("tags wrong: %+v", tags)
	}
	fields := fieldMap(p)
	if fields["power_w"] != 1840.2 {
		t.Errorf("fields wrong: %+v", fields)
	}
}

func TestWriter_TempAndHumidityGoToDeviceEnvironment(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "hallway_sensor", "environment", "hall")
	ts := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	w.OnCanonicalEvent(model.CanonicalEvent{Timestamp: ts, DeviceID: "hallway_sensor", Attribute: "temperature_c", Value: 21.5})
	w.OnCanonicalEvent(model.CanonicalEvent{Timestamp: ts, DeviceID: "hallway_sensor", Attribute: "humidity_pct", Value: 55.0})
	if got := api.PointsForMeasurement("device_environment"); len(got) != 2 {
		t.Fatalf("expected 2 environment points, got %d", len(got))
	}
}

func TestWriter_BatterySampleGoesToDeviceBattery(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "bedroom_climate", device.ClassSensor, "bedroom")
	ts := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	w.OnCanonicalEvent(model.CanonicalEvent{
		Timestamp: ts,
		DeviceID:  "bedroom_climate",
		Attribute: "battery_pct",
		Value:     87.0,
	})
	got := api.PointsForMeasurement("device_battery")
	if len(got) != 1 {
		t.Fatalf("expected 1 device_battery point, got %d", len(got))
	}
	p := got[0]
	tags := tagMap(p)
	if tags["device_id"] != "bedroom_climate" || tags["class"] != device.ClassSensor || tags["location"] != "bedroom" {
		t.Errorf("battery tags wrong: %+v", tags)
	}
	if fieldMap(p)["battery_pct"] != 87.0 {
		t.Errorf("battery fields wrong: %+v", fieldMap(p))
	}
}

func TestWriter_UnsupportedAttributeIsDropped(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "x", "media_power_device", "")
	w.OnCanonicalEvent(model.CanonicalEvent{Timestamp: time.Now(), DeviceID: "x", Attribute: "state", Value: "ON"})
	if len(api.Points) != 0 {
		t.Fatalf("unsupported attribute must drop the point, got %d", len(api.Points))
	}
}

func TestWriter_NoDeviceInStoreIsDropped(t *testing.T) {
	// Writer.OnCanonicalEvent looks up the device for class/location
	// tags; if the device is unknown, no point should be written.
	w, api, _ := newWriterTest(t)
	w.OnCanonicalEvent(model.CanonicalEvent{Timestamp: time.Now(), DeviceID: "ghost", Attribute: "power_w", Value: 100.0})
	if len(api.Points) != 0 {
		t.Fatalf("expected no points for unknown device, got %d", len(api.Points))
	}
}

func TestWriter_CycleFinishedWritesApplianceCycle(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "kitchen_dishwasher", device.ClassCyclePower, "kitchen")
	ts := time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)
	w.OnDerivedEvent(model.DerivedEvent{
		Timestamp:   ts,
		Type:        model.EvtCycleFinished,
		DeviceID:    "kitchen_dishwasher",
		DeviceClass: device.ClassCyclePower,
		Evidence: map[string]any{
			"duration_seconds":    int64(5400),
			"selected_energy_kwh": 1.0,
			"energy_source":       "counter",
			"reported_kwh_delta":  1.0,
			"integrated_kwh":      0.28,
		},
	})
	got := api.PointsForMeasurement("appliance_cycle")
	if len(got) != 1 {
		t.Fatalf("expected 1 appliance_cycle point, got %d", len(got))
	}
	p := got[0]
	if tagMap(p)["device_id"] != "kitchen_dishwasher" {
		t.Errorf("device_id tag missing: %+v", tagMap(p))
	}
	if tagMap(p)["location"] != "kitchen" {
		t.Errorf("location tag missing: %+v", tagMap(p))
	}
	fields := fieldMap(p)
	if fields["selected_energy_kwh"] != 1.0 || fields["energy_source"] != "counter" {
		t.Errorf("fields wrong: %+v", fields)
	}
}

func TestWriter_CycleFinishedWithoutEvidenceIsDropped(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "k", device.ClassCyclePower, "")
	w.OnDerivedEvent(model.DerivedEvent{Type: model.EvtCycleFinished, DeviceID: "k"})
	if len(api.PointsForMeasurement("appliance_cycle")) != 0 {
		t.Fatalf("cycle_finished with no evidence must not write a point")
	}
}

func TestWriter_ActivityChangedWritesDeviceActivity(t *testing.T) {
	w, api, store := newWriterTest(t)
	seedDevice(t, store, "kettle", device.ClassShortBurst, "kitchen")
	w.OnDerivedEvent(model.DerivedEvent{
		Timestamp:   time.Now(),
		Type:        model.EvtDeviceActivityChanged,
		DeviceID:    "kettle",
		DeviceClass: device.ClassShortBurst,
		Evidence:    map[string]any{"from": "idle", "to": "active"},
	})
	got := api.PointsForMeasurement("device_activity")
	if len(got) != 1 {
		t.Fatalf("expected 1 device_activity point, got %d", len(got))
	}
	if fieldMap(got[0])["to"] != "active" {
		t.Errorf("expected to=active, got %+v", fieldMap(got[0]))
	}
}

func TestWriter_HouseStateChangedWritesHouseState(t *testing.T) {
	w, api, _ := newWriterTest(t)
	w.OnDerivedEvent(model.DerivedEvent{
		Timestamp: time.Now(),
		Type:      model.EvtHouseStateChanged,
		Evidence:  map[string]any{"state": "active", "confidence": 0.85},
	})
	got := api.PointsForMeasurement("house_state")
	if len(got) != 1 {
		t.Fatalf("expected 1 house_state point, got %d", len(got))
	}
	if tagMap(got[0])["state"] != "active" {
		t.Errorf("expected state=active tag, got %+v", tagMap(got[0]))
	}
	if fieldMap(got[0])["confidence"] != 0.85 {
		t.Errorf("expected confidence=0.85, got %+v", fieldMap(got[0]))
	}
}

func TestWriter_IrrelevantDerivedEventIsIgnored(t *testing.T) {
	w, api, _ := newWriterTest(t)
	w.OnDerivedEvent(model.DerivedEvent{Type: model.EvtDeviceDiscovered, DeviceID: "x"})
	if len(api.Points) != 0 {
		t.Fatalf("expected no points for device_discovered, got %d", len(api.Points))
	}
}

func TestEvidenceAsFields_FiltersUnsupportedTypes(t *testing.T) {
	in := map[string]any{
		"int":    42,
		"int64":  int64(7),
		"float":  3.14,
		"string": "hello",
		"bool":   true,
		// unsupported:
		"slice": []int{1, 2},
		"map":   map[string]int{"a": 1},
		"nil":   nil,
	}
	got := evidenceAsFields(in)
	for _, want := range []string{"int", "int64", "float", "string", "bool"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected key %q to survive filtering", want)
		}
	}
	for _, banned := range []string{"slice", "map", "nil"} {
		if _, ok := got[banned]; ok {
			t.Errorf("expected key %q to be filtered out", banned)
		}
	}
}

func TestEvidenceAsFields_EmptyInputReturnsNil(t *testing.T) {
	if got := evidenceAsFields(nil); got != nil {
		t.Errorf("expected nil for nil input, got %+v", got)
	}
	if got := evidenceAsFields(map[string]any{}); got != nil {
		t.Errorf("expected nil for empty map, got %+v", got)
	}
}

func TestWriter_StatsCountsQueuedCanonicalWrites(t *testing.T) {
	w, _, store := newWriterTest(t)
	seedDevice(t, store, "x", device.ClassMedia, "")
	for i := 0; i < 3; i++ {
		w.OnCanonicalEvent(model.CanonicalEvent{Timestamp: time.Now(), DeviceID: "x", Attribute: "power_w", Value: 50.0})
	}
	queued, failure := w.Stats()
	if queued != 3 {
		t.Errorf("expected 3 queued, got %d", queued)
	}
	if failure != 0 {
		t.Errorf("expected 0 failures, got %d", failure)
	}
}

func TestWriter_QueuedIncrementsAfterOnCanonicalEvent(t *testing.T) {
	w, _, store := newWriterTest(t)
	seedDevice(t, store, "sensor", device.ClassSensor, "living_room")

	queuedBefore, _ := w.Stats()
	w.OnCanonicalEvent(model.CanonicalEvent{
		Timestamp: time.Now(),
		DeviceID:  "sensor",
		Attribute: "temperature_c",
		Value:     22.0,
	})
	queuedAfter, _ := w.Stats()

	if queuedAfter != queuedBefore+1 {
		t.Errorf("expected queued to increment by 1: before=%d after=%d", queuedBefore, queuedAfter)
	}
}

func TestWriter_CloseFlushesFakeAPI(t *testing.T) {
	w, api, _ := newWriterTest(t)
	w.Close()
	if api.Flushed != 1 {
		t.Errorf("expected 1 flush on Close, got %d", api.Flushed)
	}
}
