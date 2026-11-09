package storage

import (
	"context"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestObservedSleepEpisodeRequiresContinuousUnambiguousDetailedSegments(t *testing.T) {
	wake := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	candidate := wakeCandidate{Source: "watch", Wake: wake}
	segment := func(metric, source string, start time.Time, hours float64) wakeInputSegment {
		return wakeInputSegment{Metric: metric, Source: source, Start: start, Hours: hours, Quality: "ok"}
	}
	base := []wakeInputSegment{segment("sleep_core", "watch", wake.Add(-3*time.Hour), 2), segment("sleep_awake", "watch", wake.Add(-time.Hour), 0.25), segment("sleep_rem", "watch", wake.Add(-45*time.Minute), 0.75)}
	if got, _ := observedSleepEpisode(nil, candidate, time.UTC); got.State != health.MorningSleepMissing {
		t.Fatalf("missing episode state: %#v", got)
	}
	got, err := observedSleepEpisode(base, candidate, time.UTC)
	if err != nil || got.State != "observed_stable" || got.Hours != 2.75 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if got.Episode == nil || got.Episode.AsleepMinutes != 165 || got.Episode.CoreMinutes == nil || *got.Episode.CoreMinutes != 120 || got.Episode.REMMinutes == nil || *got.Episode.REMMinutes != 45 || got.Episode.AwakeMinutes == nil || *got.Episode.AwakeMinutes != 15 || got.Episode.AwakeBouts == nil || *got.Episode.AwakeBouts != 1 || got.Episode.DeepMinutes != nil {
		t.Fatalf("recorded stage facts were lost or missing stages became zero: %+v", got.Episode)
	}
	gap := append([]wakeInputSegment(nil), base...)
	gap[1].Start = gap[1].Start.Add(time.Minute)
	if got, _ := observedSleepEpisode(gap, candidate, time.UTC); got.State != health.MorningSleepGappedOrConflicting {
		t.Fatalf("gap accepted: %#v", got)
	}
	overlap := append([]wakeInputSegment(nil), base...)
	overlap = append(overlap, segment("sleep_core", "ring", wake.Add(-2*time.Hour), 0.5))
	if got, _ := observedSleepEpisode(overlap, candidate, time.UTC); got.State != health.MorningSleepGappedOrConflicting || got.Reason != "conflicting_source" {
		t.Fatalf("conflict accepted: %#v", got)
	}
}

func TestMorningSleepReadinessRequiresCurrentQuietWakeInputs(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	missing, err := db.MorningSleepReadiness(ctx, "2026-11-09", time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	if err != nil || missing.State != health.MorningSleepMissing {
		t.Fatalf("missing inputs: %#v %v", missing, err)
	}
	id := insertTestRawRecord(t, db, "morning-observed-episode")
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES ($1,'sleep_core','hr','2026-11-08 23:00:00 +0000',3,'Apple Watch','ok'), ($1,'sleep_rem','hr','2026-11-09 02:00:00 +0000',2,'Apple Watch','ok'), ($1,'sleep_core','hr','2026-11-09 04:00:00 +0000',3,'Apple Watch','ok')`, id); err != nil {
		t.Fatal(err)
	}
	collecting, err := db.MorningSleepReadiness(ctx, "2026-11-09", time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	if err != nil || collecting.State != health.MorningSleepCollecting {
		t.Fatalf("missing saved wake: %#v %v", collecting, err)
	}
	candidate, ok, err := db.loadWakeCandidate("2026-11-09", time.UTC)
	if err != nil || !ok {
		t.Fatalf("candidate %v %v", ok, err)
	}
	changed := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := MorningWakeStatus{Ready: true, Confidence: WakeConfidenceHigh, CandidateWake: candidate.Wake, InputSource: candidate.Source, InputsHash: candidate.InputsHash, Signal: candidate.Signal}
	if err := saveWakeStatusTx(ctx, tx, "2026-11-09", status, changed, changed); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	early, err := db.MorningSleepReadiness(ctx, "2026-11-09", changed.Add(19*time.Minute))
	if err != nil || early.State != health.MorningSleepCollecting {
		t.Fatalf("quiet window bypass: %#v %v", early, err)
	}
	stable, err := db.MorningSleepReadiness(ctx, "2026-11-09", changed.Add(21*time.Minute))
	if err != nil || stable.State != "observed_stable" || stable.Hours != 8 {
		t.Fatalf("stable: %#v %v", stable, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE derived_metrics SET metadata=metadata || '{"confidence":"medium","reason":"quiet_timeout"}'::jsonb WHERE metric_name=$1 AND metric_date='2026-11-09'`, DerivedMetricWakeTime); err != nil {
		t.Fatal(err)
	}
	passive, err := db.MorningSleepReadiness(ctx, "2026-11-09", changed.Add(21*time.Minute))
	if err != nil || passive.State != health.MorningSleepObservedStable {
		t.Fatalf("existing passive wake confirmation ignored: %+v %v", passive, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE derived_metrics SET metadata=metadata || '{"reason":"awaiting_confirmation"}'::jsonb WHERE metric_name=$1 AND metric_date='2026-11-09'`, DerivedMetricWakeTime); err != nil {
		t.Fatal(err)
	}
	unconfirmed, err := db.MorningSleepReadiness(ctx, "2026-11-09", changed.Add(21*time.Minute))
	if err != nil || unconfirmed.State != health.MorningSleepCollecting {
		t.Fatalf("unconfirmed wake admitted: %+v %v", unconfirmed, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE metric_points SET qty=2 WHERE health_record_id=$1 AND date='2026-11-08 23:00:00 +0000'`, id); err != nil {
		t.Fatal(err)
	}
	stale, err := db.MorningSleepReadiness(ctx, "2026-11-09", changed.Add(22*time.Minute))
	if err != nil || stale.State != health.MorningSleepCollecting {
		t.Fatalf("stale wake accepted: %#v %v", stale, err)
	}
}
