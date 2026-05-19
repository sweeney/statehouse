package httpapi

import (
	"time"

	"github.com/sweeney/statehouse/internal/device"
	"github.com/sweeney/statehouse/internal/model"
)

const schemaVersion = "net.swee.statehouse.snapshot.v1"

// activeActivityStates is the set of activity states that count as "active"
// for the summary's ActiveCount.
var activeActivityStates = map[model.DeviceActivityState]bool{
	model.ActivityActive:          true,
	model.ActivityStarting:        true,
	model.ActivityRunning:         true,
	model.ActivityFinishing:       true,
	model.ActivityFinishedRecently: true,
	model.ActivityActiveCycle:     true,
}

// stalenessSecondsForClass returns the staleness threshold (in seconds) for a
// given device class. When the DeviceClassConfig has a non-nil StalenessSeconds
// field, that wins.
func stalenessSecondsForClass(class string, staleness *int) float64 {
	if staleness != nil {
		return float64(*staleness)
	}
	switch class {
	case device.ClassShortBurst, device.ClassCyclePower, device.ClassContinuous, device.ClassMedia:
		return 900
	case device.ClassBinaryState:
		return 3600
	default:
		return 3600
	}
}

// cycleTypeForClass returns the cycle type label for a given device class.
func cycleTypeForClass(class string) string {
	switch class {
	case device.ClassShortBurst, device.ClassCyclePower, device.ClassMedia:
		return "appliance_cycle"
	case device.ClassContinuous:
		return "compressor_cycle"
	case device.ClassBinaryState:
		return "binary_cycle"
	default:
		return "unknown"
	}
}

// nilIfZero returns nil if t is the zero time, otherwise a pointer to t.
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// agoInt returns the elapsed seconds since t, rounded to the nearest int,
// or nil if t is zero.
func agoInt(t time.Time, now time.Time) *int {
	if t.IsZero() {
		return nil
	}
	v := int((now.Sub(t) + 500*time.Millisecond) / time.Second)
	return &v
}

