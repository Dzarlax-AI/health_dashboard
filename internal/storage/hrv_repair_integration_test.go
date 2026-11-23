package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/testdb"
)

func insertHRVRepairPoints(t *testing.T, db *DB, points []MetricPoint) int64 {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	var id int64
	if err := db.pool.QueryRow(ctx, `INSERT INTO health_records(payload) VALUES('{}') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPoints(id, points); err != nil {
		t.Fatal(err)
	}
	return id
}

func hrvRepairPoint(date, source string, qty float64) MetricPoint {
	return MetricPoint{MetricName: "heart_rate_variability", Units: "ms", Date: date, Source: source, Qty: qty}
}

func assertHRVRepairValue(t *testing.T, db *DB, date string, want *float64) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	var got *float64
	if err := db.pool.QueryRow(ctx, `SELECT hrv_avg FROM daily_scores WHERE date=$1`, date).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want == nil {
		if got != nil {
			t.Fatalf("daily HRV for %s = %v, want NULL", date, *got)
		}
		return
	}
	if got == nil || math.Abs(*got-*want) > 1e-5 {
		if got == nil {
			t.Fatalf("daily HRV for %s = NULL, want %.5f", date, *want)
		}
		t.Fatalf("daily HRV for %s = %.5f, want %.5f", date, *got, *want)
	}
}

func assertHRVRepairHours(t *testing.T, db *DB, date string, expected map[string]map[string]float64) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := db.pool.Query(ctx, `SELECT SUBSTRING(hour,12,5),source,avg_val FROM hourly_metrics
		WHERE metric_name='heart_rate_variability' AND SUBSTRING(hour,1,10)=$1 ORDER BY 1,2`, date)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actual := map[string]map[string]float64{}
	for rows.Next() {
		var hour, source string
		var value float64
		if err := rows.Scan(&hour, &source, &value); err != nil {
			t.Fatal(err)
		}
		if actual[hour] == nil {
			actual[hour] = map[string]float64{}
		}
		actual[hour][source] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("HRV hours for %s = %#v, want %#v", date, actual, expected)
	}
	for hour, sources := range expected {
		if len(actual[hour]) != len(sources) {
			t.Fatalf("HRV sources at %s %s = %#v, want %#v", date, hour, actual[hour], sources)
		}
		for source, want := range sources {
			if got, ok := actual[hour][source]; !ok || math.Abs(got-want) > 1e-5 {
				t.Fatalf("HRV at %s %s/%s = %v, want %.5f", date, hour, source, got, want)
			}
		}
	}
}

func markHRVRepairForTest(t *testing.T, db *DB, date string, mutate func(pgx.Tx)) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if mutate != nil {
		mutate(tx)
	}
	if err := db.markCacheDirtyWithHRVRepairsTx(ctx, tx, []string{date}, []string{date}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func isolatedFullHRVRepairDB(t *testing.T) (*DB, func()) {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx, cancel := queryCtx()
	defer cancel()
	admin, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	schema := testdb.SchemaName("hrv_profile_isolated")
	if err := testdb.CreateSchema(ctx, admin, schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		_ = testdb.DropSchema(ctx, admin, schema)
		admin.Close()
		t.Fatal(err)
	}
	db := NewFromPool(pool)
	if err := db.EnsureAllTables(); err != nil {
		_ = testdb.DropSchema(ctx, admin, schema)
		db.Close()
		admin.Close()
		t.Fatal(err)
	}
	if err := db.MigrateSchemaContract(); err != nil {
		_ = testdb.DropSchema(ctx, admin, schema)
		db.Close()
		admin.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		db.Close()
		if err := testdb.DropSchema(context.Background(), admin, schema); err != nil {
			t.Errorf("drop isolated HRV profile schema: %v", err)
		}
		admin.Close()
	}
	return db, cleanup
}

func TestHRVRepairCrashRetryWeightsAndHistoricalParity(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	date, neighbor := "2026-10-20", "2026-10-21"
	insertHRVRepairPoints(t, db, []MetricPoint{
		hrvRepairPoint(date+" 08:00:00 +0000", "source-a", 40),
		hrvRepairPoint(date+" 08:30:00 +0000", "source-a", 60),
		hrvRepairPoint(date+" 09:00:00 +0000", "source-a", 80),
		hrvRepairPoint(date+" 08:15:00 +0000", "source-b", 90),
		{MetricName: "resting_heart_rate", Units: "bpm", Date: date + " 08:00:00 +0000", Source: "source-a", Qty: 62},
		{MetricName: "step_count", Units: "count", Date: date + " 12:00:00 +0000", Source: "source-a", Qty: 500},
		hrvRepairPoint(neighbor+" 08:00:00 +0000", "neighbor", 35),
	})
	assertChart := func(want, low float64) {
		t.Helper()
		points, err := db.GetMetricData("heart_rate_variability", date, date+" 23:59:59", "day", "AVG")
		if err != nil || len(points) != 1 || points[0].Qty != want || points[0].Min != low || points[0].Max != 90 {
			t.Fatalf("unexpected daily HRV chart: %+v, error %v", points, err)
		}
	}
	assertChart(77.5, 40)
	if err := db.UpsertRecentCache([]string{date, neighbor}, false); err != nil {
		t.Fatal(err)
	}
	initial := 77.5 // source-a mean=(50+80)/2; source-b mean=90; average sources.
	assertHRVRepairValue(t, db, date, &initial)
	assertChart(initial, 40)
	assertHRVRepairHours(t, db, date, map[string]map[string]float64{
		"08:00": {"source-a": 50, "source-b": 90}, "09:00": {"source-a": 80},
	})

	// Source correction and durable repair obligation commit together. Stop
	// here to represent a process crash before surviving samples are rebuilt.
	markHRVRepairForTest(t, db, date, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE metric_points SET qty=20 WHERE metric_name='heart_rate_variability'
			AND date=$1 AND source='source-a'`, date+" 08:30:00 +0000"); err != nil {
			t.Fatal(err)
		}
	})
	assertHRVRepairValue(t, db, date, nil)
	assertHRVRepairHours(t, db, date, nil)
	var pending string
	if err := db.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, hrvRepairKeyPrefix+date).Scan(&pending); err != nil || pending == "" {
		t.Fatalf("repair marker missing before retry: %q %v", pending, err)
	}

	// Simulate an older process draining ordinary dirty state while the
	// forward-compatible per-date repair marker survives its restart.
	captured, err := db.CaptureCacheDirty(ctx, []string{date})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteCacheDirty(ctx, captured); err != nil {
		t.Fatal(err)
	}
	restarted := NewFromPool(db.pool.(*pgxpool.Pool))
	if err = restarted.reconcileHRVRepairDirty(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := restarted.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dirty[date] == 0 {
		t.Fatal("orphan repair marker did not restore the ordinary dirty date")
	}
	if err = restarted.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	corrected := 72.5 // source-a mean=(30+80)/2; source-b mean=90; average sources.
	assertHRVRepairValue(t, restarted, date, &corrected)
	assertHRVRepairHours(t, restarted, date, map[string]map[string]float64{
		"08:00": {"source-a": 30, "source-b": 90}, "09:00": {"source-a": 80},
	})
	if err = restarted.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, hrvRepairKeyPrefix+date).Scan(&pending); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("repair marker should clear atomically after retry, got %q err=%v", pending, err)
	}
	var rhr *float64
	var steps *float64
	if err = restarted.pool.QueryRow(ctx, `SELECT rhr_avg,steps FROM daily_scores WHERE date=$1`, date).Scan(&rhr, &steps); err != nil || rhr == nil || *rhr != 62 || steps == nil || *steps != 500 {
		t.Fatalf("unrelated daily values changed: rhr=%v steps=%v err=%v", rhr, steps, err)
	}
	neighborValue := 35.0
	assertHRVRepairValue(t, restarted, neighbor, &neighborValue)
	assertHRVRepairHours(t, restarted, neighbor, map[string]map[string]float64{"08:00": {"neighbor": 35}})

	// Simulate a marker whose ordinary dirty entry was drained by an older
	// binary. Startup recovery must restore it, and the historical builder
	// must consume it with the same source weights as a foreground rebuild.
	markHRVRepairForTest(t, restarted, date, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE metric_points SET qty=10 WHERE metric_name='heart_rate_variability'
			AND date=$1 AND source='source-a'`, date+" 08:30:00 +0000"); err != nil {
			t.Fatal(err)
		}
	})
	captured, err = restarted.CaptureCacheDirty(ctx, []string{date})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.CompleteCacheDirty(ctx, captured); err != nil {
		t.Fatal(err)
	}
	maintenance := NewFromPool(db.pool.(*pgxpool.Pool))
	sawRestoredDate := false
	if err = maintenance.RunCacheMaintenance(ctx, "UTC", func(dates []string) error {
		if len(dates) == 0 {
			return nil
		}
		for _, dirtyDate := range dates {
			if dirtyDate == date {
				sawRestoredDate = true
			}
		}
		captured, err := maintenance.CaptureCacheDirty(ctx, dates)
		if err != nil {
			return err
		}
		return maintenance.CompleteCacheDirty(ctx, captured)
	}); err != nil {
		t.Fatal(err)
	}
	if !sawRestoredDate {
		t.Fatal("RunCacheMaintenance did not receive the orphan repair date after reconciliation")
	}
	if err = maintenance.RebuildHistoricalCacheDate(ctx, date); err != nil {
		t.Fatal(err)
	}
	historical := 71.25 // ((25+80)/2 + 90)/2.
	assertHRVRepairValue(t, restarted, date, &historical)
	assertChart(historical, 10)
}

func TestHRVDelayedPointWithoutWithdrawalKeepsExistingHour(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	date := "2026-10-25"
	recordID := insertHRVRepairPoints(t, db, []MetricPoint{hrvRepairPoint(date+" 08:00:00 +0000", "watch", 45)})
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPoints(recordID, []MetricPoint{hrvRepairPoint(date+" 10:00:00 +0000", "watch", 55)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	assertHRVRepairHours(t, db, date, map[string]map[string]float64{
		"08:00": {"watch": 45}, "10:00": {"watch": 55},
	})
	ctx, cancel := queryCtx()
	defer cancel()
	var count int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key=$1`, hrvRepairKeyPrefix+date).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ordinary delayed point unexpectedly scheduled withdrawal repair: count=%d err=%v", count, err)
	}
	cacheOnly := "2026-10-24"
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,hrv_avg) VALUES($1,61)`, cacheOnly); err != nil {
		t.Fatal(err)
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.MarkCacheDirtyTx(ctx, tx, []string{cacheOnly}); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertRecentCache([]string{cacheOnly}, false); err != nil {
		t.Fatal(err)
	}
	metrics := db.rawMetricsFromDailyScoresAt(cacheOnly, true)
	if metrics == nil || metrics.ReadinessEvidence == nil || !metrics.ReadinessEvidence.HRV.Present ||
		metrics.ReadinessEvidence.HRV.Value == nil || *metrics.ReadinessEvidence.HRV.Value != 61 ||
		string(metrics.ReadinessEvidence.HRV.Confidence) != "provisional" {
		t.Fatalf("ordinary dirty date without a withdrawal marker lost its cache fallback: %+v", metrics)
	}
}

func TestHRVRepairAllInvalidPointsBecomesNull(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	date := "2026-10-26"
	insertHRVRepairPoints(t, db, []MetricPoint{
		hrvRepairPoint(date+" 08:00:00 +0000", "watch-a", 320),
		hrvRepairPoint(date+" 09:00:00 +0000", "watch-b", 350),
	})
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	markHRVRepairForTest(t, db, date, func(tx pgx.Tx) {
		if _, err := tx.Exec(context.Background(), `UPDATE metric_points SET quality='impossible'
			WHERE metric_name='heart_rate_variability' AND SUBSTRING(date,1,10)=$1`, date); err != nil {
			t.Fatal(err)
		}
	})
	raw := db.rawMetricsFromDailyScoresAt(date, true)
	if raw != nil && raw.ReadinessEvidence != nil && raw.ReadinessEvidence.HRV.Present {
		t.Fatalf("pending all-point withdrawal exposed stale cached HRV: %+v", raw.ReadinessEvidence.HRV)
	}
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	assertHRVRepairValue(t, db, date, nil)
	assertHRVRepairHours(t, db, date, map[string]map[string]float64{})
}

func TestHRVEmptyCacheSettlesOnlyCapturedGenerations(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	newerDate, repairDate, clearDate := "2026-10-22", "2026-10-23", "2026-10-24"
	if err := db.MarkCacheDirty(ctx, []string{newerDate, repairDate, clearDate}); err != nil {
		t.Fatal(err)
	}
	markHRVRepairForTest(t, db, repairDate, nil)
	captured, err := db.CaptureCacheDirty(ctx, []string{newerDate, repairDate, clearDate})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkCacheDirty(ctx, []string{newerDate}); err != nil {
		t.Fatal(err)
	}
	settled, err := db.settleEmptyCacheDirty(ctx, captured)
	if err != nil || !settled {
		t.Fatalf("empty settlement=%v err=%v", settled, err)
	}
	state, err := db.LoadCacheMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dirty[newerDate] <= captured[newerDate] || state.Dirty[repairDate] != captured[repairDate] || state.Dirty[clearDate] != 0 {
		t.Fatalf("settlement did not fence newer/pending generations: captured=%v current=%v", captured, state.Dirty)
	}
	var marker string
	if err := db.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, hrvRepairKeyPrefix+repairDate).Scan(&marker); err != nil || marker == "" {
		t.Fatalf("settlement cleared pending HRV repair marker: %q err=%v", marker, err)
	}
	var payload string
	if err := db.pool.QueryRow(ctx, `SELECT payload::text FROM dashboard_cache_snapshots WHERE singleton`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Date  string          `json:"date"`
		Cards json.RawMessage `json:"cards"`
	}
	if err := json.Unmarshal([]byte(payload), &dashboard); err != nil {
		t.Fatal(err)
	}
	var cards []json.RawMessage
	if err := json.Unmarshal(dashboard.Cards, &cards); err != nil && string(dashboard.Cards) != "null" {
		t.Fatalf("decode empty dashboard cards: %s: %v", dashboard.Cards, err)
	}
	if dashboard.Date != "" || len(cards) != 0 {
		t.Fatalf("empty dashboard snapshot not published: %s", payload)
	}
}

func TestHRVWithdrawalIsProfileIsolatedAndKeepsArchivedReport(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	other, cleanupOther := isolatedFullHRVRepairDB(t)
	defer cleanupOther()
	ctx, cancel := queryCtx()
	defer cancel()
	date := "2026-10-28"
	var recordID int64
	if err := db.pool.QueryRow(ctx, `INSERT INTO health_records(automation_name,payload) VALUES('Health dash - Hourly','{}') RETURNING id`).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,origin,quality)
		VALUES($1,'heart_rate_variability','ms',$2,80,'primary-watch','live','ok')`, recordID, date+" 08:00:00 +0000"); err != nil {
		t.Fatal(err)
	}
	insertHRVRepairPoints(t, other, []MetricPoint{hrvRepairPoint(date+" 08:00:00 +0000", "other-watch", 45)})
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	if err := other.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	key := "report:morning:" + date
	archived := &ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: date, Timezone: "UTC", Language: "en",
		PlainBody: "archived sent report", RichBody: "<b>archived sent report</b>", DecisionID: "synthetic",
		Context: []byte(`{"source":"synthetic"}`), AIMetadata: []byte(`{"provider":"synthetic"}`)}
	token, reserved, err := db.ReserveArchivedReport(ctx, archived)
	if err != nil || !reserved {
		t.Fatalf("archive synthetic sent report: reserved=%v err=%v", reserved, err)
	}
	attempt, err := db.ReserveReportAttempt(ctx, archived.ID, "rich", archived.RichBody, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteReportAttempt(ctx, attempt, "sent", "", nil); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetReportHistory(ctx, archived.ID)
	if err != nil || len(before.Attempts) != 1 || before.Attempts[0].Status != "sent" {
		t.Fatalf("read sent report before withdrawal: %+v err=%v", before, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE daily_scores SET readiness=88,score_version=$2 WHERE date=$1`, date, ScoreVersion); err != nil {
		t.Fatal(err)
	}
	db.RemoveAutoExportForRange(date, date)
	assertHRVWithdrawalReadsInvalidated(t, db, date)
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	assertHRVRepairValue(t, db, date, nil)
	otherValue := 45.0
	assertHRVRepairValue(t, other, date, &otherValue)
	after, err := db.GetReportHistory(ctx, archived.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("archived sent report changed after HRV withdrawal: before=%+v after=%+v err=%v", before, after, err)
	}
	var primaryPoints, otherPoints int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM metric_points WHERE metric_name='heart_rate_variability' AND SUBSTRING(date,1,10)=$1`, date).Scan(&primaryPoints); err != nil {
		t.Fatal(err)
	}
	if err := other.pool.QueryRow(ctx, `SELECT count(*) FROM metric_points WHERE metric_name='heart_rate_variability' AND SUBSTRING(date,1,10)=$1`, date).Scan(&otherPoints); err != nil {
		t.Fatal(err)
	}
	if primaryPoints != 0 || otherPoints != 1 {
		t.Fatalf("cross-profile mutation: primary points=%d other points=%d", primaryPoints, otherPoints)
	}
}

// Check real reader paths before any best-effort refresh can run. This also
// represents a process crash immediately after the source transaction commits.
func assertHRVWithdrawalReadsInvalidated(t *testing.T, db *DB, date string) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	assertHRVRepairValue(t, db, date, nil)
	assertHRVRepairHours(t, db, date, nil)
	var readiness, version *int
	if err := db.pool.QueryRow(ctx, `SELECT readiness,score_version FROM daily_scores WHERE date=$1`, date).Scan(&readiness, &version); err != nil || readiness != nil || version != nil {
		t.Fatalf("withdrawn HRV retained readiness=%v version=%v: %v", readiness, version, err)
	}
	for _, bucket := range []string{"hour", "day"} {
		points, err := db.GetMetricData("heart_rate_variability", date, date+" 23:59:59", bucket, "AVG")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := db.metricDataRaw("heart_rate_variability", date, date+" 23:59:59", bucket, "AVG")
		if err != nil || !reflect.DeepEqual(points, fresh) {
			t.Fatalf("%s reader did not use surviving raw HRV: got=%+v raw=%+v %v", bucket, points, fresh, err)
		}
	}
	autonomic, err := db.LoadAutonomicRows(date, date)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range autonomic {
		if row.HRV != nil {
			t.Fatalf("autonomic loader retained withdrawn HRV: %+v", row)
		}
	}
	dashboard, err := db.GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range dashboard.Cards {
		if card.Metric == "heart_rate_variability" && card.Date == date {
			t.Fatalf("dashboard snapshot retained withdrawn HRV: %+v", card)
		}
	}
}

