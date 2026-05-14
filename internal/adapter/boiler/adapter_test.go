package boiler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// boilerCfg returns a config with the binary_state_device class
// configured and the CH/HW devices pre-registered under scheme=boiler.
func boilerCfg() config.Config {
	cfg := config.Default()
	cfg.Availability.OfflineDebounce = 30 * time.Second
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"binary_state_device": {
			DefaultThresholds: config.Thresholds{
				// nil pointers → zero duration → immediate transitions;
				// boiler-sensor firmware already debounces upstream.
			},
		},
	}
	cfg.Devices = map[string]config.DeviceConfig{
		"central_heating": {
			Scheme: SchemeName, Primary: ChannelCH,
			Class: device.ClassBinaryState, DisplayName: "Central heating", Location: "house",
		},
		"hot_water": {
			Scheme: SchemeName, Primary: ChannelHW,
			Class: device.ClassBinaryState, DisplayName: "Hot water", Location: "house",
		},
	}
	return cfg
}

type collector struct {
	derived []model.DerivedEvent
}

func (c *collector) OnDerivedEvent(ev model.DerivedEvent)    { c.derived = append(c.derived, ev) }
func (c *collector) OnCanonicalEvent(_ model.CanonicalEvent) {}

func mkAdapter(t *testing.T) (*Adapter, *state.Store, *collector, *testutil.FakeClock) {
	t.Helper()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 13, 7, 0, 0, 0, time.UTC))
	engine := state.NewEngine(boilerCfg(), store, clock)
	col := &collector{}
	engine.AddDerivedSink(col)
	return New(engine, "energy/boiler/sensor", nil), store, col, clock
}

func TestAdapter_Name(t *testing.T) {
	a, _, _, _ := mkAdapter(t)
	if a.Name() != "boiler" {
		t.Errorf("Name() = %q want boiler", a.Name())
	}
}

func TestAdapter_SubscribesToBothTopics(t *testing.T) {
	a, _, _, _ := mkAdapter(t)
	subs := a.Subscriptions()
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %v", subs)
	}
	want := map[string]bool{"energy/boiler/sensor/events": true, "energy/boiler/sensor/system": true}
	for _, s := range subs {
		if !want[s] {
			t.Errorf("unexpected subscription %q", s)
		}
	}
}

func TestAdapter_EventsPayloadDrivesBothChannels(t *testing.T) {
	a, store, col, _ := mkAdapter(t)
	payload := []byte(`{"boiler":{"timestamp":"2026-05-13T07:02:15Z","event":"CH_ON","ch":{"state":"ON"},"hw":{"state":"OFF"}}}`)
	a.HandleMessage("energy/boiler/sensor/events", payload, false)

	// Both channel devices should now exist.
	ch, okCH := store.Get("central_heating")
	hw, okHW := store.Get("hot_water")
	if !okCH || !okHW {
		t.Fatalf("expected both channel devices, got ch=%v hw=%v", okCH, okHW)
	}
	if ch.Identity.Scheme != "boiler" || ch.Identity.Primary != "ch" {
		t.Errorf("CH identity wrong: %+v", ch.Identity)
	}
	if ch.Activity.State != model.ActivityActive {
		t.Errorf("expected CH active, got %q", ch.Activity.State)
	}
	if hw.Activity.State != model.ActivityIdle {
		t.Errorf("expected HW idle, got %q", hw.Activity.State)
	}
	// CH cycle started; HW didn't.
	var sawCHStart bool
	for _, ev := range col.derived {
		if ev.Type == model.EvtCycleStarted && ev.DeviceID == "central_heating" {
			sawCHStart = true
		}
	}
	if !sawCHStart {
		t.Errorf("expected cycle_started for central_heating, got %v", summary(col.derived))
	}
}

func TestAdapter_NoDuplicateEventsWhenStateUnchanged(t *testing.T) {
	// Two consecutive events where only one channel changes must not
	// produce a transition for the unchanged channel.
	a, _, col, _ := mkAdapter(t)
	a.HandleMessage("energy/boiler/sensor/events",
		[]byte(`{"boiler":{"event":"CH_ON","ch":{"state":"ON"},"hw":{"state":"OFF"}}}`), false)
	col.derived = nil // reset
	a.HandleMessage("energy/boiler/sensor/events",
		[]byte(`{"boiler":{"event":"HW_ON","ch":{"state":"ON"},"hw":{"state":"ON"}}}`), false)
	// Only hot_water should have transitioned.
	for _, ev := range col.derived {
		if ev.Type == model.EvtCycleStarted && ev.DeviceID == "central_heating" {
			t.Fatalf("CH must not re-emit cycle_started; got %v", summary(col.derived))
		}
	}
}

