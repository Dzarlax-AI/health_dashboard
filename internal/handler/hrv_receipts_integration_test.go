package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/applehealth"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

// Run against a disposable PostgreSQL 16+ with HEALTH_DB_TESTS, READINESS_TEST_DSN and HEALTH_DBT_BINARY set.
func TestTypedHRVReceiptIngest(t *testing.T) {
	bin := os.Getenv("HEALTH_DBT_BINARY")
	if bin == "" {
		t.Skip("requires local dbt binary")
	}
	ctx := context.Background()
	dsn := testdb.DSN(t)
	admin, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := testdb.SchemaName("hrv_receipts")
	if err := testdb.CreateSchema(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testdb.DropSchema(ctx, admin, schema); err != nil {
			t.Error(err)
		}
	}()
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := storage.NewFromPool(pool)
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	cfg := pool.Config().ConnConfig
	profile := map[string]any{"health_dbt_metrics_pilot": map[string]any{"target": "fixture", "outputs": map[string]any{"fixture": map[string]any{"type": "postgres", "host": cfg.Host, "port": cfg.Port, "user": cfg.User, "password": cfg.Password, "dbname": cfg.Database, "schema": schema, "threads": 1}}}}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "profiles.yml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	project, err := filepath.Abs("../../experiments/dbt_metrics")
	if err != nil {
		t.Fatal(err)
	}
	vars := fmt.Sprintf(`{"semantic_source_schema":%q,"semantic_shadow":true,"hrv_receipt_inputs":true,"sleep_duration_reference":true}`, schema)
	cmd := exec.Command(bin, "run", "--project-dir", project, "--profiles-dir", tmp, "--target-path", filepath.Join(tmp, "target"), "--log-path", filepath.Join(tmp, "logs"), "--vars", vars, "--select", "+semantic_hrv_observation_v1")
	cmd.Env = append(os.Environ(), "DBT_SEND_ANONYMOUS_USAGE_STATS=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dbt run: %v\n%s", err, out)
	}

	const stamp = "2026-06-10 01:00:00 +0200"
	const end = "2026-06-10 01:01:00 +0200"
	sample := func(source string, qty int, attrs map[string]string) map[string]any {
		value := map[string]any{"source": source, "date": stamp, "qty": qty}
		for k, v := range attrs {
			value[k] = v
		}
		return value
	}
	post := func(aggregation string, samples ...map[string]any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"data": map[string]any{"metrics": []any{map[string]any{"name": "heart_rate_variability", "units": "ms", "data": samples}}}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/health", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("automation-aggregation", aggregation)
		rec := httptest.NewRecorder()
		(&Handler{refreshCache: (*storage.DB).UpsertRecentCache}).health(rec, req.WithContext(ctxdb.WithDB(req.Context(), db, schema)))
		if rec.Code != http.StatusOK {
			t.Fatalf("ingest status %d", rec.Code)
		}
	}
	check := func(source, method, kind, status string, qty int) {
		t.Helper()
		var m, k, s string
		var v float64
		var ends bool
		err := pool.QueryRow(ctx, `select method,time_kind,analytic_status,qty,end_at is not null from semantic_hrv_observation_v1
         where source=$1 and date=$2`, source, stamp).Scan(&m, &k, &s, &v, &ends)
		if err != nil || m != method || k != kind || s != status || v != float64(qty) || ends != (kind == "interval") {
			t.Fatalf("%s: got %s/%s/%s qty=%v end=%v err=%v", source, m, k, s, v, ends, err)
		}
	}
	post("", sample("Watch", 52, map[string]string{"identifier": "HKQuantityTypeIdentifierHeartRateVariabilitySDNN", "start_at": stamp, "end_at": end}),
		sample("Ring", 44, map[string]string{"method": "RMSSD"}), sample("Generic", 52, nil),
		sample("Conflict", 48, map[string]string{"identifier": "HKQuantityTypeIdentifierHeartRateVariabilitySDNN", "method": "RMSSD"}),
		sample("Invalid", 50, map[string]string{"method": "sdnn", "end_at": "bad time"}))
	check("Watch", "sdnn", "interval", "eligible", 52)
	check("Ring", "rmssd", "timestamp", "other_method", 44)
	check("Generic", "unknown", "timestamp", "unknown_method", 52)
	check("Conflict", "unknown", "timestamp", "unknown_method", 48)
	check("Invalid", "sdnn", "unknown", "time_precision", 50)
	post("hourly", sample("Bucket", 49, map[string]string{"method": "sdnn", "end_at": end}))
	check("Bucket", "sdnn", "bucket", "time_precision", 49)
	post("", sample("Watch", 58, nil))
	check("Watch", "unknown", "timestamp", "unknown_method", 58)
	post("", sample("Watch", 59, map[string]string{"method": "sdnn"}), sample("Watch", 59, map[string]string{"method": "rmssd"}))
	check("Watch", "unknown", "timestamp", "unknown_method", 59)
	// Legacy imports and malformed receipts have no point-level method evidence.
	// Exercise the durable path also when their raw contents cannot be parsed.
	for _, tc := range []struct {
		source, body string
		qty          int
	}{
		{"Legacy", "legacy import receipt", 54},
		{"WrongValue", `{"data":{"metrics":[{"name":"heart_rate_variability","units":"ms","data":[{"date":"2026-06-10 01:00:00 +0200","source":"WrongValue","qty":99,"method":"sdnn"}]}]}}`, 56},
	} {
		id, err := db.InsertRaw(storage.Record{ContentType: "application/json", Payload: tc.body})
		if err != nil {
			t.Fatal(err)
		}
		err = db.InsertPoints(id, []storage.MetricPoint{{MetricName: "heart_rate_variability", Date: stamp, Source: tc.source, Units: "ms", Qty: float64(tc.qty)}})
		if err != nil {
			t.Fatal(err)
		}
		check(tc.source, "unknown", "timestamp", "unknown_method", tc.qty)
	}

	for _, tc := range []struct {
		source         string
		normalized     bool
		method, status string
	}{
		{"NewXML", true, "sdnn", "eligible"}, {"OldXML", false, "unknown", "unknown_method"},
	} {
		session, err := db.BeginAppleHealthXMLImport(storage.ImportOptions{SourceName: "synthetic archive", CatalogNormalized: tc.normalized, SnapshotAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		xml := fmt.Sprintf(`<HealthData><Record type="HKQuantityTypeIdentifierHeartRateVariabilitySDNN" sourceName="%s" unit="ms" startDate="%s" endDate="%s" value="62"/></HealthData>`, tc.source, stamp, end)
		var points []storage.MetricPoint
		if err := applehealth.ParseXML(strings.NewReader(xml), func(batch []storage.MetricPoint) { points = append(points, batch...) }); err != nil {
			t.Fatal(err)
		}
		if len(points) != 1 {
			t.Fatalf("unexpected imported HRV points: %d", len(points))
		}
		if err := session.AddPoints(points); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Commit(); err != nil {
			t.Fatal(err)
		}
		check(tc.source, tc.method, "timestamp", tc.status, 62)
	}
	var collision []storage.MetricPoint
	xml := fmt.Sprintf(`<HealthData><Record type="HKQuantityTypeIdentifierHeartRateVariability" sourceName="Fallback" unit="ms" startDate="%s" endDate="%s" value="62"/></HealthData>`, stamp, end)
	if err := applehealth.ParseXML(strings.NewReader(xml), func(batch []storage.MetricPoint) { collision = append(collision, batch...) }); err != nil {
		t.Fatal(err)
	}
	if len(collision) > 0 {
		t.Fatal("fallback XML key collides with catalog SDNN")
	}

}
