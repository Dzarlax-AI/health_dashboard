package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func TestInstalledProfileMetrics(t *testing.T) {
	phase := os.Getenv("PROFILE_SCREEN_PHASE")
	if phase == "" {
		t.Skip("requires the two-phase profile graph fixture")
	}
	ctx := t.Context()
	dsn, source := os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA")
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var marked bool
	err = conn.QueryRow(ctx, `select current_database()='health_profile_graph_fixture' and current_schema()=$1 and token=$2
	    from profile_graph_fixture_marker`, source, os.Getenv("PROFILE_GRAPH_FIXTURE_TOKEN")).Scan(&marked)
	if err != nil || !marked {
		t.Fatalf("unknown fixture: %v", err)
	}
	legacy, err := storage.NewWithSchema(ctx, dsn, source)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if phase == "before" {
		if err := legacy.PrepareMetricCatalog(ctx); err != nil {
			t.Fatal(err)
		}
	}
	graph, err := storage.NewLocalProfileGraphReader(ctx, dsn, source, os.Getenv("PROFILE_GRAPH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close()
	step := 17
	if source == "peer_source" {
		step = 71
	} else if phase == "after" {
		step = 19
	}
	h := &Handler{mgr: &tenants.Manager{}}
	for _, candidate := range []bool{false, true, false} {
		db, wantSteps, wantRHR, latestDay := legacy, 903, 112, "2026-01-02"
		if candidate {
			db, wantSteps, wantRHR, latestDay = graph, step, 64, "2026-01-15"
		}
		get := func(path string, fn http.HandlerFunc, v2 bool) []byte {
			t.Helper()
			r := httptest.NewRequest("GET", path, nil)
			r = r.WithContext(ctxdb.WithDB(r.Context(), db, source))
			if v2 {
				r.Header.Set(metriccontract.Header, "2")
			}
			r.SetPathValue("name", "step_count")
			w := httptest.NewRecorder()
			fn(w, r)
			if w.Code != 200 {
				t.Fatalf("%s: HTTP %d: %s", path, w.Code, w.Body.String())
			}
			return w.Body.Bytes()
		}
		decode := func(path string, fn http.HandlerFunc, value any) {
			t.Helper()
			if err := json.Unmarshal(get(path, fn, true), value); err != nil {
				t.Fatal(err)
			}
		}
		var catalog []struct {
			Name  string
			Count int
		}
		decode("/api/metrics", h.listMetrics, &catalog)
		found := false
		for _, m := range catalog {
			if m.Name == "step_count" && m.Count > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("step_count missing from catalog")
		}
		var latest []storage.LatestValue
		decode("/api/metrics/latest", h.latestMetricValues, &latest)
		values := map[string]storage.LatestValue{}
		for _, v := range latest {
			values[v.Metric] = v
		}
		if v := values["step_count"]; v.Value != float64(wantSteps) || v.Date != "2026-02-01" || v.Unit != "count" {
			t.Fatalf("latest steps %+v, want %d", v, wantSteps)
		}
		if v := values["resting_heart_rate"]; v.Value != float64(wantRHR) || v.Date != latestDay {
			t.Fatalf("latest RHR %+v, want %d on %s", v, wantRHR, latestDay)
		}
		var r api.MetricRangeResponse
		decode("/api/metrics/range?metric=step_count", h.metricRange, &r)
		if r.Min != "2026-01-01" || r.Max != "2026-02-01" {
			t.Fatalf("steps date range %+v", r)
		}
		var chart api.MetricDataResponse
		decode("/api/metrics/data?metric=resting_heart_rate&bucket=day&from="+latestDay+"&to="+latestDay, h.metricData, &chart)
		if len(chart.Points) != 1 || chart.Points[0].Qty != float64(wantRHR) {
			t.Fatalf("chart disagrees with latest RHR: %+v", chart.Points)
		}
		for _, mode := range []string{"1", "0"} {
			t.Setenv("HEALTH_METRICS_V1_ENABLED", mode)
			var detailPoints api.MetricDataResponse
			body := get("/api/metrics/data?metric=step_count&bucket=day&from=2026-02-01&to=2026-02-01", h.metricData, false)
			if err := json.Unmarshal(body, &detailPoints); err != nil || len(detailPoints.Points) != 1 || detailPoints.Points[0].Qty != float64(wantSteps) {
				t.Fatalf("browser detail, legacy enabled=%s: %s", mode, body)
			}
			list := string(get("/metrics?q=step_count", h.pageMetrics, false))
			if !strings.Contains(list, `href="/metrics/step_count"`) || !strings.Contains(list, `class="metrics-card-value">`+strconv.Itoa(wantSteps)+"</span>") {
				t.Fatalf("Metrics list and detail disagree with legacy enabled=%s, want %d", mode, wantSteps)
			}
		}
		if detail := string(get("/metrics/step_count", h.pageMetricDetail, false)); !strings.Contains(detail, `id="chart-view"`) || !strings.Contains(detail, "step_count") {
			t.Fatal("metric detail missing chart or metric key")
		}
	}
}
