package state

import (
	"github.com/sweeney/statehouse/internal/model"
	"time"
)

// recomputeElectricity refreshes the whole-house electricity summary
// after a power-bearing reading. It runs the pure aggregator, advances
// the three integrators (so kWh totals stay additively consistent), and
// writes the summary onto the house state.
//
// triggeredByMeter controls canonical-event emission: only meter
// readings produce a synthetic "house_electricity" canonical event set.
// Plug readings update the live House.Electricity field for the HTTP
// snapshot but emit nothing, so the canonical stream stays locked to
// meter cadence.
//
// Concurrency: IngestReading is a supported concurrent entry point
// (the engine has no input-side serialisation; the concurrent test
// proves the data path is race-free). A monotonicity guard discards
// readings whose timestamp is older than the last applied electricity
// recompute. Without it, two concerns arise from out-of-order arrivals:
//
//  1. setHouseElectricity could clobber a newer summary with an older
//     one — House.Electricity.ComputedAt and *KWh would regress.
//  2. The shared energy.Integrator skips accrual on dt<=0 but still
//     advances its internal lastAt to the older timestamp, which
//     causes the *next* legitimate interval to overlap the previous
//     one and double-count a slice of energy.
//
// Skipping is the conservative choice: the device's Latest is still
// updated upstream of recomputeElectricity, so the next on-time meter
// tick will pick up the dropped reading's contribution in its
// monitored sum.
func (e *Engine) recomputeElectricity(now time.Time, triggeredByMeter bool, sourceTopic string, reading model.Reading) {
	devices := e.store.Devices()
	stalenessFor := func(class string) *int {
		if c, ok := e.cfg.DeviceClasses[class]; ok {
			return c.StalenessSeconds
		}
		return nil
	}
	agg := AggregateElectricity(now, devices, stalenessFor)
	if !agg.GrossSeen {
		return
	}

	e.elecMu.Lock()
	if !e.lastElecAt.IsZero() && !now.After(e.lastElecAt) {
		e.elecMu.Unlock()
		return
	}
	e.lastElecAt = now
	e.grossIntegrator.Update(now, agg.GrossW)
	e.monitoredIntegrator.Update(now, agg.MonitoredW)
	e.unmonitoredIntegrator.Update(now, agg.UnmonitoredW)
	// Refresh the authoritative meter period totals when this recompute
	// was driven by a meter reading that carried them. Plug-triggered
	// recomputes leave the previously-seen values in place.
	if triggeredByMeter {
		if reading.MeterTodayKWh != nil {
			e.meterTodayKWh = reading.MeterTodayKWh
		}
		if reading.MeterWeekKWh != nil {
			e.meterWeekKWh = reading.MeterWeekKWh
		}
		if reading.MeterMonthKWh != nil {
			e.meterMonthKWh = reading.MeterMonthKWh
		}
	}
	summary := model.ElectricitySummary{
		GrossW:           agg.GrossW,
		MonitoredW:       agg.MonitoredW,
		UnmonitoredW:     agg.UnmonitoredW,
		StaleDeviceCount: len(agg.StaleIDs),
		StaleDevices:     agg.StaleIDs,
		TodayKWh:         e.meterTodayKWh,
		WeekKWh:          e.meterWeekKWh,
		MonthKWh:         e.meterMonthKWh,
		Session: model.SessionEnergy{
			Since:          e.startedAt,
			GrossKWh:       e.grossIntegrator.Total(),
			MonitoredKWh:   e.monitoredIntegrator.Total(),
			UnmonitoredKWh: e.unmonitoredIntegrator.Total(),
		},
		ComputedAt: now,
	}
	// Coverage is monitored / gross, exposed raw. It can exceed 1
	// briefly (monitored outruns gross due to sample-cadence skew or
	// apparent-vs-real power on some plugs). It can be negative when
	// gross is negative — SMETS2 meters with solar/battery report
	// net export as a negative power.value, in which case the ratio
	// loses its "fraction of consumption" semantics. Downstream
	// consumers decide whether to render it; clamping here would
	// hide misconfiguration. Zero gross gives Coverage = 0 (the
	// `!= 0` guard avoids NaN/Inf).
	if agg.GrossW != 0 {
		summary.Coverage = agg.MonitoredW / agg.GrossW
	}
	// setHouseElectricity is inside elecMu so the store mirror
	// reflects the same integrator snapshot the canonical events
	// will carry; releasing the lock between the build and the
	// store-write would allow a concurrent recompute to install a
	// newer summary first and then have this one clobber it.
	e.store.setHouseElectricity(summary)
	e.elecMu.Unlock()

	if triggeredByMeter {
		e.emitElectricityCanonical(now, sourceTopic, summary)
	}
}

// emitElectricityCanonical fans the aggregate out as canonical events,
// one per field. They are attributed to the synthetic HouseDeviceID
// (never registered in the store, leading-underscore collision-safe)
// with scheme="house". Two things to keep in mind for reviewers:
//
//   - Downstream sinks that look up Store.Get(ev.DeviceID) on a canonical
//     event must short-circuit when ev.Capability ==
//     HouseElectricityCapability, since the lookup will (correctly) miss.
//     The Influx writer does this; other sinks should too.
//   - SourceTopic is the meter's topic — the event that triggered the
//     recomputation. Carrying the meter's topic, not a synthetic one,
//     gives operators a real provenance trail when debugging.
func (e *Engine) emitElectricityCanonical(now time.Time, sourceTopic string, s model.ElectricitySummary) {
	e.mu.Lock()
	sinks := append([]CanonicalSink(nil), e.canonicalSinks...)
	e.mu.Unlock()
	if len(sinks) == 0 {
		return
	}
	emit := func(attr string, value any, unit string) {
		ev := model.CanonicalEvent{
			Timestamp:   now,
			Source:      houseIdentity.Scheme,
			SourceTopic: sourceTopic,
			DeviceID:    HouseDeviceID,
			Identity:    houseIdentity,
			Capability:  HouseElectricityCapability,
			Attribute:   attr,
			Value:       value,
			Unit:        unit,
		}
		for _, sink := range sinks {
			sink.OnCanonicalEvent(ev)
		}
	}
	emit("gross_w", s.GrossW, "W")
	emit("monitored_w", s.MonitoredW, "W")
	emit("unmonitored_w", s.UnmonitoredW, "W")
	emit("coverage", s.Coverage, "")
	// Authoritative meter period totals (only once the meter has reported).
	if s.TodayKWh != nil {
		emit("today_kwh", *s.TodayKWh, "kWh")
	}
	if s.WeekKWh != nil {
		emit("week_kwh", *s.WeekKWh, "kWh")
	}
	if s.MonthKWh != nil {
		emit("month_kwh", *s.MonthKWh, "kWh")
	}
	// Service-lifetime integration — prefixed so it is never mistaken for
	// a true house total in the time series.
	emit("session_gross_kwh", s.Session.GrossKWh, "kWh")
	emit("session_monitored_kwh", s.Session.MonitoredKWh, "kWh")
	emit("session_unmonitored_kwh", s.Session.UnmonitoredKWh, "kWh")
	emit("stale_device_count", float64(s.StaleDeviceCount), "")
}
