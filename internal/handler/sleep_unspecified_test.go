package handler

import "testing"

func TestSleepUnspecified_IsScoreRelevant(t *testing.T) {
	if !scoreRelevantMetrics["sleep_unspecified"] {
		t.Fatalf("sleep_unspecified must trigger a readiness recompute — without it, RingConn-only nights would never refresh the score")
	}
}
