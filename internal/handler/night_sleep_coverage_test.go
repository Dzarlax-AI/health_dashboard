package handler

import "testing"

func TestParseMetricPayloadParsesBoundNightSleepCoverage(t *testing.T) {
	parsed, err := parseMetricPayload([]byte(`{
		"data": {
			"metrics": [{"name":"night_sleep_total","units":"hr","data":[{"date":"2026-09-10T07:00:00+02:00","source":"Apple Watch","qty":7.2}]}],
			"night_sleep_coverage": [{
				"wake_date":"2026-09-10","metric_date":"2026-09-10T07:00:00+02:00","source":"Apple Watch",
				"source_epoch":"initial","capture_completeness":"complete","sync_generation":"sync-42",
				"covered_interval_start":"2026-09-09T12:00:00+02:00","covered_interval_end":"2026-09-10T12:00:00+02:00"
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

func TestParseMetricPayloadKeepsMetricsWhenCoverageIsUnbounded(t *testing.T) {
	parsed, err := parseMetricPayload([]byte(`{"data":{"metrics":[{"name":"step_count","units":"count","data":[{"date":"2026-09-10T07:00:00+02:00","source":"Apple Watch","qty":42}]}],"night_sleep_coverage":[{"wake_date":"2026-09-10","capture_completeness":"complete"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Points) != 1 || len(parsed.NightSleepCoverage) != 0 || len(parsed.NightSleepCoverageErr) != 1 {
		t.Fatalf("parsed payload = %#v", parsed)
	}
}

func TestParseMetricPayloadKeepsMetricsWhenCoverageDoesNotBind(t *testing.T) {
	parsed, err := parseMetricPayload([]byte(`{
		"data": {
			"metrics": [{"name":"night_sleep_total","units":"hr","data":[{"date":"2026-09-10T07:00:00+02:00","source":"Apple Watch","qty":7.2}]}],
			"night_sleep_coverage": [{
				"wake_date":"2026-09-10","metric_date":"2026-09-10T08:00:00+02:00","source":"Apple Watch",
				"source_epoch":"initial","capture_completeness":"complete","sync_generation":"sync-42",
				"covered_interval_start":"2026-09-09T12:00:00+02:00","covered_interval_end":"2026-09-10T12:00:00+02:00"
			}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Points) != 1 || len(parsed.NightSleepCoverage) != 0 || len(parsed.NightSleepCoverageErr) != 1 {
		t.Fatalf("parsed payload = %#v", parsed)
	}
}
