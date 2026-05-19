package device

import (
	"time"

	"github.com/sweeney/statehouse/internal/config"
	"github.com/sweeney/statehouse/internal/energy"
	"github.com/sweeney/statehouse/internal/model"
)

const (
	// finishedRecentlyLowReadings is the number of consecutive low-power
	// readings required to decay finished_recently → idle.
	finishedRecentlyLowReadings = 3
	// finishedRecentlyTTL is the fallback timeout: if readings don't
	// arrive, Tick decays finished_recently → idle after this duration.
	finishedRecentlyTTL = 30 * time.Minute
)

func derefF64(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// derefIfSet returns (*p, true) when p is non-nil, and (zero, false)
// when p is nil. It is the nil-aware alternative to derefF64 for cases
// where an explicit zero carries different semantics than "not set".
func derefIfSet[T any](p *T) (T, bool) {
	var zero T
	if p == nil {
		return zero, false
	}
	return *p, true
}

func derefDur(p *time.Duration, def time.Duration) time.Duration {
	if p == nil {
		return def
	}
	return *p
}

// Outcome captures everything the engine learned by feeding one
// reading into a device. State transitions are advisory: the caller
// decides which derived events to emit based on the
// {PrevActivity, NewActivity} pair and CycleStarted/CycleFinished
// signals.
type Outcome struct {
	PrevActivity  model.DeviceActivityState
	NewActivity   model.DeviceActivityState
	CycleStarted  bool
	CycleFinished bool
	Cycle         *model.Cycle
}

// candidateSample is the most recent power reading awaiting hysteresis.
type candidateSample struct {
	at          time.Time
	powerW      float64
	abovePrevHi bool // power exceeded the active threshold
	belowPrevLo bool // power dropped below the idle threshold
}

// Runtime is the per-device working state used by the state machine.
// It is intentionally narrow: the broader Device record is owned by
// the state store.
type Runtime struct {
	Profile    Profile
	Thresholds config.Thresholds

	activity    model.DeviceActivityState
	activeSince time.Time
	candidate   *candidateSample

	// finishedLowCount tracks consecutive low-power readings while in
	// ActivityFinishedRecently so stepCycle can decay to idle.
	finishedLowCount int

	counter    energy.Counter
	integrator *energy.Integrator
	cycle      *model.Cycle

	// Snapshot of integrator state at cycle start so the cycle's
	// integrated kWh is `now - snapshot` and the integrator keeps
	// running across cycle boundaries (gaps are still detected).
	cycleStartIntegratorSnap energy.Snapshot
}

// NewRuntime initialises a Runtime for the given profile.
func NewRuntime(p Profile, maxIntegrationGap time.Duration) *Runtime {
	return &Runtime{
		Profile:    p,
		Thresholds: p.Thresholds,
		activity:   model.ActivityUnknown,
		integrator: energy.NewIntegrator(maxIntegrationGap),
	}
}

// Activity returns the current activity state.
func (r *Runtime) Activity() model.DeviceActivityState { return r.activity }

// Cycle returns the in-flight or most-recent cycle, or nil if none.
func (r *Runtime) Cycle() *model.Cycle { return r.cycle }

// OnReading drives the device state machine with one new reading.
func (r *Runtime) OnReading(at time.Time, reading model.Reading) Outcome {
	out := Outcome{PrevActivity: r.activity, NewActivity: r.activity}

	// Update energy paths first; both run regardless of activity state.
	// Binary devices simply never feed these — Reading.PowerW is nil.
	if reading.PowerW != nil {
		r.integrator.Update(at, *reading.PowerW)
	}
	if reading.EnergyKWh != nil {
		r.counter.Update(*reading.EnergyKWh)
	}

	// Binary-state devices drive activity off Reading.State, not power.
	// Dispatch them first so the power-based fallthrough doesn't kick.
	if r.Profile.Class == ClassBinaryState {
		if reading.State != nil {
			r.stepBinary(at, *reading.State, &out)
		}
		if r.cycle != nil && r.cycle.Active {
			r.refreshCycleEnergy(at)
		}
		out.NewActivity = r.activity
		out.Cycle = r.cycle
		return out
	}

	// Measurement-only sensors have no activity machine. Any reading
	// transitions unknown→reporting once; subsequent readings update
	// Latest values (in the engine) but don't transition activity.
	if r.Profile.Class == ClassSensor {
		if r.activity == model.ActivityUnknown {
			r.activity = model.ActivityReporting
		}
		out.NewActivity = r.activity
		out.Cycle = nil
		return out
	}

	if reading.PowerW == nil {
		// No power signal; activity state cannot change from this
		// reading.
		if r.cycle != nil && r.cycle.Active {
			r.refreshCycleEnergy(at)
		}
		out.NewActivity = r.activity
		out.Cycle = r.cycle
		return out
	}

	p := *reading.PowerW
	switch r.Profile.Class {
	case ClassShortBurst:
		r.stepShortBurst(at, p, &out)
	case ClassCyclePower:
		r.stepCycle(at, p, &out)
	case ClassContinuous:
		r.stepContinuous(at, p, &out)
	case ClassMedia:
		r.stepMedia(at, p, &out)
	default:
		// Unclassified: only track power but do not transition.
	}
	if r.cycle != nil && r.cycle.Active {
		r.refreshCycleEnergy(at)
	}
	out.NewActivity = r.activity
	out.Cycle = r.cycle
	return out
}

// stepShortBurst implements the kettle/toaster/microwave model.
func (r *Runtime) stepShortBurst(at time.Time, p float64, out *Outcome) {
	if r.maybeBegin(at, p) {
		r.activity = model.ActivityActive
		out.NewActivity = model.ActivityActive
		out.CycleStarted = true
		r.startCycle(at)
		return
	}
	if r.maybeEnd(at, p) {
		r.activity = model.ActivityIdle
		out.NewActivity = model.ActivityIdle
		out.CycleFinished = true
		r.finishCycle(at)
		return
	}
	// First low-power reading: resolve unknown → idle immediately.
	if r.activity == model.ActivityUnknown && p < derefF64(r.Thresholds.IdleBelowW, 0) {
		r.activity = model.ActivityIdle
		out.NewActivity = model.ActivityIdle
	}
}

// stepCycle implements the dishwasher/washer/dryer model. It uses the
// same hysteresis but exposes richer activity states.
func (r *Runtime) stepCycle(at time.Time, p float64, out *Outcome) {
	if r.maybeBegin(at, p) {
		r.activity = model.ActivityRunning
		out.NewActivity = model.ActivityRunning
		out.CycleStarted = true
		r.startCycle(at)
		return
	}
	if r.maybeEnd(at, p) {
		r.activity = model.ActivityFinishedRecently
		out.NewActivity = model.ActivityFinishedRecently
		out.CycleFinished = true
		r.finishCycle(at)
		return
	}
	// Decay finished_recently → idle on N consecutive low-power readings.
	// A non-low reading resets the counter (residual heat, standby draw, etc.).
	if r.activity == model.ActivityFinishedRecently {
		if p < derefF64(r.Thresholds.IdleBelowW, 0) {
			r.finishedLowCount++
			if r.finishedLowCount >= finishedRecentlyLowReadings {
				r.activity = model.ActivityIdle
				out.NewActivity = model.ActivityIdle
				r.finishedLowCount = 0
			}
		} else {
			r.finishedLowCount = 0
		}
		return
	}
	// First low-power reading: resolve unknown → idle immediately.
	if r.activity == model.ActivityUnknown && p < derefF64(r.Thresholds.IdleBelowW, 0) {
		r.activity = model.ActivityIdle
		out.NewActivity = model.ActivityIdle
	}
}

// stepContinuous implements the fridge/freezer/dehumidifier model.
// Idle is not zero; the device alternates between a low standby draw
// and a compressor cycle above CompressorAboveW.
func (r *Runtime) stepContinuous(at time.Time, p float64, out *Outcome) {
	highTh, highSet := derefIfSet(r.Thresholds.CompressorAboveW)
	if !highSet {
		highTh, _ = derefIfSet(r.Thresholds.ActiveAboveW)
	}
	lowTh := derefF64(r.Thresholds.IdleBelowW, 0)
	inactiveSustainedFor := derefDur(r.Thresholds.InactiveSustainedFor, 0)
	activeSustainedFor := derefDur(r.Thresholds.ActiveSustainedFor, 0)
	switch r.activity {
	case model.ActivityActiveCycle:
		if p <= lowTh {
			if r.candidate == nil || !r.candidate.belowPrevLo {
				r.candidate = &candidateSample{at: at, powerW: p, belowPrevLo: true}
				if inactiveSustainedFor <= 0 {
					r.activity = model.ActivityNormalIdle
					out.NewActivity = model.ActivityNormalIdle
					out.CycleFinished = true
					r.finishCycle(at)
					r.candidate = nil
				}
				return
			}
			if at.Sub(r.candidate.at) >= inactiveSustainedFor {
				r.activity = model.ActivityNormalIdle
				out.NewActivity = model.ActivityNormalIdle
				out.CycleFinished = true
				r.finishCycle(at)
				r.candidate = nil
			}
		} else {
			r.candidate = nil
		}
	default:
		// idle / normal_idle / unknown
		if p >= highTh {
			if r.candidate == nil || !r.candidate.abovePrevHi {
				r.candidate = &candidateSample{at: at, powerW: p, abovePrevHi: true}
				if activeSustainedFor <= 0 {
					r.activity = model.ActivityActiveCycle
					out.NewActivity = model.ActivityActiveCycle
					out.CycleStarted = true
					r.startCycle(at)
					r.candidate = nil
				}
				return
			}
			if at.Sub(r.candidate.at) >= activeSustainedFor {
				r.activity = model.ActivityActiveCycle
				out.NewActivity = model.ActivityActiveCycle
				out.CycleStarted = true
				r.startCycle(at)
				r.candidate = nil
			}
		} else {
			r.activity = model.ActivityNormalIdle
			r.candidate = nil
		}
	}
}

// stepMedia implements the TV/AV/speaker model.
func (r *Runtime) stepMedia(at time.Time, p float64, out *Outcome) {
	if r.maybeBegin(at, p) {
		r.activity = model.ActivityActive
		out.NewActivity = model.ActivityActive
		out.CycleStarted = true
		r.startCycle(at)
		return
	}
	if r.maybeEnd(at, p) {
		r.activity = model.ActivityStandby
		out.NewActivity = model.ActivityStandby
		out.CycleFinished = true
		r.finishCycle(at)
		return
	}
	// First low-power reading: resolve unknown → idle immediately.
	if r.activity == model.ActivityUnknown && p < derefF64(r.Thresholds.IdleBelowW, 0) {
		r.activity = model.ActivityIdle
		out.NewActivity = model.ActivityIdle
	}
}

// stepBinary implements the state-only model used by binary devices
// (boiler relays, contact sensors, motion sensors, switches that
// report state without power). The activity follows the reported
// ON/OFF directly, with optional sustained-for windows so micro-
// blips get debounced. "Cycles" are time-only — Energy fields stay
// zero, but DurationSeconds tells the consumer how long the device
// was on. Caller is responsible for normalising state to "ON"/"OFF".
func (r *Runtime) stepBinary(at time.Time, state string, out *Outcome) {
	inactiveSustainedFor := derefDur(r.Thresholds.InactiveSustainedFor, 0)
	activeSustainedFor := derefDur(r.Thresholds.ActiveSustainedFor, 0)
	switch r.activity {
	case model.ActivityActive:
		if state == "OFF" {
			if r.candidate == nil || !r.candidate.belowPrevLo {
				r.candidate = &candidateSample{at: at, belowPrevLo: true}
				if inactiveSustainedFor <= 0 {
					r.activity = model.ActivityIdle
					out.NewActivity = model.ActivityIdle
					out.CycleFinished = true
					r.finishCycle(at)
					r.candidate = nil
				}
				return
			}
			if at.Sub(r.candidate.at) >= inactiveSustainedFor {
				r.activity = model.ActivityIdle
				out.NewActivity = model.ActivityIdle
				out.CycleFinished = true
				r.finishCycle(at)
				r.candidate = nil
			}
		} else if state == "ON" {
			r.candidate = nil
		}
	default:
		// idle / unknown
		if state == "ON" {
			if r.candidate == nil || !r.candidate.abovePrevHi {
				r.candidate = &candidateSample{at: at, abovePrevHi: true}
				if activeSustainedFor <= 0 {
					r.activity = model.ActivityActive
					out.NewActivity = model.ActivityActive
					out.CycleStarted = true
					r.startCycle(at)
					r.candidate = nil
				}
				return
			}
			if at.Sub(r.candidate.at) >= activeSustainedFor {
				r.activity = model.ActivityActive
				out.NewActivity = model.ActivityActive
				out.CycleStarted = true
				r.startCycle(at)
				r.candidate = nil
			}
		} else if state == "OFF" {
			if r.activity == model.ActivityUnknown {
				r.activity = model.ActivityIdle
				out.NewActivity = model.ActivityIdle
			}
			r.candidate = nil
		}
	}
}

// maybeBegin returns true once the high threshold has been exceeded
// for ActiveSustainedFor.
func (r *Runtime) maybeBegin(at time.Time, p float64) bool {
	if r.activity == model.ActivityActive ||
		r.activity == model.ActivityRunning ||
		r.activity == model.ActivityActiveCycle {
		return false
	}
	activeAboveW := derefF64(r.Thresholds.ActiveAboveW, 0)
	activeSustainedFor := derefDur(r.Thresholds.ActiveSustainedFor, 0)
	if p < activeAboveW {
		// reading is not high enough; clear candidate if it was a high one.
		if r.candidate != nil && r.candidate.abovePrevHi {
			r.candidate = nil
		}
		return false
	}
	if r.candidate == nil || !r.candidate.abovePrevHi {
		r.candidate = &candidateSample{at: at, powerW: p, abovePrevHi: true}
		// If the sustained threshold is zero, fire immediately.
		if activeSustainedFor <= 0 {
			r.candidate = nil
			return true
		}
		return false
	}
	if at.Sub(r.candidate.at) >= activeSustainedFor {
		r.candidate = nil
		return true
	}
	return false
}

// maybeEnd returns true once the low threshold has been observed for
// InactiveSustainedFor.
func (r *Runtime) maybeEnd(at time.Time, p float64) bool {
	switch r.activity {
	case model.ActivityActive, model.ActivityRunning, model.ActivityActiveCycle:
		// fine
	default:
		return false
	}
	idleBelowW := derefF64(r.Thresholds.IdleBelowW, 0)
	inactiveSustainedFor := derefDur(r.Thresholds.InactiveSustainedFor, 0)
	if p > idleBelowW {
		if r.candidate != nil && r.candidate.belowPrevLo {
			r.candidate = nil
		}
		return false
	}
	if r.candidate == nil || !r.candidate.belowPrevLo {
		r.candidate = &candidateSample{at: at, powerW: p, belowPrevLo: true}
		if inactiveSustainedFor <= 0 {
			r.candidate = nil
			return true
		}
		return false
	}
	if at.Sub(r.candidate.at) >= inactiveSustainedFor {
		r.candidate = nil
		return true
	}
	return false
}

func (r *Runtime) startCycle(at time.Time) {
	r.finishedLowCount = 0
	r.cycle = &model.Cycle{Active: true, StartedAt: at}
	r.activeSince = at
	// Move the counter baseline to the current latest reading. This
	// preserves the seeded baseline if no new counter has been seen
	// since cycle start, and keeps continuity if a counter reading
	// has already been integrated this tick.
	r.counter.RebaselineAtLatest()
	// Don't reset the integrator: keep its running total + gap counter
	// alive across cycles. Snapshot the totals so the cycle records
	// the delta from this moment.
	r.cycleStartIntegratorSnap = r.integrator.SnapshotState()
}

// SeedCounter records the most recent counter value as the baseline
// for the next cycle. Call this whenever a new counter reading
// arrives outside of an active cycle so the *next* cycle starts from
// the right baseline.
func (r *Runtime) SeedCounter(v float64) {
	if r.cycle != nil && r.cycle.Active {
		return
	}
	r.counter.SetBaseline(v)
}

func (r *Runtime) finishCycle(at time.Time) {
	if r.cycle == nil {
		return
	}
	r.cycle.Active = false
	finishedAt := at
	r.cycle.FinishedAt = &finishedAt
	r.cycle.DurationSeconds = int64(at.Sub(r.cycle.StartedAt).Seconds())
	r.refreshCycleEnergy(at)
}

func (r *Runtime) refreshCycleEnergy(at time.Time) {
	if r.cycle == nil {
		return
	}
	counterKWh := r.counter.Delta()
	integratedKWh := r.integrator.Total() - r.cycleStartIntegratorSnap.Total
	if integratedKWh < 0 {
		integratedKWh = 0
	}
	selected, source := energy.SelectKWh(r.Profile.Strategy, counterKWh, integratedKWh)
	r.cycle.Energy = model.CycleEnergy{
		PrimarySource:    source,
		ReportedKWhDelta: counterKWh,
		IntegratedKWh:    integratedKWh,
		SelectedKWh:      selected,
	}
	if r.cycle.Active {
		r.cycle.DurationSeconds = int64(at.Sub(r.cycle.StartedAt).Seconds())
	}
}

// MarkDivergence records that the divergence between the two energy
// paths exceeds the configured warning threshold for this cycle.
func (r *Runtime) MarkDivergence(pct float64) {
	if r.cycle == nil {
		return
	}
	r.cycle.Energy.DivergencePct = pct
	r.cycle.Energy.DivergenceWarning = true
}

// MarkStaleCounter flags that the counter-primary device reported no
// counter movement during the cycle despite meaningful activity.
func (r *Runtime) MarkStaleCounter() {
	if r.cycle == nil {
		return
	}
	r.cycle.Energy.StaleCounter = true
}

// Tick applies time-driven transitions. The engine calls this on its
// periodic tick so TTL-based decays fire even when readings are absent.
// Currently handles: finished_recently → idle after finishedRecentlyTTL.
func (r *Runtime) Tick(at time.Time) Outcome {
	out := Outcome{PrevActivity: r.activity, NewActivity: r.activity}
	if r.activity != model.ActivityFinishedRecently {
		return out
	}
	if r.cycle == nil || r.cycle.FinishedAt == nil {
		return out
	}
	if at.Sub(*r.cycle.FinishedAt) >= finishedRecentlyTTL {
		r.activity = model.ActivityIdle
		r.finishedLowCount = 0
		out.NewActivity = model.ActivityIdle
	}
	return out
}

// IntegrationGapsClamped exposes the number of times the integrator
// has clamped a too-long gap *since the most recent cycle start*. If
// no cycle has started yet, it returns the full count from boot.
func (r *Runtime) IntegrationGapsClamped() int {
	return r.integrator.GapsClamped() - r.cycleStartIntegratorSnap.GapsClamped
}
