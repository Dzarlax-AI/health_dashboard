package health

// Quality validators — Layer 3, "hard-impossible" gate.
//
// Goal: drop sensor glitches at ingest time so they never poison the metric_points
// table or downstream baselines. We only filter values that are *physiologically
// impossible* (or strongly indicative of sensor malfunction), never values that
// are merely unusual. A sick user with HRV 8 ms or RHR 105 is a real signal —
// don't suppress it.
//
// References for ranges:
//   - HRV: Shaffer & Ginsberg 2017 (RMSSD typical 15–60 ms; clinical <10 still real
//     but ≤4 = sensor floor; >300 = motion artifact in PPG).
//   - HR / RHR: Spodick 1992; Avram et al. 2019. Adult resting HR floor ≈ 30 bpm
//     in elite endurance athletes; max HR ceiling is ~220 - age; allow 250 to
//     cover monitor errors during arrhythmia detection.
//   - SpO2: SpO2 < 70 is a medical emergency requiring intervention, not a daily
//     report. Apple Watch sensor bottoms out around 50 in error states.
//   - Respiratory rate: normal 12–20; >40 sustained = ventilator territory; <4 =
//     near apnea. Apple Watch only reports during sleep so artifacts cluster low.
//   - Body mass: human range with margin.
//   - Step count: per-data-point, can be a daily total or sub-day chunk. 100k cap
//     is generous (ultra-runners log ~80k); higher = double-counted source.

// qualityRange is the inclusive physiological window for a metric. Values
// strictly outside are dropped at ingest. Missing entry = no validation.
type qualityRange struct {
	Min, Max float64
}

var qualityRanges = map[string]qualityRange{
	"heart_rate_variability": {Min: 4, Max: 300},
	"heart_rate":             {Min: 25, Max: 250},
	"resting_heart_rate":     {Min: 28, Max: 150},
	"walking_heart_rate":     {Min: 40, Max: 200},
	"oxygen_saturation":      {Min: 70, Max: 100},
	"respiratory_rate":       {Min: 4, Max: 50},
	"body_mass":              {Min: 20, Max: 300},
	"body_fat_percentage":    {Min: 3, Max: 70},
	"vo2_max":                {Min: 10, Max: 90},
	"step_count":             {Min: 0, Max: 100000},
	"active_energy":          {Min: 0, Max: 15000},
	"basal_energy_burned":    {Min: 0, Max: 5000},
	"apple_exercise_time":    {Min: 0, Max: 1440}, // minutes/day; 24h hard ceiling
	"apple_stand_time":       {Min: 0, Max: 1440},
	"flights_climbed":        {Min: 0, Max: 1000},
	"sleep_total":            {Min: 0, Max: 14},
	"sleep_deep":             {Min: 0, Max: 8},
	"sleep_rem":              {Min: 0, Max: 8},
	"sleep_core":             {Min: 0, Max: 12},
	"sleep_awake":            {Min: 0, Max: 6},
	"night_sleep_total":      {Min: 0, Max: 14}, // matches sleep_total
	"nap_total":              {Min: 0, Max: 8},  // pathological if >8h of naps
	"wrist_temperature":      {Min: 25, Max: 42}, // °C; pyrexia cap
}

// IsImpossible reports whether (metric, value) is outside the physiological
// range for that metric. Metrics without a configured range always return
// false (safe default — better to keep an unvalidated point than to drop it
// blindly). NaN is always impossible.
func IsImpossible(metric string, value float64) bool {
	if value != value { // NaN check without importing math
		return true
	}
	r, ok := qualityRanges[metric]
	if !ok {
		return false
	}
	return value < r.Min || value > r.Max
}

// QualityRange returns the configured range for a metric and whether it
// exists. Used by the audit reporter and by tests.
func QualityRange(metric string) (min, max float64, ok bool) {
	r, found := qualityRanges[metric]
	if !found {
		return 0, 0, false
	}
	return r.Min, r.Max, true
}
