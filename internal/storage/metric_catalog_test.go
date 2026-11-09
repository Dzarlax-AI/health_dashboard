package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMetricCatalogUnitEvidenceAndReadOnlyHistory(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	var recordID int64
	if err := db.pool.QueryRow(ctx, `INSERT INTO health_records(processing_status) VALUES('complete') RETURNING id`).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, unit, status string
		rawUnits           []string
	}{
		{"active_energy", "kcal", MetricUnitKnown, []string{"kcal", "kcal"}},
		{"blood_oxygen_saturation", "%", MetricUnitKnown, []string{"%"}},
		{"walking_running_distance", "", MetricUnitMixed, []string{"km", "mi"}},
		{"heart_rate", "", MetricUnitMixed, []string{"bpm", "count/min"}},
		{"missing_unit", "", MetricUnitUnknown, []string{"", "  "}},
		{"partly_missing", "", MetricUnitUnknown, []string{"m", ""}},
		{"cache_only", "", MetricUnitUnknown, nil},
		{"trimmed_unit", "m", MetricUnitKnown, []string{" m ", "m"}},
		{"six_min_walk_distance", "m", MetricUnitKnown, []string{"m"}},
		{"six_minute_walking_test_distance", "m", MetricUnitKnown, []string{"m"}},
	}
	for _, tc := range cases {
		if _, err := db.pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count) VALUES($1,'2026-09-10 08:00','Synthetic',20,10,30,2)`, tc.name); err != nil {
			t.Fatal(err)
		}
		for index, unit := range tc.rawUnits {
			if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES($1,$2,$3,$4,$5,'Synthetic','ok')`, recordID, tc.name, unit, []string{"2026-09-10 08:00:00 +00:00", "2026-09-10 08:01:00 +00:00"}[index], 10+index*20); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Historical raw-only evidence must prevent a confident unit even when
	// the cached portion happens to use a single, different unit.
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES($1,'active_energy','kJ','2020-01-01 08:00:00 +00:00',40,'Synthetic','ok')`, recordID); err != nil {
		t.Fatal(err)
	}
	cases[0].unit, cases[0].status = "", MetricUnitMixed
	before := metricCatalogHistory(t, db)
	valuesBefore, err := db.GetMetricData("active_energy", "2026-09-10", "2026-09-10 23:59:59", "day", "SUM")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := db.ListMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != len(cases) {
		t.Fatalf("catalogue has %d rows, want %d", len(metrics), len(cases))
	}
	byName := make(map[string]MetricSummary)
	for _, metric := range metrics {
		byName[metric.Name] = metric
	}
	for _, tc := range cases {
		metric, exists := byName[tc.name]
		if !exists || metric.Units != tc.unit || metric.UnitStatus != tc.status {
			t.Errorf("%s: %+v, want units=%q status=%s", tc.name, metric, tc.unit, tc.status)
		}
		if metric.Count != 1 || metric.Min != "2026-09-10" || metric.Max != "2026-09-10" {
			t.Errorf("%s: changed hourly catalogue count/dates: %+v", tc.name, metric)
		}
	}
	valuesAfter, err := db.GetMetricData("active_energy", "2026-09-10", "2026-09-10 23:59:59", "day", "SUM")
	if err != nil || !reflect.DeepEqual(valuesBefore, valuesAfter) {
		t.Fatalf("catalogue read changed numerical chart response: %v", err)
	}
	if !reflect.DeepEqual(before, metricCatalogHistory(t, db)) {
		t.Fatal("catalogue read changed raw or hourly history")
	}
	var plan []byte
	if err := db.pool.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+metricCatalogQuery).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(plan) || strings.Contains(string(plan), "SubPlan") {
		t.Fatal("catalogue must use batch evidence rather than correlated subqueries")
	}
	t.Logf("batch catalogue query plan verified for %d synthetic series", len(cases))
}

func metricCatalogHistory(t *testing.T, db *DB) [][]byte {
	t.Helper()
	var raw, hourly []byte
	if err := db.pool.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(p) ORDER BY metric_name,date,source) FROM metric_points p`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(h) ORDER BY metric_name,hour,source) FROM hourly_metrics h`).Scan(&hourly); err != nil {
		t.Fatal(err)
	}
	return [][]byte{raw, hourly}
}
