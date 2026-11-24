package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestComputeMorningWakeStatusSerializesLateCorrectionAndRollsBack(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	loc := testWakeLocation(t)
	date := "2026-08-05"
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, loc)
	id := insertTestRawRecord(t, db, "wake-late-correction")
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points (health_record_id,metric_name,units,date,qty,source,quality,received_at) VALUES ($1,'sleep_core','hr','2026-08-05 06:00:00 +0200',1.5,'Apple Watch','ok',$2)`, id, now); err != nil {
		t.Fatal(err)
	}
	initial, err := db.ComputeMorningWakeStatus(date, loc, now)
	if err != nil || initial.Ready {
		t.Fatalf("initial wake=%+v err=%v", initial, err)
	}
	if err := db.EnsureReportHistoryTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	key := "wake-test:" + uuid.NewString()
	report := &ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: date, Timezone: loc.String(), Language: "en", IsPreview: false, PlainBody: "sent report with original wake explanation", RichBody: "sent report with original wake explanation", DecisionID: "wake-before-correction", Context: json.RawMessage(`{"wake_reason":"still_writing"}`), AIMetadata: json.RawMessage(`{}`)}
	token, reserved, err := db.ReserveArchivedReport(ctx, report)
	if err != nil || !reserved {
		t.Fatalf("archive report reserved=%v err=%v", reserved, err)
	}
	attempt, err := db.ReserveReportAttempt(ctx, report.ID, "html", report.PlainBody, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteReportAttempt(ctx, attempt, "sent", "", nil); err != nil {
		t.Fatal(err)
	}

	// Hold the writer's advisory lock so the wake calculation waits while a late sync
	// commits. The wake transaction must then see the corrected inputs.
	lockTx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),hashtext($1))`, "wake_time:"+date); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 1)
	go func() {
		got, err := db.ComputeMorningWakeStatus(date, loc, now.Add(time.Hour))
		if err == nil && (got.InputsHash == initial.InputsHash || got.InputSource != "Apple Watch") {
			err = fmt.Errorf("late-correction status did not use changed input: %+v", got)
		}
		results <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err = lockTx.QueryRow(ctx, `select exists(select from pg_locks waiter
            where not waiter.granted and waiter.locktype='advisory'
              and pg_backend_pid()=any(pg_blocking_pids(waiter.pid)))`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wake writer did not reach the held advisory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := lockTx.Exec(ctx, `update metric_points set qty=2.0,received_at=$1 where health_record_id=$2`, now.Add(time.Hour), id); err != nil {
		t.Fatal(err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	corrected, err := db.GetDerivedMetric(DerivedMetricWakeTime, date)
	if err != nil {
		t.Fatal(err)
	}
	correctedHash, correctedAt := corrected.InputsHash, corrected.CalculatedAt
	var metadataBefore json.RawMessage
	if err := db.pool.QueryRow(ctx, `SELECT metadata FROM derived_metrics WHERE metric_name='wake_time' AND metric_date=$1`, date).Scan(&metadataBefore); err != nil {
		t.Fatal(err)
	}

	// Force the final derived_metrics write to fail, after candidate and
	// confirmation SQL have run, and prove the transaction preserves its row.
	_, err = db.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION wake_test_reject_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced wake test failure'; END $$; CREATE TRIGGER wake_test_reject_write BEFORE INSERT OR UPDATE ON derived_metrics FOR EACH ROW WHEN (NEW.metric_name='wake_time') EXECUTE FUNCTION wake_test_reject_write()`)
	if err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, `DROP TRIGGER IF EXISTS wake_test_reject_write ON derived_metrics; DROP FUNCTION IF EXISTS wake_test_reject_write()`)
	if _, err := db.ComputeMorningWakeStatus(date, loc, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expected forced derived metric SQL failure")
	}
	var metadataAfter json.RawMessage
	if err := db.pool.QueryRow(ctx, `SELECT metadata FROM derived_metrics WHERE metric_name='wake_time' AND metric_date=$1`, date).Scan(&metadataAfter); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := db.GetDerivedMetric(DerivedMetricWakeTime, date)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.InputsHash != correctedHash || !rolledBack.CalculatedAt.Equal(correctedAt) || string(metadataAfter) != string(metadataBefore) {
		t.Fatalf("failed tx changed persisted wake/hash-clock: before=%s after=%s metric=%+v", metadataBefore, metadataAfter, rolledBack)
	}
	var archivedBody, archivedDecision string
	var contextMatches bool
	if err := db.pool.QueryRow(ctx, `SELECT h.plain_body,h.decision_id,h.context=$2::jsonb FROM report_history h WHERE h.id=$1`, report.ID, report.Context).Scan(&archivedBody, &archivedDecision, &contextMatches); err != nil {
		t.Fatal(err)
	}
	if archivedBody != report.PlainBody || archivedDecision != "wake-before-correction" || !contextMatches {
		t.Fatalf("wake correction rewrote sent report: body=%q decision=%q contextMatches=%t", archivedBody, archivedDecision, contextMatches)
	}
}

func evaluateMorningWake(candidate wakeCandidate, now time.Time, steps float64, typicalMin int, typicalOK bool, loc *time.Location) MorningWakeStatus {
	status := MorningWakeStatus{
		Confidence:            WakeConfidenceLow,
		Reason:                "recent_segment",
		CandidateWake:         candidate.Wake,
		LatestIngest:          candidate.LatestIngest,
		PostWakeSteps:         steps,
		InputSource:           candidate.Source,
		SourceSelectionReason: candidate.SourceSelectionReason,
		PreferredSource:       candidate.PreferredSource,
		InputsHash:            candidate.InputsHash,
		TypicalWakeMin:        typicalMin,
		TypicalWakeOK:         typicalOK,
		Signal:                candidate.Signal,
	}
	candidateAge := now.Sub(candidate.Wake)
	if candidateAge < 0 || candidateAge < wakeActivityAge {
		return status
	}
	ingestQuietSince := candidate.IngestQuietSince
	if ingestQuietSince.IsZero() {
		ingestQuietSince = candidate.LatestIngest
	}
	if !ingestQuietSince.IsZero() && now.Sub(ingestQuietSince) < wakeIngestQuiet {
		status.Reason = "still_writing"
		return status
	}
	if steps >= wakeActivitySteps {
		status.Ready = true
		if candidate.Signal == "detailed_stage_end" {
			status.Confidence = WakeConfidenceHigh
			status.Reason = "post_wake_activity"
		} else {
			status.Confidence = WakeConfidenceMedium
			status.Reason = "post_wake_activity_fallback"
		}
		return status
	}
	if candidate.Signal != "detailed_stage_end" && candidateAge < wakeFallbackAge {
		status.Reason = "fallback_candidate"
		return status
	}
	if typicalOK {
		localWake := candidate.Wake.In(loc)
		candidateMin := localWake.Hour()*60 + localWake.Minute()
		if typicalMin-candidateMin > int(wakeEarlyTolerance/time.Minute) {
			typicalGuard := time.Date(
				localWake.Year(), localWake.Month(), localWake.Day(),
				typicalMin/60, typicalMin%60, 0, 0, loc,
			).Add(-wakeTypicalGuardOffset)
			earlyDeadline := candidate.Wake.Add(wakeEarlyAge)
			if typicalGuard.Before(earlyDeadline) {
				earlyDeadline = typicalGuard
			}
			if now.Before(earlyDeadline) {
				status.Reason = "early_candidate"
				return status
			}
			status.Ready = true
			status.Confidence = WakeConfidenceMedium
			status.Reason = "early_candidate_timeout"
			return status
		}
	}
	if candidateAge < wakePassiveAge {
		status.Reason = "awaiting_confirmation"
		return status
	}
	status.Ready = true
	status.Confidence = WakeConfidenceMedium
	status.Reason = "quiet_timeout"
	return status
}

// Go candidate selection remains a test oracle only; serving paths use the shared dbt SQL.
type wakeEligibleSegment struct {
	wakeInputSegment
	End            time.Time
	HashComponents []wakeInputSegment
}

func selectWakeCandidate(segments []wakeInputSegment, localDate string, loc *time.Location) (wakeCandidate, bool, error) {
	return selectWakeCandidateWithPreference(segments, localDate, loc, "")
}

func selectWakeCandidateWithPreference(segments []wakeInputSegment, localDate string, loc *time.Location, preferred string) (wakeCandidate, bool, error) {
	if loc == nil {
		loc = time.UTC
	}
	isWakeWindow := func(end time.Time) bool {
		localEnd := end.In(loc)
		if localEnd.Format("2006-01-02") != localDate {
			return false
		}
		minute := localEnd.Hour()*60 + localEnd.Minute()
		return minute >= 3*60 && minute <= 15*60
	}
	isMidnight := func(start time.Time) bool {
		return start.In(loc).Format("15:04:05") == "00:00:00"
	}

	var detailed, rawTotal []wakeEligibleSegment
	detailedTotals := map[string]float64{}
	sleepTotalTotals := map[string]float64{}
	for _, segment := range segments {
		if segment.Hours <= 0 || segment.Source == "" {
			continue
		}
		end := segment.Start.Add(time.Duration(segment.Hours * float64(time.Hour)))
		if isMidnight(segment.Start) || !isWakeWindow(end) {
			continue
		}
		if segment.Metric == "sleep_total" {
			sleepTotalTotals[segment.Source] += segment.Hours
			rawTotal = append(rawTotal, wakeEligibleSegment{wakeInputSegment: segment, End: end})
		} else {
			detailed = append(detailed, wakeEligibleSegment{wakeInputSegment: segment, End: end})
		}
		if segment.Metric != "sleep_total" && segment.Metric != "sleep_awake" {
			detailedTotals[segment.Source] += segment.Hours
		}
	}

	source, selectionReason := chooseSleepSource(detailedTotals, true, preferred)
	selected := detailed
	signal := "detailed_stage_end"
	if source == "" {
		source, selectionReason = chooseSleepSource(sleepTotalTotals, true, preferred)
		selected = rawTotal
		signal = "raw_sleep_total_end"
	}
	if source != "" {
		selected = slicesForSource(selected, source)
	}
	if source == "" || len(selected) == 0 {
		var ok bool
		source, selected, selectionReason, ok = selectMidnightSummary(segments, localDate, loc, isWakeWindow, preferred)
		if !ok {
			return wakeCandidate{}, false, nil
		}
		signal = "midnight_summary"
	}

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Start.Equal(selected[j].Start) {
			return selected[i].Metric < selected[j].Metric
		}
		return selected[i].Start.Before(selected[j].Start)
	})
	var wake, latestIngest time.Time
	for _, segment := range selected {
		if segment.End.After(wake) {
			wake = segment.End
		}
		if segment.ReceivedAt.After(latestIngest) {
			latestIngest = segment.ReceivedAt
		}
	}
	if wake.IsZero() {
		return wakeCandidate{}, false, nil
	}
	h := sha256.New()
	fmt.Fprintf(h, "signal=%s\n", signal)
	for _, segment := range selected {
		components := segment.HashComponents
		if len(components) == 0 {
			components = []wakeInputSegment{segment.wakeInputSegment}
		}
		for _, component := range components {
			fmt.Fprintf(h, "%s|%s|%.6f|%s|%s\n", component.Metric, component.Start.UTC().Format(time.RFC3339Nano), component.Hours, component.Source, component.Quality)
		}
	}
	// The wake time still comes from the existing candidate selection above.
	// Quietness must also cover earlier detailed intervals, and competing
	// sources that could make the observed episode ambiguous. Receipt times are
	// deliberately excluded so identical re-uploads do not restart the clock.
	wakeDay := wake.In(loc)
	windowStart := time.Date(wakeDay.Year(), wakeDay.Month(), wakeDay.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -1)
	var episode []wakeInputSegment
	for _, segment := range segments {
		if segment.Metric == "sleep_total" || segment.Source == "" || segment.Hours <= 0 || segment.Quality != "ok" {
			continue
		}
		end := segment.Start.Add(time.Duration(segment.Hours * float64(time.Hour)))
		if segment.Start.Before(wake) && end.After(windowStart) {
			episode = append(episode, segment)
		}
	}
	sort.Slice(episode, func(i, j int) bool {
		a, b := episode[i], episode[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		if a.Hours != b.Hours {
			return a.Hours < b.Hours
		}
		return a.Quality < b.Quality
	})
	fmt.Fprintln(h, "episode-inputs-v1")
	for _, segment := range episode {
		fmt.Fprintf(h, "%s|%s|%.6f|%s|%s\n", segment.Metric, segment.Start.UTC().Format(time.RFC3339Nano), segment.Hours, segment.Source, segment.Quality)
	}
	return wakeCandidate{
		Wake:                  wake,
		LatestIngest:          latestIngest,
		Source:                source,
		SourceSelectionReason: selectionReason,
		PreferredSource:       preferred,
		InputsHash:            hex.EncodeToString(h.Sum(nil)),
		Signal:                signal,
	}, true, nil
}

func slicesForSource(segments []wakeEligibleSegment, source string) []wakeEligibleSegment {
	out := make([]wakeEligibleSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Source == source {
			out = append(out, segment)
		}
	}
	return out
}

func selectMidnightSummary(
	segments []wakeInputSegment,
	localDate string,
	loc *time.Location,
	isWakeWindow func(time.Time) bool, preferred string,
) (string, []wakeEligibleSegment, string, bool) {
	type summaryGroup struct {
		start        time.Time
		source       string
		asleepHours  float64
		awakeHours   float64
		latestIngest time.Time
		components   []wakeInputSegment
	}
	groups := map[string]*summaryGroup{}
	sourceTotals := map[string]float64{}
	for _, segment := range segments {
		if segment.Hours <= 0 || segment.Source == "" ||
			segment.Start.In(loc).Format("2006-01-02 15:04:05") != localDate+" 00:00:00" {
			continue
		}
		if segment.Metric != "sleep_total" && segment.Metric != "sleep_awake" {
			continue
		}
		key := segment.Source + "|" + segment.Start.UTC().Format(time.RFC3339Nano)
		group := groups[key]
		if group == nil {
			group = &summaryGroup{start: segment.Start, source: segment.Source}
			groups[key] = group
		}
		if segment.Metric == "sleep_total" {
			group.asleepHours += segment.Hours
			sourceTotals[segment.Source] += segment.Hours
		} else {
			group.awakeHours += segment.Hours
		}
		group.components = append(group.components, segment)
		if segment.ReceivedAt.After(group.latestIngest) {
			group.latestIngest = segment.ReceivedAt
		}
	}
	if preferred != "" {
		// Rank only devices with a usable midnight summary, including when
		// the preferred device is absent or ends outside the wake window.
		eligibleTotals := make(map[string]float64)
		for _, group := range groups {
			end := group.start.Add(time.Duration((group.asleepHours + group.awakeHours) * float64(time.Hour)))
			if group.asleepHours > 0 && isWakeWindow(end) {
				eligibleTotals[group.source] = sourceTotals[group.source]
			}
		}
		sourceTotals = eligibleTotals
	}
	source, selectionReason := chooseSleepSource(sourceTotals, true, preferred)
	if source == "" {
		return "", nil, "", false
	}
	var best *summaryGroup
	var wake time.Time
	for _, group := range groups {
		if group.source != source || group.asleepHours <= 0 {
			continue
		}
		end := group.start.Add(time.Duration((group.asleepHours + group.awakeHours) * float64(time.Hour)))
		if isWakeWindow(end) && end.After(wake) {
			best = group
			wake = end
		}
	}
	if best == nil {
		return "", nil, "", false
	}
	sort.Slice(best.components, func(i, j int) bool {
		if best.components[i].Start.Equal(best.components[j].Start) {
			return best.components[i].Metric < best.components[j].Metric
		}
		return best.components[i].Start.Before(best.components[j].Start)
	})
	return source, []wakeEligibleSegment{{
		wakeInputSegment: wakeInputSegment{
			Metric:     "sleep_summary",
			Start:      best.start,
			Hours:      best.asleepHours + best.awakeHours,
			Source:     best.source,
			ReceivedAt: best.latestIngest,
		},
		End:            wake,
		HashComponents: best.components,
	}}, selectionReason, true
}
