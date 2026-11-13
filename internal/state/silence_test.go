package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// Tests for the silence rule and reading-age gating (#93).

var weatherID = model.DeviceIdentity{Scheme: "climate", Primary: "home", Display: "home"}

// mkSilenceEngine builds an engine with one configured environmental sensor
// (class staleness 3600s) and the given silence multiplier, so its silence
// window is multiplier hours.
func mkSilenceEngine(t *testing.T, multiplier float64) (*Engine, *Store, *collector, *testutil.FakeClock) {
	t.Helper()
	cfg := config.Default()
	cfg.Availability.SilenceMultiplier = multiplier
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{"environmental_sensor": {}}
	cfg.Devices = map[string]config.DeviceConfig{
		"climate_weatherstation": {Scheme: "climate", Primary: "home", Class: "environmental_sensor"},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)
	col := &collector{}
	engine.AddDerivedSink(col)
	return engine, store, col, clock
}

func weather(t *testing.T, store *Store) model.Device {
	t.Helper()
	d, ok := store.Get("climate_weatherstation")
	if !ok {
		t.Fatal("climate_weatherstation not in store")
	}
	return d
}

func wantState(t *testing.T, d model.Device, avail model.Availability, act model.DeviceActivityState) {
	t.Helper()
	if d.Availability != avail || d.Activity.State != act {
		t.Fatalf("availability=%s activity=%s, want %s/%s", d.Availability, d.Activity.State, avail, act)
	}
}

// availEvents returns the availability values announced, in order.
func availEvents(c *collector) []string {
	var out []string
	for _, ev := range c.derived {
		if ev.Type == model.EvtDeviceAvailabilityChanged {
			out = append(out, ev.Evidence["availability"].(string))
		}
	}
	return out
}

// activityEvents returns the "to" of each activity change announced, in order.
func activityEvents(c *collector) []string {
	var out []string
	for _, ev := range c.derived {
		if ev.Type == model.EvtDeviceActivityChanged {
			out = append(out, ev.Evidence["to"].(string))
		}
	}
	return out
}

func temp(ts time.Time) model.Reading {
	return model.Reading{Timestamp: ts, TemperatureC: ptr(12.5)}
}

func TestSilence_LiveReadingMarksOnlineAndAnnouncesIt(t *testing.T) {
	engine, store, col, clock := mkSilenceEngine(t, 10)
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
	if got := availEvents(col); len(got) != 1 || got[0] != "online" {
		t.Errorf("availability events = %v, want [online]", got)
	}
}

func TestSilence_SilentDeviceGoesUnknownThenRecovers(t *testing.T) {
	engine, store, col, clock := mkSilenceEngine(t, 10)
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))

	// Within the window (10 × 3600s): still online.
	clock.Advance(10 * time.Hour)
	engine.Tick()
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)

	clock.Advance(time.Second)
	engine.Tick()
	wantState(t, weather(t, store), model.AvailabilityUnknown, model.ActivityUnknown)
	if d := weather(t, store); d.Latest.TemperatureC == nil {
		t.Error("silence must keep the last values in latest")
	}

	// Ticking again changes nothing and announces nothing.
	before := len(col.derived)
	engine.Tick()
	for _, ev := range col.derived[before:] {
		if ev.Type == model.EvtDeviceAvailabilityChanged || ev.Type == model.EvtDeviceActivityChanged {
			t.Errorf("repeat tick announced %s", ev.Type)
		}
	}

	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)

	if got, want := availEvents(col), []string{"online", "unknown", "online"}; !equal(got, want) {
		t.Errorf("availability events = %v, want %v", got, want)
	}
	if got, want := activityEvents(col), []string{"reporting", "unknown", "reporting"}; !equal(got, want) {
		t.Errorf("activity events = %v, want %v", got, want)
	}
}

// The #93 restart: the only thing a fresh engine hears is a retained
// observation from 16 days ago. It is recorded but is not evidence the
// device is up, and silence then leaves it alone.
func TestSilence_OldReadingAfterRestartStaysUnknown(t *testing.T) {
	engine, store, col, clock := mkSilenceEngine(t, 10)
	observed := clock.Now()
	clock.Advance(16 * 24 * time.Hour)

	engine.IngestReading(weatherID, "climate/home/observation", temp(observed))
	d := weather(t, store)
	wantState(t, d, model.AvailabilityUnknown, model.ActivityUnknown)
	if !d.Latest.LastSeen.Equal(observed) {
		t.Errorf("LastSeen = %v, want the publisher's %v", d.Latest.LastSeen, observed)
	}
	if d.Latest.TemperatureC == nil {
		t.Error("an old reading's values should still be recorded")
	}

	engine.Tick()
	wantState(t, weather(t, store), model.AvailabilityUnknown, model.ActivityUnknown)
	if got := availEvents(col); len(got) != 0 {
		t.Errorf("availability events = %v, want none", got)
	}
	if got := activityEvents(col); len(got) != 0 {
		t.Errorf("activity events = %v, want none", got)
	}

	// The sensor comes back.
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
}

