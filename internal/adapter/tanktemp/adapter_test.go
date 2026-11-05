package tanktemp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/state"
	"github.com/sweeney/statehouse/internal/testutil"
)

// The goldens in testdata/ are copied verbatim from tanktemp's
// internal/payload/testdata, which pins the wire contract.

const (
	base       = "energy/hot_water"
	stateTopic = base + "/hot-water-cylinder/state"
	statusTop  = base + "/status"
)

// Sweep times in the goldens.
var (
	okSweep = time.Date(2026, 11, 2, 21, 6, 26, 0, time.UTC)
)

func cfg() config.Config {
	no := false
	c := config.Default()
	c.Availability.OfflineDebounce = 30 * time.Second
	c.Devices = map[string]config.DeviceConfig{
		"hot-water-cylinder": {
			Class: "hot_water_cylinder", DisplayName: "Hot water cylinder",
			Telemetry: &no, Room: "first.cupboard",
		},
		"probe_hot-water-cylinder_top": {
			Class: device.ClassApplianceProbe, DisplayName: "Cylinder: top",
			Scheme: SchemeName, Primary: "hot-water-cylinder.top", PartOf: "hot-water-cylinder",
		},
		"probe_hot-water-cylinder_bottom": {
			Class: device.ClassApplianceProbe, DisplayName: "Cylinder: bottom",
			Scheme: SchemeName, Primary: "hot-water-cylinder.bottom", PartOf: "hot-water-cylinder",
		},
	}
	return c
}

type collector struct {
	canonical []model.CanonicalEvent
}

func (c *collector) OnDerivedEvent(model.DerivedEvent)        {}
func (c *collector) OnCanonicalEvent(ev model.CanonicalEvent) { c.canonical = append(c.canonical, ev) }

// temps returns the temperature_c values written, by device id.
func (c *collector) temps() map[string][]float64 {
	out := map[string][]float64{}
	for _, ev := range c.canonical {
		if ev.Attribute == "temperature_c" {
			out[ev.DeviceID] = append(out[ev.DeviceID], ev.Value.(float64))
		}
	}
	return out
}

func mk(t *testing.T, now time.Time) (*Adapter, *state.Engine, *state.Store, *collector, *testutil.FakeClock) {
	t.Helper()
	store := state.NewStore()
	clock := testutil.NewFakeClock(now)
	engine := state.NewEngine(cfg(), store, clock)
	col := &collector{}
	engine.AddCanonicalSink(col)
	return New(engine, base, nil), engine, store, col, clock
}

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSubscriptions(t *testing.T) {
	a, _, _, _, _ := mk(t, okSweep)
	got := a.Subscriptions()
	want := []string{base + "/+/state", base + "/status"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Subscriptions() = %v, want %v", got, want)
	}
	if a.Name() != "tanktemp" {
		t.Fatalf("Name() = %q", a.Name())
	}
}

func TestDefaultBase(t *testing.T) {
	if got := New(nil, "", nil).Subscriptions()[1]; got != "energy/hot_water/status" {
		t.Fatalf("default status topic = %q", got)
	}
}

func TestStateOK_OneDevicePerProbe(t *testing.T) {
	a, _, store, col, _ := mk(t, okSweep.Add(5*time.Second))
	a.HandleMessage(stateTopic, golden(t, "state-ok"), false)

	temps := col.temps()
	if got := temps["probe_hot-water-cylinder_bottom"]; len(got) != 1 || got[0] != 12.0625 {
		t.Errorf("bottom = %v, want [12.0625]", got)
	}
	if got := temps["probe_hot-water-cylinder_top"]; len(got) != 1 || got[0] != 61.185 {
		t.Errorf("top = %v, want [61.185] (temp_c, which already includes the calibration)", got)
	}
	for _, ev := range col.canonical {
		if !ev.Timestamp.Equal(okSweep) {
			t.Errorf("reading at %v, want the sweep timestamp %v", ev.Timestamp, okSweep)
		}
	}
	devs := store.Devices()
	top := devs["probe_hot-water-cylinder_top"]
	if top.Class != device.ClassApplianceProbe {
		t.Errorf("top class = %q, want appliance_probe", top.Class)
	}
	if top.Identity.Scheme != "tanktemp" || top.Identity.Primary != "hot-water-cylinder.top" {
		t.Errorf("top identity = %+v", top.Identity)
	}
	if top.Availability != model.AvailabilityOnline {
		t.Errorf("top availability = %q, want online", top.Availability)
	}
	if _, ok := devs["hot-water-cylinder"]; ok {
		t.Error("the cylinder (telemetry: false) must never become an engine device")
	}
	if len(devs) != 2 {
		t.Errorf("devices = %d, want the two probes", len(devs))
	}
}

