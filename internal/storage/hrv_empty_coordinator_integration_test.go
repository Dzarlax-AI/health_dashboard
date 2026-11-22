package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestHRVLastWithdrawalSettlesEmptyCoordinator exercises the real coordinator
// and startup maintenance callback after the final hourly observation vanishes.
func TestHRVLastWithdrawalSettlesEmptyCoordinator(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	date := "2026-10-25"
	insertHRVRepairPoints(t, db, []MetricPoint{hrvRepairPoint(date+" 08:00:00 +0000", "watch", 45)})
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	before, err := db.CaptureCacheDirty(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteCacheDirty(ctx, before); err != nil {
		t.Fatal(err)
	}
	markHRVRepairForTest(t, db, date, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE metric_points SET quality='invalid' WHERE metric_name='heart_rate_variability'`); err != nil {
			t.Fatal(err)
		}
	})
	c := testTodayCoordinator()
	unexpected := errors.New("dependent stage must not run without hourly data")
	trigger := func(dates []string, cacheReady bool) {
		c.TriggerRefresh(ctx, db, "synthetic", TodayDerivedRefresh{Dates: dates, CacheReady: cacheReady},
			func(affected []string) error { return db.RefreshChangedCache(ctx, affected) },
			func([]string) error { return unexpected }, todayTimezone,
			func() error { return unexpected })
	}
	trigger([]string{date}, false)
	if err := c.WaitForIdle(ctx, db); err != nil {
		t.Fatal(err)
	}
	state, err := db.LoadCacheMaintenance(ctx)
	if err != nil || len(state.Dirty) != 0 {
		t.Fatalf("empty correction did not settle dirty generations: %+v %v", state.Dirty, err)
	}
	var hours int
	var cached *float64
	var emptyDashboard bool
	if err := db.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM hourly_metrics),
		(SELECT hrv_avg FROM daily_scores WHERE date=$1),
		(SELECT COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(payload->'cards')='array' THEN payload->'cards' ELSE '[]'::jsonb END),0)=0 FROM dashboard_cache_snapshots WHERE singleton)`, date).Scan(&hours, &cached, &emptyDashboard); err != nil {
		t.Fatal(err)
	}
	if hours != 0 || cached != nil || !emptyDashboard {
		t.Fatalf("empty correction: hourly=%d cached=%v dashboard_empty=%v", hours, cached, emptyDashboard)
	}
	// Recreate the startup call on the now-empty store. An empty callback must
	// finish without invoking insight/energy generation or a retry loop.
	_, err = db.withCacheMaintenanceTx(ctx, func(_ pgx.Tx, state *CacheMaintenanceState) error {
		state.CompletedIdentity = CacheMaintenanceIdentity()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &DB{pool: db.pool}
	restartCoordinator := testTodayCoordinator()
	finishCalls := 0
	if err := restarted.RunCacheMaintenance(ctx, "UTC", func(dates []string) error {
		finishCalls++
		restartCoordinator.TriggerRefresh(ctx, restarted, "synthetic", TodayDerivedRefresh{Dates: dates, CacheReady: len(dates) == 0},
			func(affected []string) error { return restarted.RefreshChangedCache(ctx, affected) },
			func([]string) error { return unexpected }, todayTimezone,
			func() error { return unexpected })
		return restartCoordinator.WaitForIdle(ctx, restarted)
	}); err != nil {
		t.Fatal(err)
	}
	if finishCalls != 1 {
		t.Fatalf("startup finish calls=%d, want one", finishCalls)
	}
}
