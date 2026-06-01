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
func (e *Engine) recomputeElectricity(now time.Time, triggeredByMeter bool, sourceTopic string) {
	devices := e.store.Devices()
	agg := AggregateElectricity(now, devices, e.cfg.Energy.Electricity)
	if !agg.GrossSeen {
		return
	}

	e.elecMu.Lock()
	e.grossIntegrator.Update(now, agg.GrossW)
	e.monitoredIntegrator.Update(now, agg.MonitoredW)
	e.unmonitoredIntegrator.Update(now, agg.UnmonitoredW)
	summary := model.ElectricitySummary{
		GrossW:           agg.GrossW,
		MonitoredW:       agg.MonitoredW,
		UnmonitoredW:     agg.UnmonitoredW,
		StaleDeviceCount: len(agg.StaleIDs),
		StaleDevices:     agg.StaleIDs,
		GrossKWh:         e.grossIntegrator.Total(),
		MonitoredKWh:     e.monitoredIntegrator.Total(),
		UnmonitoredKWh:   e.unmonitoredIntegrator.Total(),
		ComputedAt:       now,
	}
	if agg.GrossW != 0 {
		summary.Coverage = agg.MonitoredW / agg.GrossW
	}
	e.elecMu.Unlock()

	e.store.setHouseElectricity(summary)

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
	emit("gross_kwh", s.GrossKWh, "kWh")
	emit("monitored_kwh", s.MonitoredKWh, "kWh")
	emit("unmonitored_kwh", s.UnmonitoredKWh, "kWh")
	emit("stale_device_count", float64(s.StaleDeviceCount), "")
}