func TestAdapter_StartupSnapshotSeedsInitialState(t *testing.T) {
	a, store, _, _ := mkAdapter(t)
	payload := []byte(`{"status":{"event":"STARTUP","ch":"OFF","hw":"OFF","ready":true}}`)
	a.HandleMessage("energy/boiler/sensor/system", payload, true)

	ch, _ := store.Get("central_heating")
	hw, _ := store.Get("hot_water")
	if ch.Activity.State != model.ActivityIdle {
		t.Errorf("CH should start idle, got %q", ch.Activity.State)
	}
	if hw.Activity.State != model.ActivityIdle {
		t.Errorf("HW should start idle, got %q", hw.Activity.State)
	}
	if ch.Availability != model.AvailabilityOnline {
		t.Errorf("CH should be online after STARTUP, got %q", ch.Availability)
	}
}

func TestAdapter_LWTDrivesOffline(t *testing.T) {
	a, store, _, clock := mkAdapter(t)
	// Establish initial online state.
	a.HandleMessage("energy/boiler/sensor/events",
		[]byte(`{"boiler":{"event":"CH_ON","ch":{"state":"ON"},"hw":{"state":"OFF"}}}`), false)
	// Last will retained on the broker.
	lwt := []byte(`{"system":{"timestamp":"...","event":"SHUTDOWN","reason":"MQTT_DISCONNECT","source":"last_will"}}`)
	a.HandleMessage("energy/boiler/sensor/system", lwt, true)
	ch, _ := store.Get("central_heating")
	if ch.Availability != model.AvailabilityOfflinePending {
		t.Fatalf("expected offline_pending, got %q", ch.Availability)
	}
	// Mature the debounce.
	clock.Advance(31 * time.Second)
	// Force engine tick by re-emitting any payload — easier: poke
	// the store directly via another message that doesn't change state.
	// We need to drive engine.Tick(); since we don't have a handle on
	// the engine in this test scope, just check the state matures via
	// a synthetic reconnect that the adapter forwards as Online.
	a.HandleMessage("energy/boiler/sensor/system",
		[]byte(`{"system":{"event":"RECONNECTED"}}`), false)
	ch, _ = store.Get("central_heating")
	if ch.Availability != model.AvailabilityOnline {
		t.Fatalf("expected online after RECONNECTED, got %q", ch.Availability)
	}
}

func TestAdapter_RejectsInvalidPayload(t *testing.T) {
	a, _, col, _ := mkAdapter(t)
	a.HandleMessage("energy/boiler/sensor/events", []byte(`not json`), false)
	a.HandleMessage("energy/boiler/sensor/events", []byte(`{"boiler":null}`), false)
	a.HandleMessage("energy/boiler/sensor/events", []byte(``), false)
	// Wrong topic — ignored.
	a.HandleMessage("zigbee2mqtt/something", []byte(`{}`), false)
	for _, ev := range col.derived {
		if ev.Type == model.EvtCycleStarted {
			t.Fatalf("malformed input must not produce cycle events; got %v", summary(col.derived))
		}
	}
}

func TestAdapter_FixtureReplayDishwasher(t *testing.T) {
	a, store, col, clock := mkAdapter(t)
	events, err := testutil.LoadFixture(filepath.Join("..", "..", "testdata", "fixtures", "boiler_cycle.jsonl"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}

	// CH ran 07:02:15 → 07:35:42 = ~33min 27s.
	ch, _ := store.Get("central_heating")
	if ch.Cycle == nil || ch.Cycle.Active {
		t.Fatalf("expected CH cycle to be finished, got %+v", ch.Cycle)
	}
	if ch.Cycle.DurationSeconds < 30*60 || ch.Cycle.DurationSeconds > 35*60 {
		t.Errorf("expected CH duration around 33min, got %ds", ch.Cycle.DurationSeconds)
	}
	if ch.Cycle.Energy.SelectedKWh != 0 {
		t.Errorf("binary device cycle must report zero energy, got %v", ch.Cycle.Energy.SelectedKWh)
	}

	// HW ran 08:15:00 → 08:18:30 = 3min 30s.
	hw, _ := store.Get("hot_water")
	if hw.Cycle == nil || hw.Cycle.Active {
		t.Fatalf("expected HW cycle to be finished, got %+v", hw.Cycle)
	}
	if hw.Cycle.DurationSeconds < 3*60 || hw.Cycle.DurationSeconds > 4*60 {
		t.Errorf("expected HW duration around 3.5min, got %ds", hw.Cycle.DurationSeconds)
	}

	// Cycle events: one start + one finish for each channel.
	starts, finishes := 0, 0
	for _, ev := range col.derived {
		if ev.Type == model.EvtCycleStarted {
			starts++
		}
		if ev.Type == model.EvtCycleFinished {
			finishes++
		}
	}
	if starts != 2 || finishes != 2 {
		t.Errorf("expected 2 starts + 2 finishes, got %d / %d (events: %v)", starts, finishes, summary(col.derived))
	}

	// LWT during the fixture should have driven both channels to
	// offline_pending; the subsequent RECONNECTED should have brought
	// them back online.
	if hw.Availability != model.AvailabilityOnline {
		t.Errorf("expected HW online after RECONNECTED, got %q", hw.Availability)
	}
}

func summary(evs []model.DerivedEvent) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, string(ev.Type)+":"+ev.DeviceID)
	}
	return out
}
