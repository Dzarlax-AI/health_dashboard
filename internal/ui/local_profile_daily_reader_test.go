package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func TestInstalledProfileDailyChart(t *testing.T) {
	source, graph := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" {
		t.Skip("requires the isolated full-profile dbt build")
	}
	dsn := os.Getenv("DATABASE_URL")
	legacy, err := storage.NewWithSchema(t.Context(), dsn, source)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	candidate, err := storage.NewLocalProfileGraphReader(t.Context(), dsn, source, graph)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	handler := &Handler{}
	for _, route := range []struct {
		db   *storage.DB
		want float64
	}{{legacy, 111}, {candidate, 60}, {legacy, 111}} {
		for _, bySource := range []bool{false, true} {
			path := "/api/metrics/data?metric=resting_heart_rate&from=2026-01-01&to=2026-01-01&bucket=day"
			if bySource {
				path += "&by_source=1"
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set(metriccontract.Header, "2")
			req = req.WithContext(ctxdb.WithDB(req.Context(), route.db, source))
			w := httptest.NewRecorder()
			handler.metricData(w, req)
			var payload clientapi.MetricDataResponse
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != http.StatusOK || payload.BySource != bySource {
				t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
			}
			points := payload.Points
			if bySource {
				if len(payload.PointsBySource) != 1 || payload.PointsBySource[0].Source != "Apple Watch" {
					t.Fatalf("%s: sources %+v", path, payload.PointsBySource)
				}
				points = payload.PointsBySource[0].Points
			}
			if len(points) != 1 || points[0].Date != "2026-01-01" || points[0].Qty != route.want {
				t.Fatalf("%s: points %+v, want %v", path, points, route.want)
			}
		}
	}
}

func TestInstalledProfileCatalogStepsChart(t *testing.T) {
	source, graph := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" {
		t.Skip("requires the isolated full-profile dbt build")
	}
	candidate, err := storage.NewLocalProfileGraphReader(t.Context(), os.Getenv("DATABASE_URL"), source, graph)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	const key = "local-catalog-synthetic-key"
	mgr := &tenants.Manager{}
	if err := mgr.SetLegacyMode(candidate, key, ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(mgr, nil, false).Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/fragments/step-daily-series?to=2026-01-01", nil)
	req.Header.Set("X-API-Key", key)
	req.Header.Set(metriccontract.Header, "2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var payload struct {
		Bucket string                   `json:"bucket"`
		Points []storage.DailyStepPoint `json:"points"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &payload) != nil || payload.Bucket != "day" {
		t.Fatalf("steps chart HTTP %d", rec.Code)
	}
	want, err := candidate.QueryReadOnly("SELECT date, steps::float8 AS qty FROM daily_scores WHERE date<=$1 AND steps IS NOT NULL ORDER BY date", "2026-01-01")
	if err != nil || len(want) == 0 || len(want) != len(payload.Points) {
		t.Fatalf("daily series: chart %d, reference %d: %v", len(payload.Points), len(want), err)
	}
	for i, point := range payload.Points {
		if want[i]["date"] != point.Date || want[i]["qty"] != point.Qty {
			t.Fatalf("synthetic steps mismatch at row %d", i)
		}
	}
}
