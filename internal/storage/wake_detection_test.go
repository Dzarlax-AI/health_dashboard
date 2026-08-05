package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func testWakeLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestSelectWakeCandidateUsesSegmentEndAndLatestReturnToSleep(t *testing.T) {
	loc := testWakeLocation(t)
	received := time.Date(2026, 8, 5, 8, 10, 0, 0, loc)
	segments := []wakeInputSegment{
		{
			Metric:     "sleep_core",
			Start:      time.Date(2026, 8, 4, 23, 0, 0, 0, loc),
			Hours:      6,
			Source:     "Apple Watch Ultra",
			ReceivedAt: received.Add(-2 * time.Hour),
		},
		{
			Metric:     "sleep_core",
			Start:      time.Date(2026, 8, 5, 6, 0, 0, 0, loc),
			Hours:      1.5,
			Source:     "Apple Watch Ultra",
			ReceivedAt: received,
		},
	}
	got, ok, err := selectWakeCandidate(segments, "2026-08-05", loc)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("no wake candidate")
	}
	want := time.Date(2026, 8, 5, 7, 30, 0, 0, loc)
	if !got.Wake.Equal(want) {
		t.Fatalf("wake=%v, want latest segment end %v", got.Wake, want)
	}
	if !got.LatestIngest.Equal(received) {
		t.Fatalf("latest ingest=%v, want %v", got.LatestIngest, received)
	}
	if got.Signal != "detailed_stage_end" {
		t.Fatalf("signal=%q, want detailed_stage_end", got.Signal)
	}
}

func TestSelectWakeCandidatePrefersWatchAndExcludesEveningSleep(t *testing.T) {
	loc := testWakeLocation(t)
	segments := []wakeInputSegment{
		{Metric: "sleep_total", Start: time.Date(2026, 8, 5, 0, 0, 0, 0, loc), Hours: 7.5, Source: "RingConn"},
		{Metric: "sleep_total", Start: time.Date(2026, 8, 5, 1, 0, 0, 0, loc), Hours: 6.8, Source: "Apple Watch"},
		{Metric: "sleep_total", Start: time.Date(2026, 8, 5, 22, 0, 0, 0, loc), Hours: 1, Source: "Apple Watch"},
	}
	got, ok, err := selectWakeCandidate(segments, "2026-08-05", loc)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("no wake candidate")
	}
	want := time.Date(2026, 8, 5, 7, 48, 0, 0, loc)
	if got.Source != "Apple Watch" || !got.Wake.Equal(want) {
		t.Fatalf("candidate=%+v, want Apple Watch wake %v", got, want)
	}
	if got.Signal != "raw_sleep_total_end" {
		t.Fatalf("signal=%q, want raw_sleep_total_end", got.Signal)
	}
}

func TestSelectWakeCandidateUsesMidnightSummaryAsLastFallback(t *testing.T) {
	loc := testWakeLocation(t)
	segments := []wakeInputSegment{
		{Metric: "sleep_total", Start: time.Date(2026, 8, 5, 0, 0, 0, 0, loc), Hours: 6.5, Source: "RingConn"},
		{Metric: "sleep_awake", Start: time.Date(2026, 8, 5, 0, 0, 0, 0, loc), Hours: 0.5, Source: "RingConn"},
	}
	got, ok, err := selectWakeCandidate(segments, "2026-08-05", loc)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("no wake candidate")
	}
	want := time.Date(2026, 8, 5, 7, 0, 0, 0, loc)
	if !got.Wake.Equal(want) || got.Signal != "midnight_summary" {
		t.Fatalf("candidate=%+v, want midnight summary at %v", got, want)
	}
}

func TestEvaluateMorningWakeConfidencePolicy(t *testing.T) {
	loc := testWakeLocation(t)
	wake := time.Date(2026, 8, 5, 8, 0, 0, 0, loc)
	base := wakeCandidate{
		Wake:         wake,
		LatestIngest: wake.Add(5 * time.Minute),
		Source:       "Apple Watch",
		InputsHash:   "hash",
		Signal:       "detailed_stage_end",
	}
	tests := []struct {
		name       string
		now        time.Time
		steps      float64
		typicalMin int
		typicalOK  bool
		ready      bool
		confidence string
		reason     string
	}{
		{"recent segment", wake.Add(25 * time.Minute), 200, 480, true, false, WakeConfidenceLow, "recent_segment"},
		{"activity confirms", wake.Add(35 * time.Minute), 100, 480, true, true, WakeConfidenceHigh, "post_wake_activity"},
		{"quiet but too soon", wake.Add(45 * time.Minute), 0, 480, true, false, WakeConfidenceLow, "awaiting_confirmation"},
		{"quiet timeout", wake.Add(60 * time.Minute), 0, 480, true, true, WakeConfidenceMedium, "quiet_timeout"},
		{"no baseline fallback", wake.Add(60 * time.Minute), 0, 0, false, true, WakeConfidenceMedium, "quiet_timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := evaluateMorningWake(base, tc.now, tc.steps, tc.typicalMin, tc.typicalOK, loc)
			if status.Ready != tc.ready || status.Confidence != tc.confidence || status.Reason != tc.reason {
				t.Fatalf("status=%+v, want ready=%v confidence=%s reason=%s", status, tc.ready, tc.confidence, tc.reason)
			}
		})
	}
}

