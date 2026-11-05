package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
	"github.com/sweeney/statehouse/internal/testutil"
)

// A telemetry: false entry (a hot water cylinder, a fridge) sends nothing
// itself, so it must never show up in anything about liveness or readings.
// Statehouse gets this for free: a device enters the store only when an
// adapter reports an identity, and such an entry deliberately has none, so
// the resolver indexes nothing for it. This test pins that, so a future
// "list every configured device" change has to decide about these entries
// rather than inherit a permanently "unknown" cylinder.
func TestEngine_TelemetryFalseEntryNeverEntersStore(t *testing.T) {
	no := false
	cfg := config.Default()
	cfg.Devices = map[string]config.DeviceConfig{
		"hot-water-cylinder": {
			Class:       "hot_water_cylinder",
			DisplayName: "Hot water cylinder",
			Telemetry:   &no,
			Room:        "first.cupboard",
		},
		"probe_hot-water-cylinder_top": {
			Class:       device.ClassApplianceProbe,
			DisplayName: "Cylinder: top",
			Scheme:      "tanktemp",
			Primary:     "hot-water-cylinder.top",
			PartOf:      "hot-water-cylinder",
		},
	}
	store := NewStore()
	clock := testutil.NewFakeClock(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	engine := NewEngine(cfg, store, clock)

	probe := model.DeviceIdentity{Scheme: "tanktemp", Primary: "hot-water-cylinder.top", Display: "hot-water-cylinder.top"}
	engine.IngestReading(probe, "energy/hot_water/hot-water-cylinder/state",
		model.Reading{Timestamp: clock.Now(), TemperatureC: ptr(61.2)})
	clock.Advance(10 * time.Minute)
	engine.Tick()

	devs := store.Devices()
	if _, ok := devs["hot-water-cylinder"]; ok {
		t.Fatalf("telemetry: false cylinder entered the store: %+v", devs["hot-water-cylinder"])
	}
	if _, ok := store.ProfiledDevices()["hot-water-cylinder"]; ok {
		t.Fatal("telemetry: false cylinder has a runtime profile")
	}
	got, ok := devs["probe_hot-water-cylinder_top"]
	if !ok {
		t.Fatalf("probe not stored under its configured id; have %v", keys(devs))
	}
	if got.Class != device.ClassApplianceProbe {
		t.Errorf("probe class = %q, want %q", got.Class, device.ClassApplianceProbe)
	}
	if len(devs) != 1 {
		t.Errorf("store holds %d devices, want only the probe: %v", len(devs), keys(devs))
	}
}

func keys(m map[string]model.Device) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
