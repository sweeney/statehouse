package model

import "time"

// Availability tracks whether the engine is currently in contact with
// the device. It is separate from activity; a device can be online but
// idle, or offline_pending while a debounce timer runs.
type Availability string

const (
	AvailabilityUnknown        Availability = "unknown"
	AvailabilityOnline         Availability = "online"
	AvailabilityOfflinePending Availability = "offline_pending"
	AvailabilityOffline        Availability = "offline"
)

// DeviceActivityState is the device's current behavioural state. The values
// used by any given device depend on its class; cycle_power_device
// uses idle/starting/running/finishing/finished_recently, etc.
type DeviceActivityState string

const (
	ActivityUnknown          DeviceActivityState = "unknown"
	ActivityIdle             DeviceActivityState = "idle"
	ActivityActive           DeviceActivityState = "active"
	ActivityStarting         DeviceActivityState = "starting"
	ActivityRunning          DeviceActivityState = "running"
	ActivityFinishing        DeviceActivityState = "finishing"
	ActivityFinishedRecently DeviceActivityState = "finished_recently"
	ActivityStandby          DeviceActivityState = "standby"
	ActivityNormalIdle       DeviceActivityState = "normal_idle"
	ActivityActiveCycle      DeviceActivityState = "active_cycle"
	// ActivityReporting describes measurement-only devices (climate
	// sensors, air-quality, illuminance) that have no behavioural
	// state — they just transmit readings periodically. Activity
	// transitions unknown→reporting on first contact and stays there.
	ActivityReporting DeviceActivityState = "reporting"
)

// Activity is the activity sub-state of a device.
type Activity struct {
	State       DeviceActivityState `json:"state"`
	Since       time.Time           `json:"since"`
	LastChanged time.Time           `json:"last_changed"`
	Confidence  float64             `json:"confidence"`
}

// Latest carries the last observed values, regardless of whether the
// device is currently active. nil-able fields keep the
// "absent vs zero" distinction.
type Latest struct {
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

	LastSeen time.Time `json:"last_seen"`
}

// CycleEnergy summarises the two parallel energy estimates for a
// session/cycle. Both values are tracked in parallel; SelectedKWh
// reflects the strategy choice for this device class.
type CycleEnergy struct {
	PrimarySource     string  `json:"primary_source"`
	ReportedKWhDelta  float64 `json:"reported_kwh_delta"`
	IntegratedKWh     float64 `json:"integrated_kwh"`
	SelectedKWh       float64 `json:"selected_kwh"`
	DivergencePct     float64 `json:"divergence_pct"`
	DivergenceWarning bool    `json:"divergence_warning"`
	StaleCounter      bool    `json:"stale_counter,omitempty"`
}

// Cycle represents one in-flight or recently-finished session of a
// power-monitored appliance.
type Cycle struct {
	Active          bool        `json:"active"`
	StartedAt       time.Time   `json:"started_at"`
	FinishedAt      *time.Time  `json:"finished_at,omitempty"`
	DurationSeconds int64       `json:"duration_seconds"`
	Energy          CycleEnergy `json:"energy"`
}

// Device is the canonical, downstream-facing view of one device.
type Device struct {
	ID           string         `json:"id"`
	DisplayName  string         `json:"display_name,omitempty"`
	Class        string         `json:"class"`
	Location     string         `json:"location,omitempty"`
	Identity     DeviceIdentity `json:"identity"`
	Availability Availability   `json:"availability"`
	Activity     Activity       `json:"activity"`
	Latest       Latest         `json:"latest"`
	Cycle        *Cycle         `json:"cycle,omitempty"`
	Unclassified bool           `json:"unclassified,omitempty"`
}

// OccupancyState is the house occupancy dimension.
type OccupancyState string

const (
	OccupancyUnknown  OccupancyState = "unknown"
	OccupancyEmpty    OccupancyState = "empty"
	OccupancyOccupied OccupancyState = "occupied"
)

// HouseActivityState is the house activity dimension.
type HouseActivityState string

const (
	HouseActivityUnknown HouseActivityState = "unknown"
	HouseActivityIdle    HouseActivityState = "idle"
	HouseActivityQuiet   HouseActivityState = "quiet"
	HouseActivityActive  HouseActivityState = "active"
	HouseActivityBusy    HouseActivityState = "busy"
)

// ModeState is the house behavioural mode dimension.
type ModeState string

const (
	ModeUnknown  ModeState = "unknown"
	ModeDay      ModeState = "day"
	ModeNight    ModeState = "night"
	ModeAway     ModeState = "away"
	ModeSleeping ModeState = "sleeping"
)

// OccupancyDimension holds the occupancy inference.
type OccupancyDimension struct {
	State       OccupancyState `json:"state"`
	Confidence  float64        `json:"confidence"`
	LastChanged time.Time      `json:"last_changed"`
}

// HouseActivityDimension holds the house activity inference.
type HouseActivityDimension struct {
	State       HouseActivityState `json:"state"`
	Confidence  float64            `json:"confidence"`
	LastChanged time.Time          `json:"last_changed"`
}

// ModeDimension holds the behavioural mode inference.
type ModeDimension struct {
	State       ModeState `json:"state"`
	Confidence  float64   `json:"confidence"`
	LastChanged time.Time `json:"last_changed"`
}

// ElectricitySummary is the whole-house electricity view: gross (from
// the meter), the sum of per-device power readings (monitored), and
// the residual (unmonitored). Unmonitored and Coverage are exposed raw
// — Coverage may exceed 1 and UnmonitoredW may be negative when the
// device sum briefly overruns the meter (different sample cadences,
// apparent-vs-active power on some plugs). Consumers decide whether to
// clip cosmetically.
type ElectricitySummary struct {
	GrossW           float64   `json:"gross_w"`
	MonitoredW       float64   `json:"monitored_w"`
	UnmonitoredW     float64   `json:"unmonitored_w"`
	Coverage         float64   `json:"coverage"`
	StaleDeviceCount int       `json:"stale_device_count"`
	StaleDevices     []string  `json:"stale_devices,omitempty"`
	GrossKWh         float64   `json:"gross_kwh"`
	MonitoredKWh     float64   `json:"monitored_kwh"`
	UnmonitoredKWh   float64   `json:"unmonitored_kwh"`
	ComputedAt       time.Time `json:"computed_at"`
}

// House summarises whole-house derived state across three independent
// semantic dimensions: occupancy, activity, and mode.
type House struct {
	Occupancy     OccupancyDimension     `json:"occupancy"`
	Activity      HouseActivityDimension `json:"activity"`
	Mode          ModeDimension          `json:"mode"`
	ActiveDevices []string               `json:"active_devices"`
	Electricity   ElectricitySummary     `json:"electricity"`
}

// Snapshot is the full state-engine view at one instant.
type Snapshot struct {
	GeneratedAt time.Time         `json:"generated_at"`
	House       House             `json:"house"`
	Devices     map[string]Device `json:"devices"`
}