// SnapshotResponse is the top-level DTO returned by GET /state.
type SnapshotResponse struct {
	SchemaVersion string                    `json:"schema_version"`
	GeneratedAt   time.Time                 `json:"generated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	StartedAgo *int       `json:"started_ago,omitempty"`
	Summary       SummaryResponse           `json:"summary"`
	House         HouseResponse             `json:"house"`
	Devices       map[string]DeviceResponse `json:"devices"`
	Activity      ActivityStateResponse     `json:"activity"`
}

// SummaryResponse contains aggregate counts across all devices.
type SummaryResponse struct {
	DeviceCount  int `json:"device_count"`
	OnlineCount  int `json:"online_count"`
	ActiveCount  int `json:"active_count"`
	WarningCount int `json:"warning_count"`
}

// HouseOccupancyResponse is the DTO for the occupancy dimension.
type HouseOccupancyResponse struct {
	State          model.OccupancyState `json:"state"`
	Confidence     float64              `json:"confidence"`
	LastChanged    *time.Time           `json:"last_changed"`
	LastChangedAgo *int                 `json:"last_changed_ago,omitempty"`
}

// HouseActivityResponse is the DTO for the house activity dimension.
type HouseActivityResponse struct {
	State          model.HouseActivityState `json:"state"`
	Confidence     float64                  `json:"confidence"`
	LastChanged    *time.Time               `json:"last_changed"`
	LastChangedAgo *int                     `json:"last_changed_ago,omitempty"`
}

// HouseModeResponse is the DTO for the mode dimension.
type HouseModeResponse struct {
	State          model.ModeState `json:"state"`
	Confidence     float64         `json:"confidence"`
	LastChanged    *time.Time      `json:"last_changed"`
	LastChangedAgo *int            `json:"last_changed_ago,omitempty"`
}

// HouseResponse is the DTO for the whole-house state.
type HouseResponse struct {
	Occupancy     HouseOccupancyResponse `json:"occupancy"`
	Activity      HouseActivityResponse  `json:"activity"`
	Mode          HouseModeResponse      `json:"mode"`
	ActiveDevices []string               `json:"active_devices"`
}

// DeviceResponse is the DTO for a single device.
type DeviceResponse struct {
	ID           string               `json:"id"`
	DisplayName  string               `json:"display_name,omitempty"`
	Class        string               `json:"class"`
	Location     string               `json:"location,omitempty"`
	Availability model.Availability   `json:"availability"`
	Activity     ActivityResponse     `json:"activity"`
	Latest       LatestResponse       `json:"latest"`
	Cycle        *CycleResponse       `json:"cycle,omitempty"`
	Unclassified bool                 `json:"unclassified,omitempty"`
	Warnings     []string             `json:"warnings"`
}

// ActivityResponse is the DTO for a device's activity sub-state.
type ActivityResponse struct {
	State          model.DeviceActivityState `json:"state"`
	LastChanged    *time.Time                `json:"last_changed"`
	LastChangedAgo *int                      `json:"last_changed_ago"`
	Confidence     float64                   `json:"confidence"`
}

// LatestResponse is the DTO for the latest observed values of a device.
// It mirrors model.Latest but with a pointer LastSeen, a computed AgeSeconds,
// and a Stale flag.
type LatestResponse struct {
	// Power / energy
	PowerW    *float64 `json:"power_w,omitempty"`
	VoltageV  *float64 `json:"voltage_v,omitempty"`
	EnergyKWh *float64 `json:"energy_kwh,omitempty"`

	// Environment
	TemperatureC   *float64 `json:"temperature_c,omitempty"`
	HumidityPct    *float64 `json:"humidity_pct,omitempty"`
	PressureHPa    *float64 `json:"pressure_hpa,omitempty"`
	WindSpeedMS    *float64 `json:"wind_speed_ms,omitempty"`
	WindDirDeg     *float64 `json:"wind_dir_deg,omitempty"`
	RainfallMM     *float64 `json:"rainfall_mm,omitempty"`
	IlluminanceLux *float64 `json:"illuminance_lux,omitempty"`
	UVIndex        *float64 `json:"uv_index,omitempty"`

	// UPS
	BatteryRuntimeMins *float64 `json:"battery_runtime_mins,omitempty"`
	OnBattery          *bool    `json:"on_battery,omitempty"`
	LowBattery         *bool    `json:"low_battery,omitempty"`

	// Device health
	BatteryPct  *float64 `json:"battery_pct,omitempty"`
	LinkQuality *int     `json:"linkquality,omitempty"`
	RSSI        *int     `json:"rssi_dbm,omitempty"`

	LastSeen   *time.Time `json:"last_seen"`
	LastSeenAgo *int      `json:"last_seen_ago"`
	Stale      bool       `json:"stale"`
}

// DivergenceResponse describes the energy divergence status for a cycle.
type DivergenceResponse struct {
	Status  string   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	Pct     *float64 `json:"pct,omitempty"`
	Warning *bool    `json:"warning,omitempty"`
}

// CycleEnergyResponse is the DTO for a cycle's energy accounting.
type CycleEnergyResponse struct {
	PrimarySource    string             `json:"primary_source"`
	ReportedKWhDelta float64            `json:"reported_kwh_delta"`
	IntegratedKWh    float64            `json:"integrated_kwh"`
	SelectedKWh      float64            `json:"selected_kwh"`
	StaleCounter     bool               `json:"stale_counter,omitempty"`
	Divergence       DivergenceResponse `json:"divergence"`
}

// CycleResponse is the DTO for an in-flight or recently-finished cycle.
type CycleResponse struct {
	Type            string              `json:"type"`
	Active          bool                `json:"active"`
	StartedAt       *time.Time          `json:"started_at"`
	FinishedAt      *time.Time          `json:"finished_at,omitempty"`
	DurationSeconds int64               `json:"duration_seconds"`
	Energy          CycleEnergyResponse `json:"energy"`
}

// BuildSnapshot is the exported entry point for other packages (MQTT
// publisher) that want the same DTO shape as GET /state — same
// schema_version, summary, warnings, staleness. lookupStaleness may be
// nil to use class defaults. Pass a zero startedAt to omit uptime fields.
func BuildSnapshot(snap model.Snapshot, signals []model.ActivitySignal, records []model.ActivityRecord, now time.Time, lookupStaleness func(class string) *int, startedAt time.Time) SnapshotResponse {
	return buildSnapshot(snap, signals, records, now, lookupStaleness, startedAt)
}

// BuildHouseResponse is the exported HTTP-DTO builder for model.House.
func BuildHouseResponse(h model.House, now time.Time) HouseResponse { return buildHouseResponse(h, now) }

// BuildDeviceResponse is the exported HTTP-DTO builder for model.Device.
// stalenessSeconds may be nil to use the class default.
func BuildDeviceResponse(d model.Device, now time.Time, stalenessSeconds *int) DeviceResponse {
	return buildDeviceResponse(d, now, stalenessSeconds)
}

// buildSnapshot converts a model.Snapshot into a SnapshotResponse. now is used
// to compute age/staleness so tests can inject a fixed value. lookupStaleness
// returns the per-class override (nil → class default); pass nil if not needed.
// startedAt, when non-zero, populates started_at and started_ago.
func buildSnapshot(snap model.Snapshot, signals []model.ActivitySignal, records []model.ActivityRecord, now time.Time, lookupStaleness func(class string) *int, startedAt time.Time) SnapshotResponse {
	if lookupStaleness == nil {
		lookupStaleness = func(string) *int { return nil }
	}
	devices := make(map[string]DeviceResponse, len(snap.Devices))
	for id, d := range snap.Devices {
		devices[id] = buildDeviceResponse(d, now, lookupStaleness(d.Class))
	}

	summary := buildSummary(devices)

	r := SnapshotResponse{
		SchemaVersion: schemaVersion,
		GeneratedAt:   snap.GeneratedAt,
		Summary:       summary,
		House:         buildHouseResponse(snap.House, now),
		Devices:       devices,
		Activity:      buildActivityStateResponse(signals, records, now),
	}
	if !startedAt.IsZero() {
		r.StartedAt = &startedAt
		r.StartedAgo = agoInt(startedAt, now)
	}
	return r
}

// SignalResponse is the DTO for one ActivitySignal in GET /state/activity.
type SignalResponse struct {
	ID         string         `json:"id"`
	Source     string         `json:"source"`
	Location   string         `json:"location,omitempty"`
	Type       string         `json:"type"`
	Confidence float64        `json:"confidence"`
	Since      time.Time      `json:"since"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// ActivityRecordResponse is the DTO for one entry in the recent-activity log.
type ActivityRecordResponse struct {
	ID        string         `json:"id"`
	Source    string         `json:"source"`
	Location  string         `json:"location,omitempty"`
	Type      string         `json:"type"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// ActivityStateResponse is returned by GET /state/activity.
type ActivityStateResponse struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Signals     []SignalResponse         `json:"signals"`
	Recent      []ActivityRecordResponse `json:"recent"`
}

// buildActivityStateResponse converts active signals and recent records into the API DTO.
func buildActivityStateResponse(signals []model.ActivitySignal, records []model.ActivityRecord, now time.Time) ActivityStateResponse {
	out := ActivityStateResponse{
		GeneratedAt: now,
		Signals:     make([]SignalResponse, 0, len(signals)),
		Recent:      make([]ActivityRecordResponse, 0, len(records)),
	}
	for _, s := range signals {
		sr := SignalResponse{
			ID:         s.ID,
			Source:     s.Source,
			Location:   s.Location,
			Type:       s.Type,
			Confidence: s.Confidence,
			Since:      s.Since,
			Meta:       s.Meta,
		}
		if !s.ExpiresAt.IsZero() {
			sr.ExpiresAt = &s.ExpiresAt
		}
		out.Signals = append(out.Signals, sr)
	}
	for _, r := range records {
		out.Recent = append(out.Recent, ActivityRecordResponse{
			ID:        r.ID,
			Source:    r.Source,
			Location:  r.Location,
			Type:      r.Type,
			StartedAt: r.StartedAt,
			EndedAt:   r.EndedAt,
			Meta:      r.Meta,
		})
	}
	return out
}

// buildSummary computes the aggregate counts from the already-built device DTOs.
func buildSummary(devices map[string]DeviceResponse) SummaryResponse {
	s := SummaryResponse{
		DeviceCount: len(devices),
	}
	for _, d := range devices {
		if d.Availability == model.AvailabilityOnline {
			s.OnlineCount++
		}
		if d.Class != device.ClassContinuous && activeActivityStates[d.Activity.State] {
			s.ActiveCount++
		}
		if len(d.Warnings) > 0 {
			s.WarningCount++
		}
	}
	return s
}

// buildHouseResponse converts a model.House into a HouseResponse.
func buildHouseResponse(h model.House, now time.Time) HouseResponse {
	activeDevices := h.ActiveDevices
	if activeDevices == nil {
		activeDevices = []string{}
	}
	return HouseResponse{
		Occupancy: HouseOccupancyResponse{
			State:          h.Occupancy.State,
			Confidence:     h.Occupancy.Confidence,
			LastChanged:    nilIfZero(h.Occupancy.LastChanged),
			LastChangedAgo: agoInt(h.Occupancy.LastChanged, now),
		},
		Activity: HouseActivityResponse{
			State:          h.Activity.State,
			Confidence:     h.Activity.Confidence,
			LastChanged:    nilIfZero(h.Activity.LastChanged),
			LastChangedAgo: agoInt(h.Activity.LastChanged, now),
		},
		Mode: HouseModeResponse{
			State:          h.Mode.State,
			Confidence:     h.Mode.Confidence,
			LastChanged:    nilIfZero(h.Mode.LastChanged),
			LastChangedAgo: agoInt(h.Mode.LastChanged, now),
		},
		ActiveDevices: activeDevices,
	}
}

// buildDeviceResponse converts a model.Device into a DeviceResponse.
// stalenessSeconds may be nil to use the class default.
func buildDeviceResponse(d model.Device, now time.Time, stalenessSeconds *int) DeviceResponse {
	warnings := []string{}

	latest, stale := buildLatestResponse(d.Latest, d.Class, now, stalenessSeconds)
	if stale {
		warnings = append(warnings, "stale_device")
	}
	if d.Cycle != nil && d.Cycle.Energy.DivergenceWarning {
		warnings = append(warnings, "cycle_divergence")
	}
	if d.Cycle != nil && d.Cycle.Energy.StaleCounter {
		warnings = append(warnings, "stale_counter")
	}

	return DeviceResponse{
		ID:           d.ID,
		DisplayName:  d.DisplayName,
		Class:        d.Class,
		Location:     d.Location,
		Availability: d.Availability,
		Activity:     buildActivityResponse(d.Activity, now),
		Latest:       latest,
		Cycle:        buildCycleResponse(d.Cycle, d.Class),
		Unclassified: d.Unclassified,
		Warnings:     warnings,
	}
}

// buildActivityResponse converts a model.Activity into an ActivityResponse.
func buildActivityResponse(a model.Activity, now time.Time) ActivityResponse {
	return ActivityResponse{
		State:          a.State,
		LastChanged:    nilIfZero(a.LastChanged),
		LastChangedAgo: agoInt(a.LastChanged, now),
		Confidence:     a.Confidence,
	}
}

// buildLatestResponse converts a model.Latest into a LatestResponse, computing
// staleness. Returns the response and a bool indicating staleness.
func buildLatestResponse(l model.Latest, class string, now time.Time, stalenessSeconds *int) (LatestResponse, bool) {
	r := LatestResponse{
		PowerW:             l.PowerW,
		VoltageV:           l.VoltageV,
		EnergyKWh:          l.EnergyKWh,
		TemperatureC:       l.TemperatureC,
		HumidityPct:        l.HumidityPct,
		PressureHPa:        l.PressureHPa,
		WindSpeedMS:        l.WindSpeedMS,
		WindDirDeg:         l.WindDirDeg,
		RainfallMM:         l.RainfallMM,
		IlluminanceLux:     l.IlluminanceLux,
		UVIndex:            l.UVIndex,
		BatteryRuntimeMins: l.BatteryRuntimeMins,
		OnBattery:          l.OnBattery,
		LowBattery:         l.LowBattery,
		BatteryPct:         l.BatteryPct,
		LinkQuality:        l.LinkQuality,
		RSSI:               l.RSSI,
		LastSeen:           nilIfZero(l.LastSeen),
	}

	stale := false
	if !l.LastSeen.IsZero() {
		r.LastSeenAgo = agoInt(l.LastSeen, now)
		threshold := stalenessSecondsForClass(class, stalenessSeconds)
		if now.Sub(l.LastSeen).Seconds() >= threshold {
			stale = true
		}
	}
	r.Stale = stale

	return r, stale
}

// buildCycleResponse converts a *model.Cycle into a *CycleResponse, or nil.
func buildCycleResponse(c *model.Cycle, class string) *CycleResponse {
	if c == nil {
		return nil
	}

	var div DivergenceResponse
	if c.Active {
		div = DivergenceResponse{
			Status: "pending",
			Reason: "cycle_active",
		}
	} else {
		status := "ok"
		if c.Energy.DivergenceWarning {
			status = "warning"
		}
		pct := c.Energy.DivergencePct
		warn := c.Energy.DivergenceWarning
		div = DivergenceResponse{
			Status:  status,
			Pct:     &pct,
			Warning: &warn,
		}
	}

	return &CycleResponse{
		Type:            cycleTypeForClass(class),
		Active:          c.Active,
		StartedAt:       nilIfZero(c.StartedAt),
		FinishedAt:      c.FinishedAt,
		DurationSeconds: c.DurationSeconds,
		Energy: CycleEnergyResponse{
			PrimarySource:    c.Energy.PrimarySource,
			ReportedKWhDelta: c.Energy.ReportedKWhDelta,
			IntegratedKWh:    c.Energy.IntegratedKWh,
			SelectedKWh:      c.Energy.SelectedKWh,
			StaleCounter:     c.Energy.StaleCounter,
			Divergence:       div,
		},
	}
}
