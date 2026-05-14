package device

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/energy"
	"github.com/sweeney/statehouse/internal/model"
)

func mkRuntime(class string, th config.Thresholds, strategy energy.Strategy) *Runtime {
	p := Profile{
		Class:      class,
		Thresholds: th,
		Strategy:   strategy,
	}
	return NewRuntime(p, 30*time.Minute)
}

func ptrF(v float64) *float64               { return &v }
func ptrDur(v time.Duration) *time.Duration { return &v }

func TestShortBurst_HysteresisAvoidsFalseStart(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassShortBurst, config.Thresholds{
		IdleBelowW:           ptrF(5),
		ActiveAboveW:         ptrF(50),
		ActiveSustainedFor:   ptrDur(3 * time.Second),
		InactiveSustainedFor: ptrDur(10 * time.Second),
	}, energy.StrategyIntegration)
	// One high reading is not enough to flip.
	out := rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(60)})
	if out.NewActivity == model.ActivityActive {
		t.Fatalf("must not flip active from a single high reading")
	}
	// Below threshold within sustained window -> candidate cleared.
	out = rt.OnReading(now.Add(time.Second), model.Reading{Timestamp: now.Add(time.Second), PowerW: ptrF(20)})
	if out.NewActivity == model.ActivityActive {
		t.Fatalf("must not flip active when reading dropped")
	}
}

func TestShortBurst_StartsAfterSustained(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassShortBurst, config.Thresholds{
		IdleBelowW:           ptrF(5),
		ActiveAboveW:         ptrF(50),
		ActiveSustainedFor:   ptrDur(3 * time.Second),
		InactiveSustainedFor: ptrDur(5 * time.Second),
	}, energy.StrategyIntegration)
	rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(60)})
	out := rt.OnReading(now.Add(4*time.Second), model.Reading{Timestamp: now.Add(4 * time.Second), PowerW: ptrF(1800)})
	if out.NewActivity != model.ActivityActive || !out.CycleStarted {
		t.Fatalf("expected active+CycleStarted, got %+v", out)
	}
	if rt.Cycle() == nil || !rt.Cycle().Active {
		t.Fatalf("expected active cycle")
	}
}

func TestCycle_StartFinishAndCounterEnergy(t *testing.T) {
	now := time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC)
	th := config.Thresholds{
		IdleBelowW:           ptrF(5),
		ActiveAboveW:         ptrF(20),
		ActiveSustainedFor:   ptrDur(10 * time.Second),
		InactiveSustainedFor: ptrDur(5 * time.Minute),
	}
	rt := mkRuntime(ClassCyclePower, th, energy.StrategyCounter)
	// Seed counter before activity.
	rt.OnReading(now, model.Reading{Timestamp: now, EnergyKWh: ptrF(100.0)})
	// Power high two times to satisfy sustained.
	rt.OnReading(now.Add(time.Second), model.Reading{Timestamp: now.Add(time.Second), PowerW: ptrF(1800), EnergyKWh: ptrF(100.0)})
	rt.OnReading(now.Add(15*time.Second), model.Reading{Timestamp: now.Add(15 * time.Second), PowerW: ptrF(1800), EnergyKWh: ptrF(100.0)})
	if rt.Cycle() == nil || !rt.Cycle().Active {
		t.Fatalf("expected active cycle after sustained high")
	}
	// Run for a while; counter advances.
	rt.OnReading(now.Add(30*time.Minute), model.Reading{Timestamp: now.Add(30 * time.Minute), PowerW: ptrF(1500), EnergyKWh: ptrF(101.0)})
	// Drop to idle; must sustain inactivity 5m before finish.
	rt.OnReading(now.Add(31*time.Minute), model.Reading{Timestamp: now.Add(31 * time.Minute), PowerW: ptrF(1)})
	out := rt.OnReading(now.Add(40*time.Minute), model.Reading{Timestamp: now.Add(40 * time.Minute), PowerW: ptrF(1), EnergyKWh: ptrF(101.0)})
	if !out.CycleFinished {
		t.Fatalf("expected CycleFinished after sustained idle, got %+v", out)
	}
	if rt.Cycle().Energy.ReportedKWhDelta != 1.0 {
		t.Fatalf("expected counter delta of 1.0, got %v", rt.Cycle().Energy.ReportedKWhDelta)
	}
	if rt.Cycle().Energy.PrimarySource != "counter" {
		t.Fatalf("expected primary counter, got %s", rt.Cycle().Energy.PrimarySource)
	}
}

