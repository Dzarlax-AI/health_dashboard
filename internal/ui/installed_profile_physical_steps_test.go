package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"health-receiver/internal/api"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func TestInstalledProfilePhysicalSteps(t *testing.T) {
	if os.Getenv("PROFILE_SCREEN_SUITE") != "physical_steps" {
		t.Skip("requires the physical-step phase of the two-profile build")
	}
	source := os.Getenv("PROFILE_SOURCE_SCHEMA")
	db, err := storage.NewLocalProfileGraphReader(t.Context(), os.Getenv("DATABASE_URL"), source, os.Getenv("PROFILE_GRAPH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := &tenants.Manager{}
	const key = "physical-steps-test"
	if err := mgr.SetLegacyMode(db, key, ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(mgr, nil, false).Register(mux)
	t.Setenv("HEALTH_METRICS_V1_ENABLED", "1")
	fold := 110
	if source == "profile_source" && os.Getenv("PROFILE_SCREEN_PHASE") == "after" {
		fold = 114
	}
	for _, c := range []struct {
		day   string
		total int
	}{{"2024-10-27", fold}, {"2024-10-28", 0}} {
		rows, err := db.QueryReadOnly("SELECT steps=$2::float8 AS expected FROM daily_scores WHERE date=$1", c.day, c.total)
		if err != nil || len(rows) != 1 || rows[0]["expected"] != true {
			t.Fatalf("daily_scores %s want %d: %v, %v", c.day, c.total, rows, err)
		}
		for _, version := range []string{"1", "2"} {
			for _, bucket := range []string{"hour", "day"} {
				req := httptest.NewRequest("GET", "/api/metrics/data?metric=step_count&agg=SUM&bucket="+bucket+"&from="+c.day+"&to="+c.day, nil)
				req.Header.Set("X-API-Key", key)
				req.Header.Set(metriccontract.Header, version)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				var body api.MetricDataResponse
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Points) != 1 || body.Points[0].Qty != float64(c.total) {
					t.Fatalf("date %s bucket %s v%s want %d: HTTP %d %s", c.day, bucket, version, c.total, w.Code, w.Body.String())
				}
			}
		}
	}
}