// A reading inside the window still counts, even if it is not brand new.
func TestSilence_RecentRetainedReadingCounts(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 10)
	observed := clock.Now()
	clock.Advance(2 * time.Hour)
	engine.IngestReading(weatherID, "climate/home/observation", temp(observed))
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
}

func TestSilence_UndatedReplayIsNotEvidence(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 10)
	r := temp(clock.Now())
	r.UndatedReplay = true
	engine.IngestReading(weatherID, "climate/home/observation", r)
	d := weather(t, store)
	wantState(t, d, model.AvailabilityUnknown, model.ActivityUnknown)
	if !d.Latest.LastSeen.IsZero() {
		t.Errorf("an undated replay must not set LastSeen, got %v", d.Latest.LastSeen)
	}
	if d.Latest.TemperatureC == nil {
		t.Error("an undated replay's values should still be recorded")
	}
}

// An old reading must not knock an online device back to unknown: it is
// only withheld as evidence, never taken as evidence against.
func TestSilence_OldReadingDoesNotDemoteOnlineDevice(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 10)
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	live := clock.Now()
	r := temp(clock.Now().Add(-48 * time.Hour))
	engine.IngestReading(weatherID, "climate/home/observation", r)
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)

	// LastSeen must not move backwards, or the next tick measures silence
	// from the old reading and demotes a device that is up (PR #94 review).
	clock.Advance(time.Minute)
	engine.Tick()
	d := weather(t, store)
	wantState(t, d, model.AvailabilityOnline, model.ActivityReporting)
	if !d.Latest.LastSeen.Equal(live) {
		t.Errorf("LastSeen = %v, want it kept at %v", d.Latest.LastSeen, live)
	}
}

// Entering unknown resets activity confidence; it is not 0.9 sure of
// "unknown" (PR #94 review).
func TestSilence_UnknownActivityHasNoConfidence(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 10)
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	if c := weather(t, store).Activity.Confidence; c == 0 {
		t.Fatalf("precondition: reporting activity should carry confidence, got %v", c)
	}
	clock.Advance(11 * time.Hour)
	engine.Tick()
	if c := weather(t, store).Activity.Confidence; c != 0 {
		t.Errorf("silenced: confidence = %v, want 0", c)
	}

	// The same through the reading path: an online device that has been
	// silenced, then sent an old reading, stays unknown with no confidence.
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now().Add(-48*time.Hour)))
	if d := weather(t, store); d.Activity.State != model.ActivityUnknown || d.Activity.Confidence != 0 {
		t.Errorf("after old reading: activity = %s @ %v, want unknown @ 0", d.Activity.State, d.Activity.Confidence)
	}
}

func TestSilence_ExplicitAvailabilityIsNotOverridden(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 10)
	engine.SetAvailability(weatherID, "climate/home/availability", model.AvailabilityOnline)
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	clock.Advance(30 * 24 * time.Hour)
	engine.Tick()
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
}

// Explicit online after an old replay restores the activity the runtime has.
func TestSilence_ExplicitOnlineReleasesHeldActivity(t *testing.T) {
	engine, store, col, clock := mkSilenceEngine(t, 10)
	r := temp(clock.Now())
	r.UndatedReplay = true
	engine.IngestReading(weatherID, "climate/home/observation", r)
	wantState(t, weather(t, store), model.AvailabilityUnknown, model.ActivityUnknown)

	engine.SetAvailability(weatherID, "climate/home/availability", model.AvailabilityOnline)
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
	if got := activityEvents(col); !equal(got, []string{"reporting"}) {
		t.Errorf("activity events = %v, want [reporting]", got)
	}
}

func TestSilence_MultiplierZeroTurnsTheRuleOff(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 0)
	observed := clock.Now()
	clock.Advance(16 * 24 * time.Hour)
	engine.IngestReading(weatherID, "climate/home/observation", temp(observed))
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
	clock.Advance(30 * 24 * time.Hour)
	engine.Tick()
	wantState(t, weather(t, store), model.AvailabilityOnline, model.ActivityReporting)
}

func TestSilence_ClassStalenessOverrideScalesTheWindow(t *testing.T) {
	engine, store, _, clock := mkSilenceEngine(t, 10)
	cfg := engine.snap().cfg
	cfg.DeviceClasses = map[string]config.DeviceClassConfig{
		"environmental_sensor": {StalenessSeconds: ptr(60)},
	}
	engine.ReloadConfig(cfg)
	engine.IngestReading(weatherID, "climate/home/observation", temp(clock.Now()))
	clock.Advance(10*time.Minute + time.Second)
	engine.Tick()
	wantState(t, weather(t, store), model.AvailabilityUnknown, model.ActivityUnknown)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
