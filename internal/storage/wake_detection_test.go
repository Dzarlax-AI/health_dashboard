package storage

import (
	"encoding/json"
	"fmt"
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

func TestSelectWakeCandidateHashIgnoresReceiptRefresh(t *testing.T) {
	loc := testWakeLocation(t)
	segment := wakeInputSegment{
		Metric: "sleep_core", Start: time.Date(2026, 8, 5, 6, 0, 0, 0, loc),
		Hours: 1.5, Source: "Apple Watch",
		ReceivedAt: time.Date(2026, 8, 5, 8, 0, 0, 0, loc),
	}
	first, ok, err := selectWakeCandidate([]wakeInputSegment{segment}, "2026-08-05", loc)
	if err != nil || !ok {
		t.Fatalf("first candidate ok=%v err=%v", ok, err)
	}
	segment.ReceivedAt = segment.ReceivedAt.Add(time.Hour)
	duplicate, ok, err := selectWakeCandidate([]wakeInputSegment{segment}, "2026-08-05", loc)
	if err != nil || !ok {
		t.Fatalf("duplicate candidate ok=%v err=%v", ok, err)
	}
	if first.InputsHash != duplicate.InputsHash {
		t.Fatalf("receipt refresh changed sleep hash: %q != %q", first.InputsHash, duplicate.InputsHash)
	}
	segment.Hours += 0.25
	changed, ok, err := selectWakeCandidate([]wakeInputSegment{segment}, "2026-08-05", loc)
	if err != nil || !ok {
		t.Fatalf("changed candidate ok=%v err=%v", ok, err)
	}
	if changed.InputsHash == first.InputsHash {
		t.Fatal("sleep input change did not change hash")
	}
}

func TestWakeInputClockPreservesDuplicatesAndRestartsOnChange(t *testing.T) {
	first := time.Date(2026, 8, 5, 8, 30, 0, 0, time.UTC)
	duplicateAt := first.Add(10 * time.Minute)
	if got := wakeInputChangedAt("same", "same", first, duplicateAt); !got.Equal(first) {
		t.Fatalf("duplicate reset change clock to %v, want %v", got, first)
	}
	if got := wakeInputChangedAt("new", "same", first, duplicateAt); !got.Equal(duplicateAt) {
		t.Fatalf("changed inputs clock=%v, want %v", got, duplicateAt)
	}
	if got := wakeInputChangedAt("same", "", time.Time{}, duplicateAt); !got.Equal(duplicateAt) {
		t.Fatalf("legacy marker clock=%v, want conservative start %v", got, duplicateAt)
	}
	clockRollback := first.Add(-time.Minute)
	if got := wakeInputChangedAt("changed", "same", first, clockRollback); !got.Equal(first) {
		t.Fatalf("clock rollback moved input clock backwards: %v, want %v", got, first)
	}
}

func TestEvaluateMorningWakeUsesPersistedInputClockAfterReceiptRefresh(t *testing.T) {
	loc := testWakeLocation(t)
	wake := time.Date(2026, 8, 5, 7, 0, 0, 0, loc)
	now := wake.Add(90 * time.Minute)
	candidate := wakeCandidate{
		Wake: wake, LatestIngest: now.Add(-5 * time.Minute),
		IngestQuietSince: now.Add(-25 * time.Minute),
		Source:           "Apple Watch", InputsHash: "stable", Signal: "detailed_stage_end",
	}
	status := evaluateMorningWake(candidate, now, 0, 0, false, loc)
	if !status.Ready || status.Reason != "quiet_timeout" {
		t.Fatalf("duplicate receipt refresh reset quiet window: %+v", status)
	}
	candidate.IngestQuietSince = now.Add(-10 * time.Minute)
	status = evaluateMorningWake(candidate, now, 0, 0, false, loc)
	if status.Ready || status.Reason != "still_writing" {
		t.Fatalf("new sleep change did not enforce quiet window: %+v", status)
	}
}

func TestComputeMorningWakeStatusPersistsSleepInputClockAcrossDuplicateSync(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	loc := testWakeLocation(t)
	recordID := insertTestRawRecord(t, db, "wake-duplicate-sync")
	receivedAt := time.Date(2026, 8, 5, 8, 0, 0, 0, loc)
	_, err := db.pool.Exec(ctx, `
		INSERT INTO metric_points
			(health_record_id, metric_name, units, date, qty, source, quality, received_at)
		VALUES ($1,'sleep_core','hr','2026-08-05 06:00:00 +0200',1.5,'Apple Watch','ok',$2)
	`, recordID, receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	firstNow := time.Date(2026, 8, 5, 8, 45, 0, 0, loc)
	first, err := db.ComputeMorningWakeStatus("2026-08-05", loc, firstNow)
	if err != nil || first.Ready || first.Reason != "still_writing" {
		t.Fatalf("first status=%+v err=%v", first, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE metric_points SET received_at=$1 WHERE health_record_id=$2 AND metric_name='sleep_core'`, firstNow.Add(9*time.Minute), recordID); err != nil {
		t.Fatal(err)
	}
	secondNow := firstNow.Add(10 * time.Minute)
	second, err := db.ComputeMorningWakeStatus("2026-08-05", loc, secondNow)
	if err != nil || second.Ready || second.Reason != "still_writing" {
		t.Fatalf("duplicate sync status=%+v err=%v", second, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE metric_points SET qty=2.0, received_at=$1 WHERE health_record_id=$2 AND metric_name='sleep_core'`, secondNow.Add(time.Minute), recordID); err != nil {
		t.Fatal(err)
	}
	changedNow := secondNow.Add(10 * time.Minute)
	changed, err := db.ComputeMorningWakeStatus("2026-08-05", loc, changedNow)
	if err != nil || changed.Ready || changed.Reason != "still_writing" {
		t.Fatalf("changed sleep status=%+v err=%v", changed, err)
	}
	metric, err := db.GetDerivedMetric(DerivedMetricWakeTime, "2026-08-05")
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metric.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if got := metadata["sleep_inputs_changed_at"]; got != changedNow.Format(time.RFC3339) {
		t.Fatalf("persisted changed_at=%v, want %s", got, changedNow.Format(time.RFC3339))
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM metric_points WHERE health_record_id=$1 AND metric_name='sleep_core'`, recordID); err != nil {
		t.Fatal(err)
	}
	missing, err := db.ComputeMorningWakeStatus("2026-08-05", loc, changedNow.Add(time.Minute))
	if err != nil || missing.Reason != "no_data" {
		t.Fatalf("missing-input status=%+v err=%v", missing, err)
	}
	metric, err = db.GetDerivedMetric(DerivedMetricWakeTime, "2026-08-05")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metric.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["sleep_inputs_hash"] != "" {
		t.Fatalf("missing inputs retained stale hash marker: %v", metadata)
	}
	appearedAt := changedNow.Add(time.Hour)
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO metric_points
			(health_record_id, metric_name, units, date, qty, source, quality, received_at)
		VALUES ($1,'sleep_core','hr','2026-08-05 06:00:00 +0200',2.0,'Apple Watch','ok',$2)
	`, recordID, appearedAt); err != nil {
		t.Fatal(err)
	}
	appeared, err := db.ComputeMorningWakeStatus("2026-08-05", loc, appearedAt)
	if err != nil || appeared.Ready || appeared.Reason != "still_writing" {
		t.Fatalf("reappeared inputs did not start quiet window: %+v err=%v", appeared, err)
	}
	metric, err = db.GetDerivedMetric(DerivedMetricWakeTime, "2026-08-05")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metric.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if got := metadata["sleep_inputs_changed_at"]; got != appearedAt.Format(time.RFC3339) {
		t.Fatalf("reappeared changed_at=%v, want %s", got, appearedAt.Format(time.RFC3339))
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

func TestSelectWakeCandidateMidnightHashRetainsSleepAndAwakeComponents(t *testing.T) {
	loc := testWakeLocation(t)
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, loc)
	received := time.Date(2026, 8, 5, 8, 10, 0, 0, loc)
	firstInputs := []wakeInputSegment{
		{Metric: "sleep_total", Start: start, Hours: 7, Source: "RingConn", Quality: "ok", ReceivedAt: received},
		{Metric: "sleep_awake", Start: start, Hours: 1, Source: "RingConn", Quality: "ok", ReceivedAt: received},
	}
	first, ok, err := selectWakeCandidate(firstInputs, "2026-08-05", loc)
	if err != nil || !ok {
		t.Fatalf("first candidate ok=%v err=%v", ok, err)
	}
	compositionChanged := []wakeInputSegment{
		{Metric: "sleep_total", Start: start, Hours: 7.5, Source: "RingConn", Quality: "ok", ReceivedAt: received.Add(time.Hour)},
		{Metric: "sleep_awake", Start: start, Hours: 0.5, Source: "RingConn", Quality: "ok", ReceivedAt: received.Add(time.Hour)},
	}
	second, ok, err := selectWakeCandidate(compositionChanged, "2026-08-05", loc)
	if err != nil || !ok {
		t.Fatalf("second candidate ok=%v err=%v", ok, err)
	}
	if !first.Wake.Equal(second.Wake) {
		t.Fatalf("equal-total composition changed wake: %v != %v", first.Wake, second.Wake)
	}
	if first.InputsHash == second.InputsHash {
		t.Fatal("equal-total sleep/awake composition change did not change input hash")
	}
}

func TestComputeMorningWakeStatusResetsClockForMidnightCompositionChange(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	loc := testWakeLocation(t)
	recordID := insertTestRawRecord(t, db, "wake-midnight-composition")
	receivedAt := time.Date(2026, 8, 5, 8, 0, 0, 0, loc)
	for _, input := range []struct {
		metric string
		qty    float64
	}{
		{"sleep_total", 7},
		{"sleep_awake", 1},
	} {
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO metric_points
				(health_record_id, metric_name, units, date, qty, source, quality, received_at)
			VALUES ($1,$2,'hr','2026-08-05 00:00:00 +0200',$3,'RingConn','ok',$4)
		`, recordID, input.metric, input.qty, receivedAt); err != nil {
			t.Fatal(err)
		}
	}
	firstNow := time.Date(2026, 8, 5, 8, 45, 0, 0, loc)
	first, err := db.ComputeMorningWakeStatus("2026-08-05", loc, firstNow)
	if err != nil || first.Ready || first.Reason != "still_writing" {
		t.Fatalf("first status=%+v err=%v", first, err)
	}
	if _, err := db.pool.Exec(ctx, `
		UPDATE metric_points SET qty=CASE metric_name
			WHEN 'sleep_total' THEN 7.5 ELSE 0.5 END,
		received_at=$1
		WHERE health_record_id=$2 AND metric_name IN ('sleep_total','sleep_awake')
	`, firstNow.Add(time.Minute), recordID); err != nil {
		t.Fatal(err)
	}
	changedNow := firstNow.Add(10 * time.Minute)
	changed, err := db.ComputeMorningWakeStatus("2026-08-05", loc, changedNow)
	if err != nil || changed.Ready || changed.Reason != "still_writing" {
		t.Fatalf("composition change status=%+v err=%v", changed, err)
	}
	if first.InputsHash == changed.InputsHash {
		t.Fatal("composition change preserved the old hash")
	}
	metric, err := db.GetDerivedMetric(DerivedMetricWakeTime, "2026-08-05")
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metric.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if got := metadata["sleep_inputs_changed_at"]; got != changedNow.Format(time.RFC3339) {
		t.Fatalf("persisted changed_at=%v, want %s", got, changedNow.Format(time.RFC3339))
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

func TestTypicalDerivedWakeMinutesIgnoresRowsOutsideCalendarWindow(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	loc := testWakeLocation(t)

	for day := 1; day <= 7; day++ {
		metric := validWakeMetric()
		date := time.Date(2026, 6, day, 0, 0, 0, 0, loc)
		wake := time.Date(2026, 6, day, 5, day, 0, 0, loc)
		metric.MetricDate = date.Format("2006-01-02")
		metric.ValueTimestamp = &wake
		if err := db.SaveDerivedMetric(metric); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := db.typicalDerivedWakeMinutes("2026-08-05", 14, loc); err != nil || ok {
		t.Fatalf("stale-only baseline ok=%v err=%v, want unavailable", ok, err)
	}

	for day := 22; day <= 28; day++ {
		metric := validWakeMetric()
		date := time.Date(2026, 7, day, 0, 0, 0, 0, loc)
		wake := time.Date(2026, 7, day, 7, day-22, 0, 0, loc)
		metric.MetricDate = date.Format("2006-01-02")
		metric.ValueTimestamp = &wake
		if err := db.SaveDerivedMetric(metric); err != nil {
			t.Fatal(err)
		}
	}
	minutes, ok, err := db.typicalDerivedWakeMinutes("2026-08-05", 14, loc)
	if err != nil || !ok || minutes != 7*60+3 {
		t.Fatalf("recent baseline minutes=%d ok=%v err=%v", minutes, ok, err)
	}
}

func TestWakeCandidateVariantsReturnsEmptyDayWithoutAbortingProbe(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	recordID := insertTestRawRecord(t, db, "wake-probe-empty")
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO metric_points
			(health_record_id, metric_name, units, date, qty, source, quality)
		VALUES ($1,'sleep_total','hr','2026-08-04 18:00:00 +0200',2,'Apple Watch','ok')
	`, recordID); err != nil {
		t.Fatal(err)
	}
	variants, err := db.WakeCandidateVariantsForDate("2026-08-05", testWakeLocation(t))
	if err != nil {
		t.Fatal(err)
	}
	if variants.SelectedSource != "Apple Watch" ||
		!variants.SleepTotalEnd.IsZero() ||
		!variants.DetailedSessionEnd.IsZero() ||
		!variants.SummarySessionEnd.IsZero() {
		t.Fatalf("variants=%+v, want selected source with no eligible candidates", variants)
	}
}

func TestComputeMorningWakeStatusConcurrentSourceChangeKeepsClock(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	loc := testWakeLocation(t)
	date := "2026-08-05"
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, loc)
	id := insertTestRawRecord(t, db, "wake-concurrent-source")
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points (health_record_id,metric_name,units,date,qty,source,quality,received_at) VALUES ($1,'sleep_core','hr','2026-08-05 06:00:00 +0200',1.5,'RingConn','ok',$2)`, id, now); err != nil {
		t.Fatal(err)
	}
	first, err := db.ComputeMorningWakeStatus(date, loc, now)
	if err != nil {
		t.Fatal(err)
	}
	changedAt := now.Add(time.Hour)
	if _, err := db.pool.Exec(ctx, `UPDATE metric_points SET source='Apple Watch', received_at=$1 WHERE health_record_id=$2`, changedAt, id); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for range 8 {
		go func() {
			got, err := db.ComputeMorningWakeStatus(date, loc, changedAt)
			if err == nil && (got.Ready || got.InputsHash == first.InputsHash || got.InputSource != "Apple Watch") {
				err = fmt.Errorf("source change status: %+v", got)
			}
			results <- err
		}()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated day cannot reset this day's persisted clock.
	if _, err := db.ComputeMorningWakeStatus("2026-08-06", loc, changedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE metric_points SET received_at=$1 WHERE health_record_id=$2`, changedAt.Add(30*time.Minute), id); err != nil {
		t.Fatal(err)
	}
	got, err := db.ComputeMorningWakeStatus(date, loc, changedAt.Add(30*time.Minute))
	if err != nil || !got.Ready {
		t.Fatalf("duplicate after quiet window: %+v err=%v", got, err)
	}
	metric, err := db.GetDerivedMetric(DerivedMetricWakeTime, date)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metric.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["sleep_inputs_changed_at"] != changedAt.Format(time.RFC3339) {
		t.Fatalf("clock=%v", metadata["sleep_inputs_changed_at"])
	}
}
