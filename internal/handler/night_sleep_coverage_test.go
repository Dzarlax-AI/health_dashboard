package handler

import (
	"strings"
	"testing"
)

func TestParseMetricPayloadParsesBoundNightSleepCoverage(t *testing.T) {
	parsed, err := parseMetricPayload([]byte(`{
		"data": {
			"metrics": [{"name":"night_sleep_total","units":"hr","data":[{"date":"2026-09-10T07:00:00+02:00","source":"Apple Watch","qty":7.2}]}],
			"night_sleep_coverage": [{
				"wake_date":"2026-09-10","metric_date":"2026-09-10T07:00:00+02:00","source":"Apple Watch",
				"source_epoch":"initial","capture_completeness":"complete","sync_generation":"sync-42",
				"covered_interval_start":"2026-09-09T20:00:00+02:00","covered_interval_end":"2026-09-10T08:00:00+02:00"
			}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Points) != 1 || len(parsed.NightSleepCoverage) != 1 {
		t.Fatalf("parsed payload = %#v", parsed)
	}
	coverage := parsed.NightSleepCoverage[0]
	if coverage.InputHash == "" || !containsCommittedNightPoint(parsed.Points, coverage) {
		t.Fatalf("coverage was not tied to exact raw point: %#v", coverage)
	}
}

func TestParseMetricPayloadRejectsUnboundedNightSleepCoverage(t *testing.T) {
	_, err := parseMetricPayload([]byte(`{"data":{"night_sleep_coverage":[{"wake_date":"2026-09-10","capture_completeness":"complete"}]}}`))
	if err == nil || !strings.Contains(err.Error(), "requires metric date") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseMetricPayloadRejectsCoverageWithoutItsExactRawPoint(t *testing.T) {
	_, err := parseMetricPayload([]byte(`{
		"data": {
			"metrics": [{"name":"night_sleep_total","units":"hr","data":[{"date":"2026-09-10T07:00:00+02:00","source":"Apple Watch","qty":7.2}]}],
			"night_sleep_coverage": [{
				"wake_date":"2026-09-10","metric_date":"2026-09-10T08:00:00+02:00","source":"Apple Watch",
				"source_epoch":"initial","capture_completeness":"complete","sync_generation":"sync-42",
				"covered_interval_start":"2026-09-09T20:00:00+02:00","covered_interval_end":"2026-09-10T08:00:00+02:00"
			}]
		}
	}`))
	if err == nil || !strings.Contains(err.Error(), "does not bind") {
		t.Fatalf("error = %v", err)
	}
}
