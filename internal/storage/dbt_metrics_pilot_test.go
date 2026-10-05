package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDBTMetricsPilot calls the real aggregate writers only in a marked local
// synthetic database. It never uses DATABASE_URL or the shared DB test fixture.
func TestDBTMetricsPilot(t *testing.T) {
	if os.Getenv("DBT_PILOT_ENABLE") != "synthetic-only" {
		t.Skip("opt-in local dbt pilot")
	}
	ctx := context.Background()
	dsn := os.Getenv("DBT_PILOT_DSN")
	if !strings.HasPrefix(dsn, "postgres://pilot_admin:synthetic-only@127.0.0.1:") || !strings.HasSuffix(dsn, "/hp_dbt_pilot?sslmode=disable") {
		t.Fatal("explicit synthetic loopback URL required")
	}
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
	var dates []string
	if err = json.Unmarshal([]byte(os.Getenv("DBT_PILOT_DATES")), &dates); err != nil || len(dates) == 0 {
		t.Fatal("explicit dates required", err)
	}
	for _, d := range dates {
		if parsed, e := time.Parse("2006-01-02", d); e != nil || parsed.Format("2006-01-02") != d {
			t.Fatal("invalid date")
		}
	}
	if os.Getenv("DBT_PILOT_RESET") == "1" {
		if _, err = pool.Exec(ctx, "TRUNCATE go_oracle.hourly_metrics,go_oracle.daily_scores"); err != nil {
			t.Fatal(err)
		}
	}
	db := &DB{pool: pool}
	start := time.Now()
	mode := os.Getenv("DBT_PILOT_GO_MODE")
	if mode == "legacy-date" {
		for _, d := range dates {
			if e := db.RebuildHistoricalCacheDate(ctx, d); e != nil {
				t.Fatal(d, e)
			}
		}
	} else if mode == "legacy-force" || mode == "legacy-incremental" {
		force := mode == "legacy-force"
		names, e := db.listMetricNames()
		if e != nil {
			t.Fatal(e)
		}
		for _, name := range names {
			if e = db.buildHourlyMetric(name, aggFuncFor(name), force); e != nil {
				t.Fatal(name, e)
			}
		}
		for _, spec := range [][2]string{{"hrv_avg", "heart_rate_variability"}, {"rhr_avg", "resting_heart_rate"}, {"steps", "step_count"}, {"calories", "active_energy"}, {"exercise_min", "apple_exercise_time"}, {"spo2_avg", "blood_oxygen_saturation"}, {"vo2_avg", "vo2_max"}, {"resp_avg", "respiratory_rate"}} {
			if e = db.buildDailyMetricCol(spec[0], spec[1], force); e != nil {
				t.Fatal(spec, e)
			}
		}
		if e = db.buildDailySleepBlock(force); e != nil {
			t.Fatal(e)
		}
	} else if mode == "" || mode == "live" {
		for _, d := range dates {
			for _, f := range []func(string) error{db.upsertHourlyAvgForDate, db.upsertHourlySumForDate, db.upsertHourlySleepForDate, db.upsertDailyForDate} {
				if err = f(d); err != nil {
					t.Fatal(d, err)
				}
			}
		}
	} else {
		t.Fatal("unsupported Go pilot mode")
	}
	elapsed := time.Since(start).Seconds()
	out := map[string]any{
		"seconds": elapsed,
		"aggregation_contract": map[string]string{
			"version":  AggregateContractVersion,
			"checksum": AggregateContractChecksum(),
		},
	}
	for name, q := range map[string]string{
		"hourly": "SELECT row_to_json(r) FROM (SELECT * FROM go_oracle.hourly_metrics ORDER BY metric_name,hour,source) r",
		"daily":  "SELECT row_to_json(r) FROM (SELECT date,hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,sleep_unspecified,steps,calories,exercise_min,spo2_avg,vo2_avg,resp_avg FROM go_oracle.daily_scores ORDER BY date) r",
	} {
		rows, e := pool.Query(ctx, q)
		if e != nil {
			t.Fatal(e)
		}
		values := []json.RawMessage{}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				t.Fatal(e)
			}
			values = append(values, json.RawMessage(b))
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
		out[name] = values
	}
	sums := sumMetricSlice()
	sort.Strings(sums)
	out["sum_metrics"] = sums
	path := os.Getenv("DBT_PILOT_OUTPUT")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Clean(filepath.Join(wd, "../../experiments/dbt_metrics/artifacts"))
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Clean(path)) != artifactDir {
		t.Fatal("output must be inside pilot artifacts")
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
