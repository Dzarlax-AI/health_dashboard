package ui

import (
	"context"
	"encoding/json"
	"health-receiver/internal/testdb"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
)

func TestMetricQuantityChartHTTP(t *testing.T) {
	db, mux, schema := metricHTTPFixture(t)
	id, err := db.InsertRaw(storage.Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	const day = "2026-10-25"
	if err := db.InsertPoints(id, []storage.MetricPoint{
		{MetricName: "resting_heart_rate", Date: day + " 12:00:00 +0100", Source: "Apple Watch", Units: "count/min", Qty: 61},
		{MetricName: "step_count", Date: day + " 02:00:00 +0200", Source: "Apple Watch", Units: "count", Qty: 30},
		{MetricName: "step_count", Date: day + " 02:00:00 +0100", Source: "Apple Watch", Units: "count", Qty: 15},
		{MetricName: "step_count", Date: day + " 02:00:00 +0200", Source: "iPhone", Units: "count", Qty: 120},
	}); err != nil {
		t.Fatal(err)
	}

	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set(metriccontract.Header, "2")
		r.Header.Set("X-API-Key", "catalog-fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	const base = "/api/metrics/data?metric=step_count&bucket=day&to=" + day
	var partial struct {
		Points []storage.MetricQuantityPoint `json:"points"`
	}
	adapter := request("/fragments/step-daily-series?to=" + day)
	if adapter.Code != 200 || json.Unmarshal(adapter.Body.Bytes(), &partial) != nil || len(partial.Points) != 1 || partial.Points[0].Qty != 45 {
		t.Fatal("compatibility route lost its physical hours")
	}
	for _, extra := range []string{"&range=all", "&from=" + day} {
		r := request(base + "&view=quantity" + extra)
		var got clientapi.MetricQuantityResponse
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &got) != nil || got.Agg != "SUM" || got.Metric != "step_count" || got.Bucket != "day" || !slices.Equal(got.Points, partial.Points) {
			t.Fatalf("invalid daily quantities: HTTP %d, points %+v", r.Code, got.Points)
		}
	}

	full := request(base + "&from=" + day)
	var extrema clientapi.MetricDataResponse
	if full.Code != 200 || json.Unmarshal(full.Body.Bytes(), &extrema) != nil || len(extrema.Points) != 1 || extrema.Points[0].Min >= extrema.Points[0].Max {
		t.Fatalf("observation extrema lost: %+v", extrema.Points)
	}
	for _, tc := range []struct {
		agg  string
		want float64
	}{{"MIN", 15}, {"MAX", 30}} {
		w := request(base + "&view=quantity&from=" + day + "&agg=" + tc.agg)
		var q clientapi.MetricQuantityResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &q) != nil || q.Agg != tc.agg || len(q.Points) != 1 || q.Points[0].Qty != tc.want {
			t.Fatalf("invalid %s projection: %+v", tc.agg, q)
		}
	}

	source := request(base + "&from=" + day + "&by_source=1")
	var split clientapi.MetricDataResponse
	if source.Code != 200 || json.Unmarshal(source.Body.Bytes(), &split) != nil || !split.BySource || len(split.PointsBySource) != 2 {
		t.Fatalf("source series lost: %+v", split)
	}

	for _, metric := range []string{"resting_heart_rate", "height", "active_energy", "sleep_total"} {
		r := request("/api/metrics/data?metric=" + metric + "&view=quantity&bucket=day&range=all&to=" + day)
		var series clientapi.MetricQuantityResponse
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &series) != nil || series.Points == nil {
			t.Fatalf("invalid all-time quantities for %s: HTTP %d", metric, r.Code)
		}
		if metric == "resting_heart_rate" && (len(series.Points) != 1 || series.Points[0].Qty != 61) {
			t.Fatalf("all-time series lost its first observation: %+v", series.Points)
		}
	}

	empty := request("/api/metrics/data?metric=step_count&bucket=day&view=quantity&range=all&to=2000-01-01")
	var gap clientapi.MetricQuantityResponse
	if json.Unmarshal(empty.Body.Bytes(), &gap) != nil || gap.Points == nil || len(gap.Points) != 0 {
		t.Fatal("expected empty list")
	}
	for _, invalid := range []string{"&view=quantity&range=all&from=" + day, "&view=quantity&by_source=1", "&view=quantity&by_source=true", "&range=all", "&view=invalid", "&view=quantity&agg=banana", "&view=quantity&agg=AVG"} {
		if code := request(base + invalid).Code; code != 400 {
			t.Errorf("invalid option %s: HTTP %d", invalid, code)
		}
	}
	// Hold the daily reader at its first SQL statement. The request deadline
	// must release the connection even while the fixture still holds the lock.
	admin, err := testdb.NewPool(t.Context(), testdb.DSN(t), schema)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	lock, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(t.Context(), "lock table hourly_metrics in access exclusive mode"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = db.MetricQuantitiesForContract(ctx, "raw_fallback_fixture", day, day, "day", "AVG", false)
	if err == nil || ctx.Err() == nil || time.Since(started) > 4*time.Second {
		t.Fatalf("request deadline must cancel a blocked chart query; error=%v", err)
	}
	if err := lock.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	// This disposable schema has no hourly history for the fixture metric.
	// A missing raw source must surface as a failure, never a valid empty list.
	if _, err = admin.Exec(t.Context(), "alter table metric_points rename to unavailable_fixture_points"); err != nil {
		t.Fatal(err)
	}
	failedRaw := request("/api/metrics/data?metric=raw_fallback_fixture&bucket=day&view=quantity&range=all&to=" + day)
	if failedRaw.Code != http.StatusInternalServerError {
		t.Fatal("failed raw history was published as a successful chart")
	}

	db.Close()
	failedSource := request(base + "&from=" + day + "&by_source=1")
	if failedSource.Code != http.StatusInternalServerError || !strings.Contains(failedSource.Body.String(), "could not read per-source metrics") {
		t.Fatal("source error did not terminate the v2 source request")
	}

}
