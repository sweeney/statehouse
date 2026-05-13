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

func ptrF(v float64) *float64 { return &v }

func TestShortBurst_HysteresisAvoidsFalseStart(t *testing.T) {
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	rt := mkRuntime(ClassShortBurst, config.Thresholds{
		IdleBelowW:           5,
		ActiveAboveW:         50,
		ActiveSustainedFor:   3 * time.Second,
		InactiveSustainedFor: 10 * time.Second,
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
		IdleBelowW:           5,
		ActiveAboveW:         50,
		ActiveSustainedFor:   3 * time.Second,
		InactiveSustainedFor: 5 * time.Second,
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
		IdleBelowW:           5,
		ActiveAboveW:         20,
		ActiveSustainedFor:   10 * time.Second,
		InactiveSustainedFor: 5 * time.Minute,
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
		IdleBelowW:           5,
		ActiveAboveW:         20,
		ActiveSustainedFor:   1 * time.Second,
		InactiveSustainedFor: 1 * time.Second,
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
		IdleBelowW:           5,
		ActiveAboveW:         25,
		CompressorAboveW:     25,
		ActiveSustainedFor:   1 * time.Second,
		InactiveSustainedFor: 1 * time.Second,
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
		IdleBelowW:           5,
		ActiveAboveW:         20,
		ActiveSustainedFor:   1 * time.Second,
		InactiveSustainedFor: 1 * time.Second,
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
	th := config.Thresholds{IdleBelowW: 5, ActiveAboveW: 20, ActiveSustainedFor: 0, InactiveSustainedFor: 0}
	rt := mkRuntime(ClassCyclePower, th, energy.StrategyCounter)
	state := "ON"
	out := rt.OnReading(now, model.Reading{Timestamp: now, State: &state, LinkQuality: nil})
	if out.PrevActivity != out.NewActivity {
		t.Fatalf("activity must not change without a power reading, got %+v", out)
	}
}