func TestCycle_IntegrationGapDuringActiveCycle(t *testing.T) {
	now := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	th := config.Thresholds{
		IdleBelowW:           ptrF(5),
		ActiveAboveW:         ptrF(20),
		ActiveSustainedFor:   ptrDur(1 * time.Second),
		InactiveSustainedFor: ptrDur(1 * time.Second),
	}
	rt := mkRuntime(ClassCyclePower, th, energy.StrategyIntegration)
	rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(1000)})
	rt.OnReading(now.Add(2*time.Second), model.Reading{Timestamp: now.Add(2 * time.Second), PowerW: ptrF(1000)})
	// Huge gap during cycle - integrator must clamp instead of accruing 1kW for 4h.
	rt.OnReading(now.Add(4*time.Hour), model.Reading{Timestamp: now.Add(4 * time.Hour), PowerW: ptrF(1000)})
	if rt.IntegrationGapsClamped() == 0 {
		t.Fatalf("expected integration gap to be clamped")
	}
	if rt.Cycle() != nil && rt.Cycle().Energy.IntegratedKWh > 0.5 {
		t.Fatalf("integrated should not include the clamped gap, got %v", rt.Cycle().Energy.IntegratedKWh)
	}
}

func TestContinuous_TreatsIdleAsNonZero(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	th := config.Thresholds{
		IdleBelowW:           ptrF(5),
		ActiveAboveW:         ptrF(25),
		CompressorAboveW:     ptrF(25),
		ActiveSustainedFor:   ptrDur(1 * time.Second),
		InactiveSustainedFor: ptrDur(1 * time.Second),
	}
	rt := mkRuntime(ClassContinuous, th, energy.StrategyCounter)
	// 2W idle - normal_idle, not "active".
	out := rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(2)})
	if out.NewActivity == model.ActivityActiveCycle {
		t.Fatalf("2W must not start active cycle, got %+v", out)
	}
	// Compressor kicks in.
	rt.OnReading(now.Add(time.Second), model.Reading{Timestamp: now.Add(time.Second), PowerW: ptrF(80)})
	out = rt.OnReading(now.Add(3*time.Second), model.Reading{Timestamp: now.Add(3 * time.Second), PowerW: ptrF(85)})
	if out.NewActivity != model.ActivityActiveCycle || !out.CycleStarted {
		t.Fatalf("expected active cycle after sustained compressor, got %+v", out)
	}
}

func TestMedia_TransitionsActiveStandby(t *testing.T) {
	now := time.Date(2026, 5, 13, 20, 0, 0, 0, time.UTC)
	th := config.Thresholds{
		IdleBelowW:           ptrF(5),
		ActiveAboveW:         ptrF(20),
		ActiveSustainedFor:   ptrDur(1 * time.Second),
		InactiveSustainedFor: ptrDur(1 * time.Second),
	}
	rt := mkRuntime(ClassMedia, th, energy.StrategyIntegration)
	rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(80)})
	out := rt.OnReading(now.Add(2*time.Second), model.Reading{Timestamp: now.Add(2 * time.Second), PowerW: ptrF(80)})
	if out.NewActivity != model.ActivityActive {
		t.Fatalf("expected active, got %+v", out)
	}
	rt.OnReading(now.Add(3*time.Second), model.Reading{Timestamp: now.Add(3 * time.Second), PowerW: ptrF(1)})
	out = rt.OnReading(now.Add(5*time.Second), model.Reading{Timestamp: now.Add(5 * time.Second), PowerW: ptrF(1)})
	if out.NewActivity != model.ActivityStandby {
		t.Fatalf("expected standby, got %+v", out)
	}
}

func TestOnReading_NoPowerDoesNotTransition(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	th := config.Thresholds{IdleBelowW: ptrF(5), ActiveAboveW: ptrF(20)}
	rt := mkRuntime(ClassCyclePower, th, energy.StrategyCounter)
	state := "ON"
	out := rt.OnReading(now, model.Reading{Timestamp: now, State: &state, LinkQuality: nil})
	if out.PrevActivity != out.NewActivity {
		t.Fatalf("activity must not change without a power reading, got %+v", out)
	}
}

// Binary-state class: tests state-driven transitions for boilers /
// contact sensors / etc. PowerW is intentionally nil throughout —
// these devices have no power telemetry.

func ptrS(s string) *string { return &s }

func TestBinary_OnTransitionsImmediatelyWhenSustainedZero(t *testing.T) {
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassBinaryState, config.Thresholds{}, "")
	out := rt.OnReading(now, model.Reading{Timestamp: now, State: ptrS("ON")})
	if out.NewActivity != model.ActivityActive || !out.CycleStarted {
		t.Fatalf("expected active + CycleStarted, got %+v", out)
	}
	if rt.Cycle() == nil || !rt.Cycle().Active {
		t.Fatalf("expected active cycle")
	}
}

