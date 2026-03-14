// Package applehealth parses Apple Health export XML into storage.MetricPoint values.
// It uses a streaming (token-based) XML decoder so it can handle files >2 GB
// without loading them into memory.
package applehealth

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
"strconv"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

// hkSkip lists HK type suffixes (after "Identifier") that carry no useful
// time-series data and should be silently ignored.
var hkSkip = map[string]bool{
	"":                      true,
	"SleepDurationGoal":     true, // goal setting, not a measurement
	"AudioExposureEvent":    true, // category event, no value
	"HandwashingEvent":      true,
	"ToothbrushingEvent":    true,
	"MindfulSession":        true, // handled separately as mindful_minutes
	"SleepAnalysis":         true, // handled separately
	"AppleStandHour":        true, // handled separately
}

// hkTypeMap maps HKQuantityTypeIdentifier / HKCategoryTypeIdentifier suffixes
// (everything after the last "Identifier") to (metricName, units).
// Category types that need special handling (sleep, stand hour, mindfulness)
// are handled separately in parseRecord.
var hkTypeMap = map[string][2]string{
	// Activity
	"StepCount":                    {"step_count", "count"},
	"ActiveEnergyBurned":           {"active_energy", "kcal"},
	"BasalEnergyBurned":            {"basal_energy_burned", "kcal"},
	"AppleExerciseTime":            {"apple_exercise_time", "min"},
	"AppleStandTime":               {"apple_stand_time", "min"},
	"FlightsClimbed":               {"flights_climbed", "count"},
	"DistanceWalkingRunning":       {"walking_running_distance", "km"},
	"DistanceCycling":              {"distance_cycling", "km"},
	"DistanceSwimming":             {"distance_swimming", "km"},
	"SwimmingStrokeCount":          {"swimming_stroke_count", "count"},
	"TimeInDaylight":               {"time_in_daylight", "min"},
	"PhysicalEffort":               {"physical_effort", "MET"},

	// Heart & cardio
	"HeartRate":                    {"heart_rate", "count/min"},
	"HeartRateVariabilitySDNN":     {"heart_rate_variability_sdnn", "ms"},
	"RestingHeartRate":             {"resting_heart_rate", "count/min"},
	"HeartRateRecoveryOneMinute":   {"heart_rate_recovery", "count/min"},
	"WalkingHeartRateAverage":      {"walking_heart_rate_average", "count/min"},

	// Respiratory & SpO2
	"RespiratoryRate":              {"respiratory_rate", "count/min"},
	"AppleSleepingBreathingDisturbances": {"breathing_disturbances", "count/hr"},
	"VO2Max":                       {"vo2_max", "mL/kg·min"},

	// Body measurements
	"BodyMass":                     {"body_mass", "kg"},
	"BodyFatPercentage":            {"body_fat_percentage", "%"},
	"BodyMassIndex":                {"body_mass_index", "count"},
	"LeanBodyMass":                 {"lean_body_mass", "kg"},
	"Height":                       {"height", "cm"},

	// Blood pressure (stored as separate metrics)
	"BloodPressureSystolic":        {"blood_pressure_systolic", "mmHg"},
	"BloodPressureDiastolic":       {"blood_pressure_diastolic", "mmHg"},

	// Temperature
	"AppleSleepingWristTemperature": {"wrist_temperature", "degC"},

	// Audio
	"EnvironmentalAudioExposure":        {"environmental_audio", "dBASPL"},
	"HeadphoneAudioExposure":            {"headphone_audio", "dBASPL"},
	"EnvironmentalSoundReduction":       {"environmental_sound_reduction", "dBASPL"},

	// Blood oxygen (multiple identifiers → same metric)
	"OxygenSaturation":                  {"oxygen_saturation", "%"},
	"BloodOxygen":                       {"oxygen_saturation", "%"},

	// Gait & mobility
	"WalkingSpeed":                      {"walking_speed", "km/hr"},
	"WalkingStepLength":                 {"walking_step_length", "cm"},
	"WalkingAsymmetryPercentage":        {"walking_asymmetry", "%"},
	"WalkingDoubleSupportPercentage":    {"walking_double_support", "%"},
	"AppleWalkingSteadiness":            {"walking_steadiness", "%"},
	"StairAscentSpeed":                  {"stair_ascent_speed", "ft/s"},
	"StairDescentSpeed":                 {"stair_descent_speed", "ft/s"},
	"SixMinuteWalkTestDistance":         {"six_min_walk_distance", "m"},

	// Nutrition (imported but kept separate)
	"DietaryEnergyConsumed":        {"dietary_energy", "kcal"},
	"DietaryProtein":               {"dietary_protein", "g"},
	"DietaryCarbohydrates":         {"dietary_carbs", "g"},
	"DietaryFatTotal":              {"dietary_fat", "g"},
	"DietaryFiber":                 {"dietary_fiber", "g"},
	"DietarySugar":                 {"dietary_sugar", "g"},
	"DietaryWater":                 {"dietary_water", "mL"},
	"DietaryCaffeine":              {"dietary_caffeine", "mg"},
	"NumberOfAlcoholicBeverages":   {"alcoholic_beverages", "count"},
}

