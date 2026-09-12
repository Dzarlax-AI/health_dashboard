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

func TestParseMetricPayloadBindsCompletedEpisodesToCompletePeriodCoverage(t *testing.T) {
	parsed, err := parseMetricPayload([]byte(`{
		"data": {
			"sleep_period_coverage": [{
				"wake_date":"2026-09-10","source_epoch":"health-sync-ios-v1","capture_completeness":"complete","sync_generation":"period-42",
				"covered_interval_start":"2026-09-09T10:00:00Z","covered_interval_end":"2026-09-10T10:00:00Z"
			}],
			"completed_sleep_episodes": [{
				"wake_date":"2026-09-10","start":"2026-09-09T22:00:00Z","end":"2026-09-10T06:00:00Z","source":"Apple Watch",
				"source_epoch":"health-sync-ios-v1","sync_generation":"period-42"
			}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.SleepPeriodCoverage) != 1 || len(parsed.CompletedSleepEpisodes["2026-09-10"]) != 1 {
		t.Fatalf("parsed period payload = %#v", parsed)
	}
	episode := parsed.CompletedSleepEpisodes["2026-09-10"][0]
	if episode.InputHash == "" || episode.EpisodeID == "" || episode.CoverageGeneration != "period-42" {
		t.Fatalf("episode did not receive server identity: %#v", episode)
	}
}

func TestParseMetricPayloadKeepsCompleteEmptySleepPeriod(t *testing.T) {
	parsed, err := parseMetricPayload([]byte(`{
		"data": {
			"metrics": [],
			"sleep_period_coverage": [{
				"wake_date":"2026-09-10","source_epoch":"health-sync-ios-v1","capture_completeness":"complete","sync_generation":"empty-period-42",
				"covered_interval_start":"2026-09-09T10:00:00Z","covered_interval_end":"2026-09-10T10:00:00Z"
			}],
			"completed_sleep_episodes": []
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Points) != 0 || len(parsed.SleepPeriodCoverage) != 1 {
		t.Fatalf("parsed empty period payload = %#v", parsed)
	}
	if episodes, found := parsed.CompletedSleepEpisodes["2026-09-10"]; found || len(episodes) != 0 {
		t.Fatalf("empty period unexpectedly has episodes: %#v", parsed.CompletedSleepEpisodes)
	}
}

func TestParseMetricPayloadRejectsEpisodeWithoutMatchingCoverageGeneration(t *testing.T) {
	_, err := parseMetricPayload([]byte(`{
		"data": {
			"sleep_period_coverage": [{
				"wake_date":"2026-09-10","source_epoch":"health-sync-ios-v1","capture_completeness":"complete","sync_generation":"period-42",
				"covered_interval_start":"2026-09-09T10:00:00Z","covered_interval_end":"2026-09-10T10:00:00Z"
			}],
			"completed_sleep_episodes": [{
				"wake_date":"2026-09-10","start":"2026-09-09T22:00:00Z","end":"2026-09-10T06:00:00Z","source":"Apple Watch",
				"source_epoch":"health-sync-ios-v1","sync_generation":"other-generation"
			}]
		}
	}`))
	if err == nil || !strings.Contains(err.Error(), "does not match period coverage generation") {
		t.Fatalf("error = %v", err)
	}
}