func TestBinary_OffFinishesCycleWithDuration(t *testing.T) {
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassBinaryState, config.Thresholds{}, "")
	rt.OnReading(now, model.Reading{Timestamp: now, State: ptrS("ON")})
	out := rt.OnReading(now.Add(30*time.Minute), model.Reading{Timestamp: now.Add(30 * time.Minute), State: ptrS("OFF")})
	if !out.CycleFinished {
		t.Fatalf("expected CycleFinished, got %+v", out)
	}
	if rt.Cycle() == nil || rt.Cycle().Active {
		t.Fatalf("expected finished cycle")
	}
	if rt.Cycle().DurationSeconds != 30*60 {
		t.Errorf("expected duration 1800s, got %d", rt.Cycle().DurationSeconds)
	}
	if rt.Cycle().Energy.SelectedKWh != 0 {
		t.Errorf("binary cycle must report zero energy, got %v", rt.Cycle().Energy.SelectedKWh)
	}
}

func TestBinary_SustainedWindowDebouncesBlip(t *testing.T) {
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassBinaryState, config.Thresholds{
		ActiveSustainedFor:   ptrDur(5 * time.Second),
		InactiveSustainedFor: ptrDur(5 * time.Second),
	}, "")
	rt.OnReading(now, model.Reading{Timestamp: now, State: ptrS("ON")})
	// Blip back to OFF before window matures — must NOT activate.
	rt.OnReading(now.Add(2*time.Second), model.Reading{Timestamp: now.Add(2 * time.Second), State: ptrS("OFF")})
	out := rt.OnReading(now.Add(3*time.Second), model.Reading{Timestamp: now.Add(3 * time.Second), State: ptrS("OFF")})
	if out.NewActivity == model.ActivityActive {
		t.Fatalf("must not flip active from sub-window blip, got %+v", out)
	}
	// Now hold ON for the full window.
	rt.OnReading(now.Add(10*time.Second), model.Reading{Timestamp: now.Add(10 * time.Second), State: ptrS("ON")})
	out = rt.OnReading(now.Add(16*time.Second), model.Reading{Timestamp: now.Add(16 * time.Second), State: ptrS("ON")})
	if out.NewActivity != model.ActivityActive {
		t.Fatalf("expected active after sustained ON, got %+v", out)
	}
}

func TestBinary_OffSeedsIdleFromUnknown(t *testing.T) {
	// First contact with State=OFF should set ActivityIdle (rather
	// than leaving it at unknown). This means a STARTUP snapshot
	// with both channels OFF gives consumers a useful initial state.
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassBinaryState, config.Thresholds{}, "")
	out := rt.OnReading(now, model.Reading{Timestamp: now, State: ptrS("OFF")})
	if out.NewActivity != model.ActivityIdle {
		t.Fatalf("expected idle from first OFF reading, got %+v", out)
	}
}

func TestBinary_NoStateIsNoOp(t *testing.T) {
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassBinaryState, config.Thresholds{}, "")
	rt.OnReading(now, model.Reading{Timestamp: now, State: ptrS("ON")})
	// Reading without State — must not change activity.
	prev := rt.Activity()
	out := rt.OnReading(now.Add(time.Second), model.Reading{Timestamp: now.Add(time.Second)})
	if out.NewActivity != prev {
		t.Fatalf("missing State must not transition activity, got %+v", out)
	}
}

// Sensor class: measurement-only devices. No cycles, no occupancy
// signal — activity transitions unknown→reporting on first contact
// and stays there.

func TestSensor_FirstReadingFlipsToReporting(t *testing.T) {
	now := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassSensor, config.Thresholds{}, "")
	temp := 21.4
	out := rt.OnReading(now, model.Reading{Timestamp: now, TemperatureC: &temp})
	if out.PrevActivity != model.ActivityUnknown {
		t.Errorf("expected prev=unknown, got %q", out.PrevActivity)
	}
	if out.NewActivity != model.ActivityReporting {
		t.Errorf("expected new=reporting, got %q", out.NewActivity)
	}
	if out.CycleStarted || out.CycleFinished {
		t.Errorf("sensors must not emit cycle events, got %+v", out)
	}
	if out.Cycle != nil {
		t.Errorf("sensors must not produce a Cycle, got %+v", out.Cycle)
	}
}

func TestSensor_SubsequentReadingDoesNotRetransition(t *testing.T) {
	now := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassSensor, config.Thresholds{}, "")
	temp := 21.4
	rt.OnReading(now, model.Reading{Timestamp: now, TemperatureC: &temp})
	// Second reading — activity unchanged; PrevActivity must equal
	// NewActivity so the engine doesn't emit another transition event.
	temp2 := 21.5
	out := rt.OnReading(now.Add(5*time.Minute), model.Reading{Timestamp: now.Add(5 * time.Minute), TemperatureC: &temp2})
	if out.PrevActivity != model.ActivityReporting || out.NewActivity != model.ActivityReporting {
		t.Fatalf("expected reporting→reporting, got %q→%q", out.PrevActivity, out.NewActivity)
	}
}