// sleepValueMap maps HKCategoryValueSleepAnalysis* → metric name.
// Duration is computed from startDate/endDate and stored in hours.
// InBed is skipped — it overlaps with all stages.
var sleepValueMap = map[string]string{
	"HKCategoryValueSleepAnalysisAsleepDeep":        "sleep_deep",
	"HKCategoryValueSleepAnalysisAsleepREM":         "sleep_rem",
	"HKCategoryValueSleepAnalysisAsleepCore":        "sleep_core",
	"HKCategoryValueSleepAnalysisAwake":             "sleep_awake",
	"HKCategoryValueSleepAnalysisAsleepUnspecified": "sleep_core", // treat as core for older data
}

// ParseZip opens the zip at path, finds apple_health_export/export.xml inside,
// and streams records into emit. onProgress (may be nil) is called periodically
// with (bytesRead, totalBytes) so callers can show a progress bar.
func ParseZip(path string, emit func([]storage.MetricPoint), onProgress func(read, total int64)) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name != "apple_health_export/export.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open export.xml in zip: %w", err)
		}
		defer rc.Close()
		// Uncompressed size is known from the ZIP central directory.
		total := int64(f.UncompressedSize64)
		return ParseXML(newCountingReader(rc, total, onProgress), emit)
	}
	return fmt.Errorf("apple_health_export/export.xml not found in zip")
}

// ParseXMLFile opens an export.xml file on disk and streams it.
func ParseXMLFile(path string, emit func([]storage.MetricPoint), onProgress func(read, total int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, _ := f.Stat()
	total := int64(0)
	if info != nil {
		total = info.Size()
	}
	return ParseXML(newCountingReader(f, total, onProgress), emit)
}

// countingReader wraps an io.Reader and calls onProgress every ~1 MB read.
type countingReader struct {
	r          io.Reader
	read       int64
	total      int64
	lastReport int64
	onProgress func(read, total int64)
}

func newCountingReader(r io.Reader, total int64, onProgress func(int64, int64)) io.Reader {
	if onProgress == nil {
		return r
	}
	return &countingReader{r: r, total: total, onProgress: onProgress}
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.read += int64(n)
		if c.read-c.lastReport >= 1<<20 { // report every 1 MB
			c.onProgress(c.read, c.total)
			c.lastReport = c.read
		}
	}
	return n, err
}

// ParseXML streams XML from r, converting every <Record> element to zero or
// more MetricPoint values. emit is called with a non-empty slice every
// batchSize records.
func ParseXML(r io.Reader, emit func([]storage.MetricPoint)) error {
	const batchSize = 2000
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var batch []storage.MetricPoint

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Apple Health XML sometimes contains malformed entity refs; skip
			if isHarmlessXMLErr(err) {
				continue
			}
			return fmt.Errorf("xml decode: %w", err)
		}

		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Record" {
			continue
		}

		pts := parseRecord(attrMap(se.Attr))
		batch = append(batch, pts...)

		if len(batch) >= batchSize {
			emit(batch)
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		emit(batch)
	}
	return nil
}