func TestHRVWithdrawalFailedRefreshKeepsCachedReadersInvalidated(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	date := "2026-10-29"
	id := insertHRVRepairPoints(t, db, []MetricPoint{hrvRepairPoint(date+" 08:00:00 +0000", "auto-export", 90)})
	if _, err := db.pool.Exec(ctx, `UPDATE health_records SET automation_name='Health dash - Hourly' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	insertHRVRepairPoints(t, db, []MetricPoint{hrvRepairPoint(date+" 09:00:00 +0000", "survivor", 40)})
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE daily_scores SET readiness=88,score_version=$2 WHERE date=$1`, date, ScoreVersion); err != nil {
		t.Fatal(err)
	}
	db.RemoveAutoExportForRange(date, date)
	assertHRVWithdrawalReadsInvalidated(t, db, date)
	if _, err := db.pool.Exec(ctx, `CREATE FUNCTION fail_hrv_refresh() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic HRV refresh failure'; END $$;
		CREATE TRIGGER fail_hrv_refresh BEFORE INSERT ON hourly_metrics FOR EACH STATEMENT EXECUTE FUNCTION fail_hrv_refresh()`); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS fail_hrv_refresh ON hourly_metrics; DROP FUNCTION IF EXISTS fail_hrv_refresh()`)
	if err := db.UpsertRecentCache([]string{date}, true); err == nil || !strings.Contains(err.Error(), "synthetic HRV refresh failure") {
		t.Fatalf("expected injected HRV refresh failure, got %v", err)
	}
	assertHRVWithdrawalReadsInvalidated(t, db, date)
	var pending bool
	if err := db.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM settings WHERE key=$1)`, hrvRepairKeyPrefix+date).Scan(&pending); err != nil || !pending {
		t.Fatalf("failed refresh lost repair obligation: %v %v", pending, err)
	}
	if _, err := db.pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_hrv_refresh ON hourly_metrics; DROP FUNCTION IF EXISTS fail_hrv_refresh()`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRecentCache([]string{date}, true); err != nil {
		t.Fatal(err)
	}
	want := 40.0
	assertHRVRepairValue(t, db, date, &want)
}
