package applehealth

import (
	"bytes"
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

func TestParseXMLFocusedEdgeFixturePinsImportSafety(t *testing.T) {
	points := collectXMLFixturePoints(t, "testdata/focused_edge_export.xml")

	if got, want := len(points), 4; got != want {
		t.Fatalf("point count = %d, want %d: %+v", got, want, points)
	}
	assertPoint(t, points, "blood_oxygen_saturation", "%", "2026-02-01 06:00:00 +0100", "Synthetic Watch", 98)
	assertPoint(t, points, "mindful_minutes", "min", "2026-02-01 09:00:00 +0100", "Synthetic Phone", 15)
	assertPoint(t, points, "apple_stand_hour", "count", "2026-02-01 10:00:00 +0100", "Synthetic Watch", 1)
	assertPoint(t, points, "some_new_metric", "count", "2026-02-01 13:00:00 +0100", "Synthetic Device", 12.5)

	assertMetricCount(t, points, "mindful_minutes", 1)
	assertNoMetric(t, points, "blood_pressure")
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