// parseRecord converts a single <Record> attribute map to 0–2 MetricPoints.
func parseRecord(a map[string]string) []storage.MetricPoint {
	hkType := a["type"]
	if hkType == "" {
		return nil
	}
	// Only process Quantity and Category types; skip Correlation, Audiogram, etc.
	if !strings.HasPrefix(hkType, "HKQuantityTypeIdentifier") &&
		!strings.HasPrefix(hkType, "HKCategoryTypeIdentifier") {
		return nil
	}

	source := a["sourceName"]
	startDate := a["startDate"]
	endDate := a["endDate"]

	// ── Sleep analysis ────────────────────────────────────────────────────────
	if hkType == "HKCategoryTypeIdentifierSleepAnalysis" {
		metricName, ok := sleepValueMap[a["value"]]
		if !ok {
			return nil // InBed or unknown — skip
		}
		dur := durationHours(startDate, endDate)
		if dur <= 0 {
			return nil
		}
		pts := []storage.MetricPoint{
			{MetricName: metricName, Units: "hr", Date: startDate, Qty: dur, Source: source},
		}
		// Also emit sleep_total for every "asleep" stage (aggregated by storage layer)
		if metricName != "sleep_awake" {
			pts = append(pts, storage.MetricPoint{
				MetricName: "sleep_total", Units: "hr", Date: startDate, Qty: dur, Source: source,
			})
		}
		return pts
	}

	// ── Apple Stand Hour ──────────────────────────────────────────────────────
	if hkType == "HKCategoryTypeIdentifierAppleStandHour" {
		qty := 0.0
		if a["value"] == "HKCategoryValueAppleStandHourStood" {
			qty = 1
		}
		return []storage.MetricPoint{
			{MetricName: "apple_stand_hour", Units: "count", Date: startDate, Qty: qty, Source: source},
		}
	}

	// ── Mindful session ───────────────────────────────────────────────────────
	if hkType == "HKCategoryTypeIdentifierMindfulSession" {
		dur := durationHours(startDate, endDate) * 60 // convert to minutes
		if dur <= 0 {
			return nil
		}
		return []storage.MetricPoint{
			{MetricName: "mindful_minutes", Units: "min", Date: startDate, Qty: dur, Source: source},
		}
	}

	// ── Quantity types ────────────────────────────────────────────────────────
	suffix := hkTypeSuffix(hkType)
	if hkSkip[suffix] {
		return nil
	}

	value, err := strconv.ParseFloat(a["value"], 64)
	if err != nil || value == 0 {
		return nil
	}

	units := a["unit"]

	info, ok := hkTypeMap[suffix]
	if ok {
		if units == "" {
			units = info[1]
		}
		return []storage.MetricPoint{
			{MetricName: info[0], Units: units, Date: startDate, Qty: value, Source: source},
		}
	}

	// Unknown HK type — store under a derived snake_case name so no data is lost.
	// e.g. "HKQuantityTypeIdentifierSomeNewMetric" → "some_new_metric"
	metricName := toSnakeCase(suffix)
	if metricName == "" {
		return nil
	}
	return []storage.MetricPoint{
		{MetricName: metricName, Units: units, Date: startDate, Qty: value, Source: source},
	}
}

// hkTypeSuffix returns the part after the last "Identifier" in a HK type string.
// e.g. "HKQuantityTypeIdentifierStepCount" → "StepCount"
func hkTypeSuffix(t string) string {
	if i := strings.LastIndex(t, "Identifier"); i >= 0 {
		return t[i+len("Identifier"):]
	}
	return t
}

// attrMap converts []xml.Attr to map[name]value.
func attrMap(attrs []xml.Attr) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Name.Local] = a.Value
	}
	return m
}

const appleTimeLayout = "2006-01-02 15:04:05 -0700"

// durationHours returns (end - start) in hours, or 0 on parse error.
func durationHours(start, end string) float64 {
	s, err1 := time.Parse(appleTimeLayout, start)
	e, err2 := time.Parse(appleTimeLayout, end)
	if err1 != nil || err2 != nil {
		return 0
	}
	h := e.Sub(s).Hours()
	if h < 0 {
		return 0
	}
	return h
}

// isHarmlessXMLErr returns true for XML errors we can safely skip over
// (e.g. unknown HTML entities embedded in device strings).
func isHarmlessXMLErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "invalid character entity") ||
		strings.Contains(s, "undefined entity")
}

// toSnakeCase converts a CamelCase HK suffix to snake_case.
// e.g. "SomeNewMetric" → "some_new_metric"
func toSnakeCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32) // toLower
		} else {
			b.WriteRune(r)
		}
	}
	result := b.String()
	// Trim leading underscore if suffix started with uppercase (always true)
	return strings.TrimPrefix(result, "_")
}
