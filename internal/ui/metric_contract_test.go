package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
)

func TestHealthKitContractReadSurfaces(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := sharedTenantPool.Exec(ctx, `ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS quality TEXT NOT NULL DEFAULT 'ok'`); err != nil {
		t.Fatal(err)
	}
	const day = "2026-08-01"
	const old = "six_min_walk_distance"
	const current = "six_minute_walking_test_distance"
	id, err := db.InsertRaw(storage.Record{ContentType: "application/json", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedTenantPool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,date,units,source,qty)
		VALUES ($1,$2,$3,'m','Watch',525),($1,'height',$3,'cm','Phone',180),($1,'height',$4,'in','Phone',72)`,
		id, old, day+" 10:00:00 +0200", day+" 10:10:00 +0200"); err != nil {
		t.Fatal(err)
	}
	if err := db.PrepareMetricCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{}
	get := func(route http.HandlerFunc, path, requestVersion, responseVersion string, target any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(ctxdb.WithDB(req.Context(), db, schema))
		if requestVersion != "" {
			req.Header.Set(metriccontract.Header, requestVersion)
		}
		recorder := httptest.NewRecorder()
		route(recorder, req)
		if recorder.Code != http.StatusOK || recorder.Header().Get(metriccontract.Header) != responseVersion {
			t.Fatalf("%s status=%d contract=%s body=%s", path, recorder.Code, recorder.Header().Get(metriccontract.Header), recorder.Body.String())
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct{ flag, request, expected string }{
		{"1", "", "1"}, {"1", "2", "2"}, {"0", "", "2"}, {"1", "", "1"},
	} {
		t.Setenv("HEALTH_METRICS_V1_ENABLED", tt.flag)
		var catalog []struct{ Name, Units string }
		get(handler.listMetrics, "/api/metrics", tt.request, tt.expected, &catalog)
		wantAlias := tt.expected == "1"
		if !slices.ContainsFunc(catalog, func(m struct{ Name, Units string }) bool { return m.Name == current && m.Units == "m" }) ||
			slices.ContainsFunc(catalog, func(m struct{ Name, Units string }) bool { return m.Name == old }) != wantAlias {
			t.Fatalf("version=%s catalog=%+v", tt.expected, catalog)
		}
		var latest []storage.LatestValue
		get(handler.latestMetricValues, "/api/metrics/latest", tt.request, tt.expected, &latest)
		if !slices.ContainsFunc(latest, func(m storage.LatestValue) bool { return m.Metric == "height" && m.Value == 1.8 && m.Unit == "m" }) ||
			slices.ContainsFunc(latest, func(m storage.LatestValue) bool { return m.Metric == old }) != wantAlias {
			t.Fatalf("version=%s latest=%+v", tt.expected, latest)
		}
		var dateRange struct{ Min, Max string }
		get(handler.metricRange, "/api/metrics/range?metric="+old, tt.request, tt.expected, &dateRange)
		if dateRange.Min != day || dateRange.Max != day {
			t.Fatalf("date range=%+v", dateRange)
		}
		var series struct{ Points []storage.DataPoint }
		get(handler.metricData, "/api/metrics/data?metric=height&from="+day+"&to="+day+"&bucket=hour", tt.request, tt.expected, &series)
		if len(series.Points) != 1 || series.Points[0].Qty != 1.8 {
			t.Fatalf("version=%s graph=%+v", tt.expected, series.Points)
		}
	}
}
