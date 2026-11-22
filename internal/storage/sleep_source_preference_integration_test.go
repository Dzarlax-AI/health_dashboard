package storage

import (
	"errors"
	"slices"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestPreferredSleepSourceSnapshots(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	const day = "2026-09-10"
	const stamp = day + " 00:00:00 +0000"
	id := insertTestRawRecord(t, db, "preferred-sleep-test")
	begin := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSettings(map[string]string{"timezone": "UTC", sleepComparisonSetting: "true"}); err != nil {
		t.Fatal(err)
	}
	for _, device := range []struct {
		source string
		hours  float64
	}{{"Apple Watch A", 8}, {"Apple Watch B", 7}, {"RingConn", 6.5}} {
		if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality)
			VALUES($1,'night_sleep_total','hr',$2,$3,$4,'ok')`, id, stamp, device.hours, device.source); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveNightSleepCoverageCommitment(ctx, NightSleepCoverageCommitment{WakeDate: day, MetricDate: stamp, Source: device.source,
			SourceEpoch: device.source, CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: "fixture",
			CoveredIntervalStart: begin, CoveredIntervalEnd: begin.Add(24 * time.Hour), ObservedAt: begin.Add(24 * time.Hour), InputHash: "input-" + device.source}); err != nil {
			t.Fatal(err)
		}
	}
	save := func(value string) {
		t.Helper()
		if err := db.SaveUserSettings(ctx, map[string]string{PreferredSleepSourceSetting: value}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(source, reason, preferred string) {
		t.Helper()
		if err := db.ReconcileCompletedNightSleep(ctx, day, begin.Add(48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		var gotSource, gotReason, gotPref string
		if err := db.pool.QueryRow(ctx, `SELECT source, diagnostics->>'source_selection_reason', diagnostics->>'preferred_sleep_source' FROM completed_night_sleep WHERE wake_date=$1`, day).Scan(&gotSource, &gotReason, &gotPref); err != nil {
			t.Fatal(err)
		}
		if gotSource != source || gotReason != reason || gotPref != preferred {
			t.Fatalf("selection = %q/%q/%q, want %q/%q/%q", gotSource, gotReason, gotPref, source, reason, preferred)
		}
	}
	pref, choices, err := db.SleepSourcePreference(ctx)
	if err != nil || pref != "" || !slices.Equal(choices, []string{"Apple Watch A", "Apple Watch B", "RingConn"}) {
		t.Fatalf("choices %v (%q) %v", choices, pref, err)
	}
	if err := db.SaveUserSettings(ctx, map[string]string{PreferredSleepSourceSetting: "other-tenant", "timezone": "UTC+7"}); !errors.Is(err, ErrUnknownSleepSource) || db.GetSetting("timezone", "") != "UTC" {
		t.Fatal("invalid preference was not rejected atomically", err)
	}
	check("Apple Watch A", "automatic", "")
	save("Apple Watch B")
	check("Apple Watch B", "preferred", "Apple Watch B")
	stored := db.GetSleepDurationComparison(ctx, day)
	save("RingConn")
	if got := db.GetSleepDurationComparison(ctx, day); got.FactVersion != stored.FactVersion {
		t.Fatal("setting changed a captured calculation before reconciliation")
	}
	check("RingConn", "preferred", "RingConn")
	if _, err = db.pool.Exec(ctx, `UPDATE metric_points SET qty=1 WHERE source='RingConn'`); err != nil {
		t.Fatal(err)
	}
	check("Apple Watch A", "preferred_unavailable", "RingConn")
	save("")
	check("Apple Watch A", "automatic", "")
}