// state-failures: a crc failure, a not_present probe, a suspect 85 °C and
// clock_synced false.
func TestStateFailures_WritesOnlyValues(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	a, _, store, col, _ := mk(t, now)
	a.HandleMessage(stateTopic, golden(t, "state-failures"), false)

	temps := col.temps()
	if len(temps) != 1 {
		t.Fatalf("wrote temperatures for %v, want only the top probe", temps)
	}
	if got := temps["probe_hot-water-cylinder_top"]; len(got) != 1 || got[0] != 85 {
		t.Errorf("top = %v, want the suspect 85 written as is", got)
	}
	// clock_synced false: the receive time, not the published timestamp.
	if ts := col.canonical[0].Timestamp; !ts.Equal(now) {
		t.Errorf("reading at %v, want receive time %v", ts, now)
	}
	// Failing probes are registered but have no value, never 0.
	devs := store.Devices()
	bottom, ok := devs["probe_hot-water-cylinder_bottom"]
	if !ok {
		t.Fatalf("failing bottom probe not registered; have %d devices", len(devs))
	}
	if bottom.Latest.TemperatureC != nil {
		t.Errorf("bottom temperature = %v, want none", *bottom.Latest.TemperatureC)
	}
	if len(devs) != 3 {
		t.Errorf("devices = %d, want all three probes, failing or not", len(devs))
	}
}

func TestReplayNotWrittenTwice(t *testing.T) {
	a, _, _, col, clock := mk(t, okSweep.Add(5*time.Second))
	a.HandleMessage(stateTopic, golden(t, "state-ok"), false)
	clock.Advance(10 * time.Second)
	a.HandleMessage(stateTopic, golden(t, "state-ok"), true) // reconnect replay
	if n := len(col.temps()["probe_hot-water-cylinder_top"]); n != 1 {
		t.Fatalf("top written %d times, want once", n)
	}
}

func TestRetainedState(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		want int
	}{
		{"fresh", 60 * time.Second, 1},
		{"at the limit", staleAfter, 1},
		{"stale", staleAfter + time.Second, 0},
		{"a day old", 24 * time.Hour, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, store, col, _ := mk(t, okSweep.Add(tc.age))
			a.HandleMessage(stateTopic, golden(t, "state-ok"), true)
			if n := len(col.temps()["probe_hot-water-cylinder_top"]); n != tc.want {
				t.Fatalf("readings = %d, want %d", n, tc.want)
			}
			if _, ok := store.Devices()["probe_hot-water-cylinder_top"]; !ok {
				t.Error("probe not registered from a retained state")
			}
		})
	}
}

func TestRetainedUnsyncedStateDropped(t *testing.T) {
	a, _, _, col, _ := mk(t, time.Date(2026, 11, 2, 21, 7, 0, 0, time.UTC))
	a.HandleMessage(stateTopic, golden(t, "state-failures"), true)
	if len(col.canonical) != 0 {
		t.Fatalf("a retained state with an unsynced clock has no trustworthy age; wrote %v", col.temps())
	}
}

