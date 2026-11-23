package storage

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Uses the same disposable fixture as TestPersonalBaselineDBTShadow.
func TestMetricRuntimeBaselineCapture(t *testing.T) {
	dir, port := os.Getenv("HEALTH_METRIC_AUDIT_DIR"), os.Getenv("PERSONAL_BASELINE_FIXTURE_PORT")
	if dir == "" || port == "" {
		t.Skip("requires synthetic baseline fixture and audit directory")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig("postgres://postgres:synthetic-only@127.0.0.1:" + port + "/personal_baseline_fixture?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "fixture_one"
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var channel BaselineChannel
	var date, zone string
	var window int
	if err = pool.QueryRow(ctx, `select channel,anchor::text,zone,window_days from derived.personal_baseline_v1 where profile='fixture_one' and eligible order by capture limit 1`).Scan(&channel, &date, &zone, &window); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	result, ok, err := (&DB{pool: pool}).PersonalBaselineContext(ctx, date, channel, window, loc)
	if err != nil || !ok {
		t.Fatalf("baseline reader: eligible=%v err=%v", ok, err)
	}
	writeMetricRuntimeCapture(t, dir, "baseline.json", metricRuntimeCapture{"personal_baseline", "storage.PersonalBaselineContext", result, map[string]any{"date": date, "channel": channel, "window_days": window}})
}
