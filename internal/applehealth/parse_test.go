package applehealth

import (
	"bytes"
	"math"
	"os"
	"strings"
	"testing"

	"health-receiver/internal/storage"
)

func TestParseXMLSyntheticFixtureMapsHealthKitRecords(t *testing.T) {
	points := collectXMLFixturePoints(t, "testdata/synthetic_export.xml")

	assertPoint(t, points, "step_count", "count", "2026-01-02 10:00:00 +0000", "Synthetic Watch", 42)
	assertPoint(t, points, "heart_rate_variability", "ms", "2026-01-02 06:00:00 +0000", "Synthetic Watch", 55)
	assertPoint(t, points, "blood_oxygen_saturation", "%", "2026-01-02 06:02:00 +0000", "Synthetic Watch", 97)
	assertPoint(t, points, "body_fat_percentage", "%", "2026-01-02 07:00:00 +0000", "Synthetic Scale", 21.5)
	assertPoint(t, points, "sleep_core", "hr", "2026-01-01 23:00:00 +0000", "Synthetic Watch", 2)
	assertPoint(t, points, "sleep_deep", "hr", "2026-01-02 01:00:00 +0000", "Synthetic Watch", 0.5)
	assertPoint(t, points, "sleep_rem", "hr", "2026-01-02 01:30:00 +0000", "Synthetic Watch", 0.5)
	assertPoint(t, points, "sleep_awake", "hr", "2026-01-02 02:00:00 +0000", "Synthetic Watch", 0.25)
	assertPoint(t, points, "sleep_unspecified", "hr", "2026-01-02 23:30:00 +0000", "Synthetic Phone", 6)

	assertMetricCount(t, points, "sleep_total", 4)
	assertNoPoint(t, points, "sleep_total", "2026-01-02 02:00:00 +0000")
}

func TestParseXMLSyntheticFixtureMapsWorkouts(t *testing.T) {
	var points []storage.MetricPoint
	var workouts []storage.Workout
	if err := parseXMLFixtureWithOptions("testdata/synthetic_export.xml", EmitOptions{
		Points: func(batch []storage.MetricPoint) {
			points = append(points, batch...)
		},
		Workouts: func(batch []storage.Workout) {
			workouts = append(workouts, batch...)
		},
	}); err != nil {
		t.Fatalf("ParseXMLWithOptions: %v", err)
	}

	if got, want := len(workouts), 2; got != want {
		t.Fatalf("workout count = %d, want %d: %+v", got, want, workouts)
	}
	if len(points) == 0 {
		t.Fatal("ParseXMLWithOptions did not emit metric points")
	}

	run := workouts[0]
	if run.ExternalID != "run-sync-1" {
		t.Fatalf("run external id = %q, want sync id", run.ExternalID)
	}
	if run.Name != "Outdoor Run" {
		t.Fatalf("run name = %q, want Outdoor Run", run.Name)
	}
	if run.DurationSec != 2700 {
		t.Fatalf("run duration = %v, want 2700", run.DurationSec)
	}
	if run.IsIndoor {
		t.Fatal("run IsIndoor = true, want false")
	}
	assertFloatPtr(t, run.EnergyKcal, 500, "run energy")
	assertFloatPtr(t, run.DistanceKm, 10, "run distance")
	assertFloatPtr(t, run.AvgHRBPM, 150, "run avg HR")
	assertFloatPtr(t, run.MaxHRBPM, 180, "run max HR")
	assertFloatPtr(t, run.AvgSpeedKmh, 9, "run avg speed")
	assertFloatPtr(t, run.MaxSpeedKmh, 14.4, "run max speed")
	assertFloatPtr(t, run.ElevationUpM, 10.0584, "run elevation")
	assertFloatPtr(t, run.TemperatureC, 20, "run temperature")
	assertFloatPtr(t, run.HumidityPct, 55, "run humidity")

	strength := workouts[1]
	if !strings.HasPrefix(strength.ExternalID, "applexml:") {
		t.Fatalf("strength fallback external id = %q, want applexml prefix", strength.ExternalID)
	}
	if strength.Name != "Functional Strength Training" {
		t.Fatalf("strength name = %q, want Functional Strength Training", strength.Name)
	}
	if !strength.IsIndoor {
		t.Fatal("strength IsIndoor = false, want true")
	}
	if strength.DurationSec != 1800 {
		t.Fatalf("strength duration = %v, want 1800", strength.DurationSec)
	}
	assertFloatPtr(t, strength.EnergyKcal, 28.68072, "strength energy")
}

func TestParseXMLFocusedEdgeFixturePinsImportSafety(t *testing.T) {
	points := collectXMLFixturePoints(t, "testdata/focused_edge_export.xml")

	if got, want := len(points), 5; got != want {
		t.Fatalf("point count = %d, want %d: %+v", got, want, points)
	}
	assertPoint(t, points, "blood_oxygen_saturation", "%", "2026-02-01 06:00:00 +0100", "Synthetic Watch", 98)
	assertPoint(t, points, "walking_asymmetry", "%", "2026-02-01 06:05:00 +0100", "Synthetic Watch", 0)
	assertNoPoint(t, points, "heart_rate", "2026-02-01 06:10:00 +0100")
	assertPoint(t, points, "mindful_minutes", "min", "2026-02-01 09:00:00 +0100", "Synthetic Phone", 15)
	assertPoint(t, points, "apple_stand_hour", "count", "2026-02-01 10:00:00 +0100", "Synthetic Watch", 1)
	assertPoint(t, points, "some_new_metric", "count", "2026-02-01 13:00:00 +0100", "Synthetic Device", 12.5)

	assertMetricCount(t, points, "mindful_minutes", 1)
	assertNoMetric(t, points, "blood_pressure")
}

