package storage

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDBTHRVInvalidationTransition compares the actual incremental Go writers
// and fresh read with dbt's semantic shadow in an owned synthetic database.
func TestDBTHRVInvalidationTransition(t *testing.T) {
	if os.Getenv("DBT_PILOT_ENABLE") != "synthetic-only" {
		t.Skip("opt-in local dbt pilot")
	}
	dsn := os.Getenv("DBT_PILOT_DSN")
	if !strings.HasPrefix(dsn, "postgres://pilot_admin:synthetic-only@127.0.0.1:") || !strings.HasSuffix(dsn, "/hp_dbt_pilot?sslmode=disable") {
		t.Fatal("explicit synthetic loopback URL required")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Database != "hp_dbt_pilot" || cfg.ConnConfig.User != "pilot_admin" || len(cfg.ConnConfig.Fallbacks) != 0 {
		t.Fatal("pilot requires owned loopback database")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "go_oracle,public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var marker string
	if err = pool.QueryRow(ctx, "SELECT token FROM public.pilot_identity").Scan(&marker); err != nil || marker == "" || marker != os.Getenv("DBT_PILOT_TOKEN") {
		t.Fatal("pilot identity mismatch", err)
	}
	exec := func(query string) {
		t.Helper()
		if _, e := pool.Exec(ctx, query); e != nil {
			t.Fatal(e)
		}
	}
	exec(`TRUNCATE source_data.metric_points, source_data.calendar, source_data.daily_hrv,
		go_oracle.hourly_metrics, go_oracle.daily_scores`)
	exec(`INSERT INTO source_data.calendar VALUES ('2000-01-01'),('2000-01-02');
		INSERT INTO source_data.metric_points(metric_name,date,source,qty,quality) VALUES
		('heart_rate_variability','2000-01-01 08:00:00 +0000','fixture',60,'ok'),
		('heart_rate_variability','2000-01-02 08:00:00 +0000','fixture',35,'ok')`)
	db := &DB{pool: pool}
	refresh := func(day string) {
		t.Helper()
		if e := db.upsertHourlyAvgForDate(day); e != nil {
			t.Fatal(e)
		}
		if e := db.upsertDailyForDate(day); e != nil {
			t.Fatal(e)
		}
	}
	refresh("2000-01-01")
	refresh("2000-01-02")
	mirror := func() {
		t.Helper()
		exec(`TRUNCATE source_data.daily_hrv;
			INSERT INTO source_data.daily_hrv
            SELECT d.date,d.hrv_avg,repair.key IS NOT NULL FROM go_oracle.daily_scores d
            LEFT JOIN go_oracle.settings repair ON repair.key='` + hrvRepairKeyPrefix + `' || d.date`)
	}
	mirror()
	var live, morning, hourly, daily *float64
	var count int
	var confidence, origin string
	read := func() {
		t.Helper()
		if e := pool.QueryRow(ctx, `SELECT live_ms,morning_ms,n_points,morning_origin,morning_confidence
			FROM dbt_output.semantic_hrv_daily_v1 WHERE date='2000-01-01'`).Scan(&live, &morning, &count, &origin, &confidence); e != nil {
			t.Fatal(e)
		}
		if e := pool.QueryRow(ctx, `SELECT (SELECT avg_val FROM go_oracle.hourly_metrics
			WHERE metric_name='heart_rate_variability' AND hour='2000-01-01 08:00' AND source='fixture')`).Scan(&hourly); e != nil {
			t.Fatal(e)
		}
		if e := pool.QueryRow(ctx, `SELECT hrv_avg FROM go_oracle.daily_scores WHERE date='2000-01-01'`).Scan(&daily); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if live == nil || *live != 60 || hourly == nil || *hourly != 60 || daily == nil || *daily != 60 || morning == nil || *morning != 60 || count != 1 || origin != "raw" {
		t.Fatalf("initial HRV mismatch: live=%v hourly=%v daily=%v morning=%v count=%d origin=%s", live, hourly, daily, morning, count, origin)
	}
	exec(`UPDATE source_data.metric_points SET qty=70 WHERE date='2000-01-01 08:00:00 +0000' AND source='fixture'`)
	refresh("2000-01-01")
	mirror()
	read()
	if live == nil || *live != 70 || hourly == nil || *hourly != 70 || daily == nil || *daily != 70 || morning == nil || *morning != 70 || count != 1 || origin != "raw" {
		t.Fatalf("valid correction mismatch: live=%v hourly=%v daily=%v morning=%v count=%d origin=%s", live, hourly, daily, morning, count, origin)
	}
	exec(`UPDATE source_data.metric_points SET quality='invalid' WHERE date='2000-01-01 08:00:00 +0000' AND source='fixture'`)
	refresh("2000-01-01") // Withdraw the rejected hour while preserving the retained daily fallback.
	mirror()
	read()
	if live != nil || hourly != nil || daily == nil || *daily != 70 || morning == nil || *morning != 70 || count != 0 || origin != "same_day_cache" || confidence != "provisional" {
		t.Fatalf("transition mismatch: live=%v hourly=%v daily=%v morning=%v count=%d origin=%s confidence=%s", live, hourly, daily, morning, count, origin, confidence)
	}
	if fresh := db.freshDayFromRaw("2000-01-01"); fresh != nil {
		t.Fatalf("invalid-only fresh input should be absent, got %+v", fresh)
	}
	data := db.rawMetricsFromDailyScoresAt("2000-01-01", true)
	if data == nil || data.ReadinessEvidence == nil || !data.ReadinessEvidence.HRV.Present || data.ReadinessEvidence.HRV.Value == nil || *data.ReadinessEvidence.HRV.Value != 70 || string(data.ReadinessEvidence.HRV.Confidence) != "provisional" {
		t.Fatal("Go morning evidence did not retain the provisional same-day cache value")
	}
	// Capture the interval after a confirmed withdrawal commits and before repair.
	if _, err := pool.Exec(ctx, "INSERT INTO go_oracle.settings(key,value) VALUES ($1,'1')", hrvRepairKeyPrefix+"2000-01-01"); err != nil {
		t.Fatal(err)
	}
	mirror()
	read()
	if morning != nil || origin != "missing_same_day_value" || confidence != "missing" || daily == nil || *daily != 70 {
		t.Fatal("dbt morning result used a cached value pending repair")
	}
	data = db.rawMetricsFromDailyScoresAt("2000-01-01", true)
	if data != nil && data.ReadinessEvidence != nil && data.ReadinessEvidence.HRV.Present {
		t.Fatal("Go morning result used a cached value pending repair")
	}
	var neighbor float64
	if err = pool.QueryRow(ctx, `SELECT hrv_avg FROM go_oracle.daily_scores WHERE date='2000-01-02'`).Scan(&neighbor); err != nil || neighbor != 35 {
		t.Fatalf("neighbor changed: %v %v", neighbor, err)
	}
	t.Log("synthetic Go incremental cache retained 70; dbt recomputation omitted invalid input; both morning paths allowed ordinary fallback and suppressed it during a pending withdrawal; neighbor unchanged")
}
