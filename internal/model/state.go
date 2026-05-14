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

// ActivityState is the device's current behavioural state. The values
// used by any given device depend on its class; cycle_power_device
// uses idle/starting/running/finishing/finished_recently, etc.
type ActivityState string

const (
	ActivityUnknown          ActivityState = "unknown"
	ActivityIdle             ActivityState = "idle"
	ActivityActive           ActivityState = "active"
	ActivityRecentlyActive   ActivityState = "recently_active"
	ActivityStarting         ActivityState = "starting"
	ActivityRunning          ActivityState = "running"
	ActivityFinishing        ActivityState = "finishing"
	ActivityFinishedRecently ActivityState = "finished_recently"
	ActivityStandby          ActivityState = "standby"
	ActivityNormalIdle       ActivityState = "normal_idle"
	ActivityActiveCycle      ActivityState = "active_cycle"
	// ActivityReporting describes measurement-only devices (climate
	// sensors, air-quality, illuminance) that have no behavioural
	// state — they just transmit readings periodically. Activity
	// transitions unknown→reporting on first contact and stays there.
	ActivityReporting ActivityState = "reporting"
)

// Activity is the activity sub-state of a device.
type Activity struct {
	State       ActivityState `json:"state"`
	Since       time.Time     `json:"since"`
	LastChanged time.Time     `json:"last_changed"`
	Confidence  float64       `json:"confidence"`
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
}

// Cycle represents one in-flight or recently-finished session of a
// power-monitored appliance.
type Cycle struct {
	Active           bool        `json:"active"`
	StartedAt        time.Time   `json:"started_at"`
	FinishedAt       *time.Time  `json:"finished_at,omitempty"`
	DurationSeconds  int64       `json:"duration_seconds"`
	Energy           CycleEnergy `json:"energy"`
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

// HouseState is the V1 conservative whole-house summary.
type HouseState string

const (
	HouseUnknown  HouseState = "unknown"
	HouseEmpty    HouseState = "empty"
	HouseOccupied HouseState = "occupied"
	HouseActive   HouseState = "active"
	HouseQuiet    HouseState = "quiet"
	HouseAsleep   HouseState = "asleep"
)

// House summarises whole-house derived state.
type House struct {
	State       HouseState `json:"state"`
	Confidence  float64    `json:"confidence"`
	LastChanged time.Time  `json:"last_changed"`
	Signals     []string   `json:"signals,omitempty"`
}

// Snapshot is the full state-engine view at one instant.
type Snapshot struct {
	GeneratedAt time.Time         `json:"generated_at"`
	House       House             `json:"house"`
	Devices     map[string]Device `json:"devices"`
}
