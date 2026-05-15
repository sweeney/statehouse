package intercom

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

type capture struct {
	derived []model.DerivedEvent
}

func (c *capture) OnDerivedEvent(ev model.DerivedEvent) { c.derived = append(c.derived, ev) }

func (c *capture) types() []model.DerivedEventType {
	out := make([]model.DerivedEventType, len(c.derived))
	for i, ev := range c.derived {
		out[i] = ev.Type
	}
	return out
}

func (c *capture) has(t model.DerivedEventType) bool {
	for _, ev := range c.derived {
		if ev.Type == t {
			return true
		}
	}
	return false
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

func fixtureCfg() config.Config {
	cfg := config.Default()
	cfg.House.QuietAfter = 30 * time.Minute
	cfg.House.EmptyAfter = 2 * time.Hour
	return cfg
}

func replayIntercom(t *testing.T, path string, clock *testutil.FakeClock, a *Adapter) {
	t.Helper()
	events, err := testutil.LoadFixture(path)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, e := range events {
		if !e.Timestamp.IsZero() {
			clock.Set(e.Timestamp)
		}
		a.HandleMessage(e.Topic, e.PayloadBytes(), false)
	}
}

// TestFixture_CallAnswered verifies the full ringing→answered→hungup sequence:
// signal_asserted fired twice (ringing + answered upgrade), signal_cleared once,
// and the house was occupied while the call was active.
func TestFixture_CallAnswered(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 15, 11, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "asterisk", nil)
	a.SetDerivedSink(cap)

	replayIntercom(t, fixturePath("asterisk_call_answered.jsonl"), clock, a)

	// Two signal_asserted (ringing, then answered upgrade) + one signal_cleared.
	var asserted, cleared int
	for _, ev := range cap.derived {
		switch ev.Type {
		case model.EvtSignalAsserted:
			asserted++
		case model.EvtSignalCleared:
			cleared++
		}
	}
	if asserted != 2 {
		t.Errorf("expected 2 signal_asserted events (ringing + answered), got %d; events: %v", asserted, cap.types())
	}
	if cleared != 1 {
		t.Errorf("expected 1 signal_cleared on hungup, got %d", cleared)
	}

	// Rich intercom events should be present (sink was wired).
	if !cap.has(model.EvtIntercomRinging) {
		t.Error("expected intercom_ringing event")
	}
	if !cap.has(model.EvtIntercomAnswered) {
		t.Error("expected intercom_answered event")
	}
	if !cap.has(model.EvtIntercomHungup) {
		t.Error("expected intercom_hungup event")
	}

	// House state should have transitioned to occupied during the call.
	var sawOccupied bool
	for _, ev := range cap.derived {
		if ev.Type == model.EvtHouseStateChanged {
			if occ, _ := ev.Evidence["occupancy"].(string); occ == string(model.OccupancyOccupied) {
				sawOccupied = true
			}
		}
	}
	if !sawOccupied {
		t.Error("expected house_state_changed with occupancy=occupied during call")
	}

	// Activity record should reflect the full call lifecycle.
	records := store.RecentActivity(10)
	if len(records) != 1 {
		t.Fatalf("expected 1 activity record, got %d", len(records))
	}
	rec := records[0]
	if rec.Type != "call" {
		t.Errorf("expected record type 'call', got %q", rec.Type)
	}
	if rec.EndedAt == nil {
		t.Error("expected EndedAt set after hungup")
	}
	if rec.Meta["answered_at"] == nil {
		t.Error("expected answered_at in record meta")
	}
	if rec.Meta["talk_duration_seconds"] == nil {
		t.Error("expected talk_duration_seconds in record meta")
	}
}

// TestFixture_CallCancelled verifies that a ringing call that is cancelled
// (hungup before answered) still asserts then clears a signal, and that
// the house returns to non-occupied after the hangup.
func TestFixture_CallCancelled(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 15, 11, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "asterisk", nil)

	replayIntercom(t, fixturePath("asterisk_call_cancelled.jsonl"), clock, a)

	if !cap.has(model.EvtSignalAsserted) {
		t.Error("expected signal_asserted on ringing")
	}
	if !cap.has(model.EvtSignalCleared) {
		t.Error("expected signal_cleared on hungup")
	}

	// Immediately after hangup the house is still occupied — the signal's
	// lastSignalAt keeps it within the QuietAfter linger window.
	house := store.House()
	if house.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected house occupied within QuietAfter of hangup, got %q", house.Occupancy.State)
	}

	// Once the clock advances beyond QuietAfter, Tick prunes and recomputes.
	clock.Set(clock.Now().Add(cfg.House.QuietAfter + time.Minute))
	engine.Tick()
	house = store.House()
	if house.Occupancy.State == model.OccupancyOccupied {
		t.Errorf("expected house no longer occupied after QuietAfter expires, got %q", house.Occupancy.State)
	}

	// Activity record should show a cancelled call: EndedAt set, no answered_at,
	// cause present.
	records := store.RecentActivity(10)
	if len(records) != 1 {
		t.Fatalf("expected 1 activity record, got %d", len(records))
	}
	rec := records[0]
	if rec.EndedAt == nil {
		t.Error("expected EndedAt set on cancelled call record")
	}
	if rec.Meta["answered_at"] != nil {
		t.Error("expected no answered_at for cancelled call")
	}
	if rec.Meta["cause"] == nil {
		t.Error("expected cause in record meta for cancelled call")
	}
}

// TestFixture_ConcurrentCalls verifies that two overlapping calls each get
// their own signal, and that the house remains occupied until the second
// call also hangs up.
func TestFixture_ConcurrentCalls(t *testing.T) {
	cfg := fixtureCfg()
	store := state.NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 5, 15, 11, 0, 0, 0, time.UTC))
	engine := state.NewEngine(cfg, store, clock)
	cap := &capture{}
	engine.AddDerivedSink(cap)
	a := New(engine, "asterisk", nil)

	replayIntercom(t, fixturePath("asterisk_concurrent_calls.jsonl"), clock, a)

	// 3 asserted: call-A ringing, call-B ringing, call-A answered upgrade.
	// 2 cleared: call-B hungup, call-A hungup.
	var asserted, cleared int
	for _, ev := range cap.derived {
		switch ev.Type {
		case model.EvtSignalAsserted:
			asserted++
		case model.EvtSignalCleared:
			cleared++
		}
	}
	if asserted != 3 {
		t.Errorf("expected 3 signal_asserted events, got %d; events: %v", asserted, cap.types())
	}
	if cleared != 2 {
		t.Errorf("expected 2 signal_cleared events, got %d", cleared)
	}

	// Immediately after both hangups the house is still occupied — within QuietAfter.
	house := store.House()
	if house.Occupancy.State != model.OccupancyOccupied {
		t.Errorf("expected house occupied within QuietAfter of last hangup, got %q", house.Occupancy.State)
	}

	// Advance past QuietAfter: house should no longer be occupied.
	clock.Set(clock.Now().Add(cfg.House.QuietAfter + time.Minute))
	engine.Tick()
	house = store.House()
	if house.Occupancy.State == model.OccupancyOccupied {
		t.Errorf("expected house no longer occupied after QuietAfter expires, got %q", house.Occupancy.State)
	}

	// Both calls should have ended activity records.
	records := store.RecentActivity(10)
	if len(records) != 2 {
		t.Fatalf("expected 2 activity records for concurrent calls, got %d", len(records))
	}
	for _, rec := range records {
		if rec.EndedAt == nil {
			t.Errorf("expected EndedAt set on record %q", rec.ID)
		}
	}
}