func TestEvaluateMorningWakeWaitsWhileSleepIngestIsFresh(t *testing.T) {
	loc := testWakeLocation(t)
	wake := time.Date(2026, 8, 5, 8, 0, 0, 0, loc)
	now := wake.Add(40 * time.Minute)
	candidate := wakeCandidate{
		Wake:         wake,
		LatestIngest: now.Add(-10 * time.Minute),
		Source:       "Apple Watch",
		InputsHash:   "hash",
		Signal:       "detailed_stage_end",
	}
	status := evaluateMorningWake(candidate, now, 200, 8*60, true, loc)
	if status.Ready || status.Reason != "still_writing" {
		t.Fatalf("status=%+v", status)
	}
}

func TestEvaluateMorningWakeWaitsLongerForEarlyCandidate(t *testing.T) {
	loc := testWakeLocation(t)
	wake := time.Date(2026, 8, 5, 5, 0, 0, 0, loc)
	candidate := wakeCandidate{
		Wake:         wake,
		LatestIngest: wake.Add(5 * time.Minute),
		Source:       "Apple Watch",
		InputsHash:   "hash",
		Signal:       "detailed_stage_end",
	}
	before := evaluateMorningWake(candidate, wake.Add(89*time.Minute), 0, 8*60, true, loc)
	if before.Ready || before.Reason != "early_candidate" {
		t.Fatalf("before early timeout=%+v", before)
	}
	after := evaluateMorningWake(candidate, wake.Add(90*time.Minute), 0, 8*60, true, loc)
	if !after.Ready || after.Confidence != WakeConfidenceMedium || after.Reason != "early_candidate_timeout" {
		t.Fatalf("after early timeout=%+v", after)
	}
}

func TestBackfillWakeTimesIsIdempotentAndLeavesRowsProvisional(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	recordID := insertTestRawRecord(t, db, "wake-backfill")
	for _, point := range []struct {
		metric string
		date   string
		hours  float64
	}{
		{"sleep_core", "2026-08-05 00:30:00 +0200", 4},
		{"sleep_rem", "2026-08-05 04:30:00 +0200", 2},
		{"sleep_awake", "2026-08-05 06:30:00 +0200", 0.5},
	} {
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO metric_points
				(health_record_id, metric_name, units, date, qty, source, quality)
			VALUES ($1,$2,'hr',$3,$4,'Apple Watch','ok')
		`, recordID, point.metric, point.date, point.hours); err != nil {
			t.Fatal(err)
		}
	}
	loc := testWakeLocation(t)
	dry, err := db.BackfillWakeTimes("2026-08-05", "2026-08-05", loc, true)
	if err != nil || dry.Detected != 1 || dry.Written != 0 {
		t.Fatalf("dry run=%+v err=%v", dry, err)
	}
	for i := 0; i < 2; i++ {
		result, err := db.BackfillWakeTimes("2026-08-05", "2026-08-05", loc, false)
		if err != nil || result.Written != 1 {
			t.Fatalf("apply %d result=%+v err=%v", i, result, err)
		}
	}
	metric, err := db.GetDerivedMetric(DerivedMetricWakeTime, "2026-08-05")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 5, 7, 0, 0, 0, loc)
	if metric.ValueTimestamp == nil || !metric.ValueTimestamp.Equal(want) {
		t.Fatalf("wake=%v, want %v", metric.ValueTimestamp, want)
	}
	if metric.State != DerivedMetricStateProvisional || metric.FinalizedAt != nil {
		t.Fatalf("historical metric state=%q finalized=%v", metric.State, metric.FinalizedAt)
	}
	var count int
	if err := db.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM derived_metrics
		 WHERE metric_name=$1 AND metric_date='2026-08-05'
	`, DerivedMetricWakeTime).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d err=%v, want one canonical row", count, err)
	}
}

func TestRecordWakeCheckinEvidenceAnnotatesCanonicalMetric(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	metric := validWakeMetric()
	if err := db.SaveDerivedMetric(metric); err != nil {
		t.Fatal(err)
	}
	answeredAt := time.Date(2026, 8, 5, 8, 40, 0, 0, time.UTC)
	if err := db.RecordWakeCheckinEvidence(metric.MetricDate, answeredAt); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDerivedMetric(metric.MetricName, metric.MetricDate)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["subjective_checkin_answered_at"] != answeredAt.Format(time.RFC3339) {
		t.Fatalf("metadata=%v", metadata)
	}
}
