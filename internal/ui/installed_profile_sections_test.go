package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"health-receiver/internal/api"
	"health-receiver/internal/health"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// Exercise the section, briefing, chart and historical endpoints selected by
// health-detail/loader.ts. The harness repeats the same date after an incremental
// refresh, with independent values for the other profile and both legacy caches.
func TestInstalledProfileSections(t *testing.T) {
	phase := os.Getenv("PROFILE_SCREEN_PHASE")
	if phase == "" {
		t.Skip("run the phased installed profile graph probe")
	}
	if phase != "before" && phase != "after" {
		t.Fatalf("unknown section probe phase %q", phase)
	}
	source, graph := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	var step int
	switch source {
	case "profile_source":
		step = 17
		if phase == "after" {
			step = 19
		}
	case "peer_source":
		step = 71
	default:
		t.Fatalf("unexpected fixture schema %q", source)
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
	mgr := &tenants.Manager{}
	mux := http.NewServeMux()
	New(mgr, nil, false).Register(mux)
	get := func(path, version string, dst any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", "synthetic-sections-key")
		if version != "" {
			req.Header.Set(metriccontract.Header, version)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), dst) != nil {
			t.Fatalf("%s: HTTP %d: %s", path, w.Code, w.Body.String())
		}
	}
	t.Setenv("HEALTH_METRICS_V1_ENABLED", "1")
	for _, reader := range []struct {
		db              *storage.DB
		steps, vo2, hrv int
	}{{legacy, 903, 33, 84}, {candidate, step, 46, 52}, {legacy, 903, 33, 84}} {
		if err := mgr.SetLegacyMode(reader.db, "synthetic-sections-key", ""); err != nil {
			t.Fatal(err)
		}
		var br health.BriefingResponse
		get("/api/health-briefing?lang=en", "", &br)
		if br.Date != "2026-02-02" {
			t.Fatalf("briefing date %q", br.Date)
		}
		for _, c := range []struct {
			section, label, metric, detail, agg string
			chart                               int
		}{
			{"activity", "Steps", "step_count", fmt.Sprintf("%d/day", reader.steps), "SUM", reader.steps},
			{"cardio", "VO2 Max", "vo2_max", fmt.Sprintf("%d.0 ml/kg/min", reader.vo2), "AVG", reader.vo2},
			{"recovery", "HRV", "heart_rate_variability", fmt.Sprintf("%d ms", reader.hrv), "AVG", reader.hrv},
		} {
			var sect api.SectionResponse
			get("/api/section/"+c.section+"?lang=en", "", &sect)
			if sect.Key != c.section || sect.Summary == "" || !slices.ContainsFunc(sect.Details, func(d api.SectionDetail) bool { return d.Label == c.label && d.Value == c.detail }) || !slices.ContainsFunc(sect.Charts, func(ch api.SectionChart) bool { return ch.Metric == c.metric && ch.Agg == c.agg }) {
				t.Fatalf("%s: summary %q, details %+v, want %s %q and chart %s", c.section, sect.Summary, sect.Details, c.label, c.detail, c.metric)
			}
			if !slices.ContainsFunc(br.Sections, func(s health.BriefingSection) bool {
				return s.Key == sect.Key && s.Summary == sect.Summary && slices.ContainsFunc(s.Details, func(d health.BriefingDetail) bool { return d.Label == c.label && d.Value == c.detail })
			}) {
				t.Fatalf("briefing and %s screen disagree", c.section)
			}
			var visible api.MetricDataResponse
			get("/api/metrics/data?from=2026-02-01&to=2026-02-02&bucket=day&agg="+c.agg+"&metric="+c.metric, "", &visible)
			if len(visible.Points) != 1 || visible.Points[0].Date != "2026-02-01" || visible.Points[0].Qty != float64(c.chart) {
				t.Fatalf("%s chart: %+v, want %d", c.section, visible.Points, c.chart)
			}
			if c.section == "recovery" && !slices.ContainsFunc(sect.Charts, func(ch api.SectionChart) bool { return ch.Virtual }) {
				t.Fatal("missing readiness history chart")
			}
		}
		var history api.ReadinessHistoryResponse
		get("/api/readiness-history?days=365", "", &history)
		if !slices.ContainsFunc(history.Points, func(p health.ReadinessPoint) bool { return p.Date == "2026-02-01" && p.Score == 61 && p.Band != "" }) {
			t.Fatalf("recorded February 1 readiness missing: %+v", history.Points)
		}
		if reader.db == candidate {
			actual, err := candidate.QueryReadOnly(`SELECT steps=135 AS preferred_hour_steps FROM daily_scores WHERE date=$1`, "2026-01-01")
			if err != nil || len(actual) != 1 || actual[0]["preferred_hour_steps"] != true {
				t.Fatalf("January 1 preferred-source daily rollup: %+v, %v", actual, err)
			}
		}
		// Both contracts retain the active cache when source evidence is incomplete.
		// On Jan 1 the Watch wins the contested hour,
		// and the iPhone contributes its evening hour.
		for _, ch := range []struct {
			date string
			qty  int
		}{{"2026-02-01", reader.steps}, {"2026-01-01", 135}} {
			var points api.MetricDataResponse
			get("/api/metrics/data?metric=step_count&bucket=day&agg=SUM&from="+ch.date+"&to="+ch.date, "2", &points)
			if len(points.Points) != 1 || points.Points[0].Qty != float64(ch.qty) {
				t.Fatalf("v2 combined steps %s: %+v, want %d", ch.date, points.Points, ch.qty)
			}
		}
		const janSteps = "/api/metrics/data?metric=step_count&agg=SUM&from=2026-01-01&to=2026-01-01"
		var hourly, sources api.MetricDataResponse
		get(janSteps+"&bucket=hour", "2", &hourly)
		for hour, want := range map[string]float64{"2026-01-01 10:00": 35, "2026-01-01 18:00": 100} {
			if !slices.ContainsFunc(hourly.Points, func(p storage.DataPoint) bool { return p.Date == hour && p.Qty == want }) {
				t.Fatalf("v2 selected hour %s absent: %+v", hour, hourly.Points)
			}
		}
		get(janSteps+"&bucket=day&by_source=1", "2", &sources)
		for source, want := range map[string]float64{"Apple Watch": 35, "iPhone": 200, "Other": 1000} {
			if !slices.ContainsFunc(sources.PointsBySource, func(s storage.SourceDataPoints) bool {
				return s.Source == source && len(s.Points) == 1 && s.Points[0].Qty == want
			}) {
				t.Fatalf("v2 source %s absent: %+v", source, sources.PointsBySource)
			}
		}

	}
}