func TestSensor_NoPowerNeeded(t *testing.T) {
	// A sensor reading carries no power; the power short-circuit in
	// the power-based dispatcher must not affect sensor handling.
	now := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassSensor, config.Thresholds{}, "")
	hum := 55.0
	out := rt.OnReading(now, model.Reading{Timestamp: now, HumidityPct: &hum})
	if out.NewActivity != model.ActivityReporting {
		t.Fatalf("expected reporting from humidity-only reading, got %q", out.NewActivity)
	}
}

// TestFirstLowPowerReadingResolvesUnknown verifies that a first reading
// with power below idle_below_w immediately transitions the device out
// of ActivityUnknown without waiting for hysteresis.
func TestFirstLowPowerReadingResolvesUnknown(t *testing.T) {
	const idleBelow = 5.0
	const activeAbove = 50.0

	type tc struct {
		name      string
		class     string
		powerW    float64
		wantAct   model.DeviceActivityState
		wantCycle bool // CycleStarted should be true for the "active" sub-cases
	}
	cases := []tc{
		// Low-power first reading → idle (or normal_idle for Continuous).
		{name: "ShortBurst_lowPower", class: ClassShortBurst, powerW: idleBelow - 1, wantAct: model.ActivityIdle},
		{name: "CyclePower_lowPower", class: ClassCyclePower, powerW: idleBelow - 1, wantAct: model.ActivityIdle},
		{name: "Continuous_lowPower", class: ClassContinuous, powerW: idleBelow - 1, wantAct: model.ActivityNormalIdle},
		{name: "Media_lowPower", class: ClassMedia, powerW: idleBelow - 1, wantAct: model.ActivityIdle},

		// High-power first reading (above active threshold, no sustained
		// window) → appropriate active state.
		{name: "ShortBurst_highPower", class: ClassShortBurst, powerW: activeAbove + 1, wantAct: model.ActivityActive, wantCycle: true},
		{name: "CyclePower_highPower", class: ClassCyclePower, powerW: activeAbove + 1, wantAct: model.ActivityRunning, wantCycle: true},
		{name: "Continuous_highPower", class: ClassContinuous, powerW: activeAbove + 1, wantAct: model.ActivityActiveCycle, wantCycle: true},
		{name: "Media_highPower", class: ClassMedia, powerW: activeAbove + 1, wantAct: model.ActivityActive, wantCycle: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(2026, 5, 14, 8, 0, 0, 0, time.UTC)
			th := config.Thresholds{
				IdleBelowW:           ptrF(idleBelow),
				ActiveAboveW:         ptrF(activeAbove),
				CompressorAboveW:     ptrF(activeAbove), // used by Continuous
				ActiveSustainedFor:   nil,               // fire immediately
				InactiveSustainedFor: nil,
			}
			rt := mkRuntime(c.class, th, energy.StrategyIntegration)
			out := rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(c.powerW)})
			if out.PrevActivity != model.ActivityUnknown {
				t.Errorf("expected prev=unknown, got %q", out.PrevActivity)
			}
			if out.NewActivity != c.wantAct {
				t.Errorf("expected new=%q, got %q", c.wantAct, out.NewActivity)
			}
			if c.wantCycle && !out.CycleStarted {
				t.Errorf("expected CycleStarted=true for high-power first reading")
			}
			if !c.wantCycle && out.NewActivity == model.ActivityUnknown {
				t.Errorf("activity must not remain unknown after first low-power reading")
			}
		})
	}
}

// TestStepContinuous_ExplicitZeroCompressorAboveW verifies that
// CompressorAboveW=0 (explicit operator intent: "any draw counts")
// is honoured and does NOT fall back to ActiveAboveW=100. A 10W
// reading must start a compressor cycle even though 10 < 100.
func TestStepContinuous_ExplicitZeroCompressorAboveW(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassContinuous, config.Thresholds{
		CompressorAboveW: ptrF(0),
		ActiveAboveW:     ptrF(100),
		IdleBelowW:       ptrF(5),
		// No sustained windows: fire immediately.
	}, energy.StrategyIntegration)
	out := rt.OnReading(now, model.Reading{Timestamp: now, PowerW: ptrF(10)})
	if !out.CycleStarted {
		t.Errorf("explicit CompressorAboveW=0 should treat 10W as active; got CycleStarted=false (activity=%s)", out.NewActivity)
	}
	if out.NewActivity != model.ActivityActiveCycle {
		t.Errorf("expected ActivityActiveCycle, got %s", out.NewActivity)
	}
}