func TestParseWorkoutValidationAndCanonicalNames(t *testing.T) {
	if _, ok := parseWorkout(xmlWorkout{}); ok {
		t.Fatal("empty workout parsed, want rejected")
	}
	if _, ok := parseWorkout(xmlWorkout{
		ActivityType: "HKWorkoutActivityTypeRunning",
		StartDate:    "2026-01-02 12:45:00 +0000",
		EndDate:      "2026-01-02 12:00:00 +0000",
	}); ok {
		t.Fatal("workout with end before start parsed, want rejected")
	}

	indoor, ok := parseWorkout(xmlWorkout{
		ActivityType: "HKWorkoutActivityTypeCycling",
		Duration:     "30",
		DurationUnit: "min",
		StartDate:    "2026-01-02 12:00:00 +0000",
		EndDate:      "2026-01-02 12:30:00 +0000",
		Metadata:     []xmlMetadataEntry{{Key: "HKIndoorWorkout", Value: "1"}},
	})
	if !ok {
		t.Fatal("valid indoor cycling workout rejected")
	}
	if indoor.Name != "Indoor Cycling" {
		t.Fatalf("indoor cycling name = %q, want Indoor Cycling", indoor.Name)
	}

	outdoor, ok := parseWorkout(xmlWorkout{
		ActivityType: "HKWorkoutActivityTypeCycling",
		Duration:     "30",
		DurationUnit: "min",
		StartDate:    "2026-01-02 12:00:00 +0000",
		EndDate:      "2026-01-02 12:30:00 +0000",
	})
	if !ok {
		t.Fatal("valid outdoor cycling workout rejected")
	}
	if outdoor.Name != "Outdoor Cycling" {
		t.Fatalf("outdoor cycling name = %q, want Outdoor Cycling", outdoor.Name)
	}
}

func TestParseXMLEmptyAndMalformedInputs(t *testing.T) {
	var emitted bool
	if err := parseXMLFixture("testdata/empty_export.xml", func(points []storage.MetricPoint) {
		emitted = true
	}); err != nil {
		t.Fatalf("ParseXML empty health data: %v", err)
	}
	if emitted {
		t.Fatal("ParseXML emitted points for empty health data")
	}

	err := parseXMLFixture("testdata/malformed_export.xml", func([]storage.MetricPoint) {})
	if err == nil {
		t.Fatal("ParseXML malformed XML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "xml decode") {
		t.Fatalf("ParseXML malformed error = %v, want xml decode wrapper", err)
	}
}

func TestExportDateFromXML(t *testing.T) {
	tm, ok, err := exportDateFromXML(strings.NewReader(`<?xml version="1.0"?>
<HealthData locale="en_US" exportDate="2026-07-05 14:15:16 +0200">
  <Record type="HKQuantityTypeIdentifierStepCount" sourceName="Watch" unit="count" startDate="2026-07-05 10:00:00 +0200" endDate="2026-07-05 10:00:00 +0200" value="1"/>
</HealthData>`))
	if err != nil {
		t.Fatalf("exportDateFromXML: %v", err)
	}
	if !ok {
		t.Fatal("exportDateFromXML ok = false, want true")
	}
	if got := tm.Format(appleTimeLayout); got != "2026-07-05 14:15:16 +0200" {
		t.Fatalf("exportDate = %q, want 2026-07-05 14:15:16 +0200", got)
	}
}

func collectXMLFixturePoints(t *testing.T, path string) []storage.MetricPoint {
	t.Helper()
	var out []storage.MetricPoint
	if err := parseXMLFixture(path, func(points []storage.MetricPoint) {
		out = append(out, points...)
	}); err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	return out
}

func parseXMLFixture(path string, emit func([]storage.MetricPoint)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return ParseXML(bytes.NewReader(data), emit)
}

func parseXMLFixtureWithOptions(path string, opts EmitOptions) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return ParseXMLWithOptions(bytes.NewReader(data), opts)
}

func assertPoint(t *testing.T, points []storage.MetricPoint, metric, units, date, source string, qty float64) {
	t.Helper()
	for _, p := range points {
		if p.MetricName == metric && p.Date == date && p.Source == source {
			if p.Units != units || p.Qty != qty {
				t.Fatalf("%s at %s = units %q qty %v, want units %q qty %v", metric, date, p.Units, p.Qty, units, qty)
			}
			return
		}
	}
	t.Fatalf("missing point metric=%s date=%s source=%s in %+v", metric, date, source, points)
}

func assertFloatPtr(t *testing.T, got *float64, want float64, label string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", label, want)
	}
	if math.Abs(*got-want) > 0.00001 {
		t.Fatalf("%s = %v, want %v", label, *got, want)
	}
}

func assertNoPoint(t *testing.T, points []storage.MetricPoint, metric, date string) {
	t.Helper()
	for _, p := range points {
		if p.MetricName == metric && p.Date == date {
			t.Fatalf("unexpected point metric=%s date=%s: %+v", metric, date, p)
		}
	}
}

func assertNoMetric(t *testing.T, points []storage.MetricPoint, metric string) {
	t.Helper()
	for _, p := range points {
		if p.MetricName == metric {
			t.Fatalf("unexpected metric=%s point: %+v", metric, p)
		}
	}
}

func assertMetricCount(t *testing.T, points []storage.MetricPoint, metric string, want int) {
	t.Helper()
	got := 0
	for _, p := range points {
		if p.MetricName == metric {
			got++
		}
	}
	if got != want {
		t.Fatalf("metric %s count = %d, want %d; points=%+v", metric, got, want, points)
	}
}