func TestRejects(t *testing.T) {
	ok := string(golden(t, "state-ok"))
	cases := []struct{ name, topic, payload string }{
		{"device_id differs from topic", base + "/other/state", ok},
		{"bad device_id in topic", base + "/Bad.Id/state", ok},
		{"other prefix", "test/energy/hot_water/hot-water-cylinder/state", ok},
		{"deeper topic", base + "/hot-water-cylinder/x/state", ok},
		{"not json", stateTopic, "{"},
		{"out of range", stateTopic, `{"device_id":"hot-water-cylinder","timestamp":"2026-11-02T21:06:26Z","clock_synced":true,"sensors":[{"position":"top","temp_c":300,"error":null}]}`},
		{"bad position", stateTopic, `{"device_id":"hot-water-cylinder","timestamp":"2026-11-02T21:06:26Z","clock_synced":true,"sensors":[{"position":"Top.x","temp_c":60,"error":null}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _, col, _ := mk(t, okSweep.Add(5*time.Second))
			a.HandleMessage(tc.topic, []byte(tc.payload), false)
			if len(col.canonical) != 0 {
				t.Fatalf("wrote %v", col.temps())
			}
		})
	}
}

func TestOfflineMarksProbesOffline(t *testing.T) {
	for _, name := range []string{"lwt", "status-shutdown"} {
		t.Run(name, func(t *testing.T) {
			a, engine, store, _, clock := mk(t, okSweep.Add(5*time.Second))
			a.HandleMessage(stateTopic, golden(t, "state-ok"), false)
			a.HandleMessage(statusTop, golden(t, name), true)
			clock.Advance(31 * time.Second)
			engine.Tick()
			for id, d := range store.Devices() {
				if d.Availability != model.AvailabilityOffline {
					t.Errorf("%s availability = %q, want offline", id, d.Availability)
				}
			}
		})
	}
}

func TestOnlineStatusChangesNothing(t *testing.T) {
	for _, name := range []string{"status-startup", "status-heartbeat"} {
		t.Run(name, func(t *testing.T) {
			a, _, store, col, _ := mk(t, okSweep.Add(5*time.Second))
			a.HandleMessage(statusTop, golden(t, name), true)
			// Publisher health (unexpected ROMs, startup problems) never
			// creates devices.
			if n := len(store.Devices()); n != 0 {
				t.Errorf("status created %d devices", n)
			}
			if len(col.canonical) != 0 {
				t.Errorf("status wrote readings")
			}
		})
	}
}

// The data-line fault from tanktemp's STATEHOUSE.md: good, bus_low,
// not_present, good. Only the two good sweeps write anything.
func TestDataLineFault(t *testing.T) {
	start := time.Date(2026, 11, 2, 16, 51, 0, 0, time.UTC)
	a, _, _, col, clock := mk(t, start)
	sweep := func(i int, vals [3]string, errCode string) {
		ts := start.Add(time.Duration(i) * 30 * time.Second)
		clock.Set(ts.Add(4 * time.Second))
		p := fmt.Sprintf(`{"device_id":"cyl","timestamp":%q,"clock_synced":true,"sweep_ms":4000,"sensors":[`, ts.Format(time.RFC3339))
		for j, pos := range []string{"left", "middle", "right"} {
			if j > 0 {
				p += ","
			}
			e := "null"
			if errCode != "" {
				e = fmt.Sprintf("%q", errCode)
			}
			p += fmt.Sprintf(`{"position":%q,"order":%d,"height_mm":null,"rom":"28-00000000000%d","temp_c":%s,"raw_c":%s,"offset_c":0,"error":%s,"suspect":null}`,
				pos, j+1, j, vals[j], vals[j], e)
		}
		a.HandleMessage(base+"/cyl/state", []byte(p+"]}"), false)
	}
	sweep(0, [3]string{"20.5625", "20.3125", "20.25"}, "")
	sweep(1, [3]string{"null", "null", "null"}, "bus_low")
	sweep(2, [3]string{"null", "null", "null"}, "not_present")
	sweep(3, [3]string{"20.5625", "20.25", "20.1875"}, "")

	temps := col.temps()
	if len(temps) != 3 {
		t.Fatalf("probes with readings = %d, want 3: %v", len(temps), temps)
	}
	for id, vs := range temps {
		if len(vs) != 2 {
			t.Errorf("%s: %v, want two readings (the good sweeps)", id, vs)
		}
		for _, v := range vs {
			if v == 0 {
				t.Errorf("%s: a 0 was written for a failed read", id)
			}
		}
	}
}
