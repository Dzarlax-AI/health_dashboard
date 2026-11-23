package ui

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"health-receiver/internal/api"
	"health-receiver/internal/health"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// The graph harness calls this probe twice on the same two marked profiles.
// Report history is created by TestInstalledProfileSleepReader before the phases.
func TestInstalledProfileSleepReports(t *testing.T) {
	phase := os.Getenv("PROFILE_SCREEN_PHASE")
	if phase == "" {
		t.Skip("requires the two-phase disposable profile graph")
	}
	if phase != "before" && phase != "after" {
		t.Fatal("unknown fixture phase")
	}
	ctx := t.Context()
	dsn, schema, graph := os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var marked bool
	token := os.Getenv("PROFILE_GRAPH_FIXTURE_TOKEN")
	if err := conn.QueryRow(ctx, `select current_database()='health_profile_graph_fixture'
		and current_schema()=$1 and token=$2 from profile_graph_fixture_marker`, schema, token).Scan(&marked); err != nil || !marked || token == "" {
		t.Fatalf("invalid disposable fixture: %v", err)
	}
	primary := schema == "profile_source" && graph == "profile_graph"
	if !primary && (schema != "peer_source" || graph != "peer_graph") {
		t.Fatal("unexpected profile schemas")
	}
	legacy, err := storage.NewWithSchema(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	candidate, err := storage.NewLocalProfileGraphReader(ctx, dsn, schema, graph)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()

	const day = "1981-03-15"
	goalHours, source := 8.0, "Peer Graph Watch"
	if primary {
		goalHours, source = 7.75, "Graph Watch"
	}
	wakeAt := time.Date(2026, 2, 1, 7, 0, 0, 0, time.UTC)
	if !primary {
		wakeAt = wakeAt.Add(time.Hour)
	}
	if phase == "before" {
		if err := legacy.EnsureDerivedMetricsTablesContext(ctx); err != nil {
			t.Fatal(err)
		}
		if err := legacy.SaveDerivedMetric(storage.DerivedMetric{MetricName: storage.DerivedMetricWakeTime,
			MetricDate: "2026-02-01", ValueType: storage.DerivedValueTimestamp, Unit: "timestamp", ValueTimestamp: &wakeAt,
			State: storage.DerivedMetricStateFinal, FinalizedAt: &wakeAt, CalculatedAt: wakeAt,
			FormulaVersion: "wake-screen-fixture-v1", InputsHash: "wake-screen-fixture"}); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `create table if not exists health_records(id bigint primary key);
			create table screen_report_witness(id uuid primary key, body_digest bytea not null)`); err != nil {
			t.Fatal(err)
		}
		if err := legacy.EnsureSleepDurationBalanceTablesContext(ctx); err != nil {
			t.Fatal(err)
		}
		goal := health.SleepGoal{EffectiveDate: "1981-03-02", Hours: goalHours, Version: "manual-goal-v1"}
		if err := legacy.SaveSleepGoal(ctx, goal, time.Now()); err != nil {
			t.Fatal(err)
		}
		// Mirror the canonical covered periods and their selected episodes.
		// The display reader must respond to a later episode correction independently of the saved report.
		if _, err := conn.Exec(ctx, `insert into sleep_period_coverage(wake_date,source_epoch,capture_completeness,coverage_generation,
			covered_interval_start,covered_interval_end,observed_at,input_hash)
			select d::date::text,'screen','complete','one',d-interval '12 hours',d+interval '12 hours',d+interval '12 hours',d::date::text
			from generate_series('1981-03-02'::timestamptz,'1981-03-15'::timestamptz,'1 day') d`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `insert into completed_sleep_episode(episode_id,wake_date,start_at,end_at,
			source,source_epoch,input_hash,coverage_generation,capture_state,duration_assessment,observed_at)
			select 'screen-'||wake_date,wake_date,covered_interval_end-interval '10 hours',covered_interval_end-interval '2.5 hours',
				$1,'screen',input_hash,'one','complete','plausible',observed_at from sleep_period_coverage
			where wake_date between '1981-03-02' and '1981-03-15'`, source); err != nil {
			t.Fatal(err)
		}
	}
	muxFor := func(db *storage.DB) *http.ServeMux {
		t.Helper()
		mgr := &tenants.Manager{}
		if err := mgr.SetLegacyMode(db, "fixture-key", ""); err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		New(mgr, nil, false).Register(mux)
		return mux
	}
	get := func(mux *http.ServeMux, path string, into any) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", "fixture-key")
		req.Header.Set(metriccontract.Header, "2")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	live := muxFor(candidate)
	original := muxFor(legacy)
	var goal api.SleepGoalResponse
	get(live, "/api/sleep/goal?date="+day, &goal)
	if goal.Goal == nil || goal.Goal.GoalHours != goalHours || goal.Goal.EffectiveDate != "1981-03-02" || goal.Goal.Version != "manual-goal-v1" {
		t.Fatalf("effective goal: %+v", goal)
	}
	snapshotHash := func() string {
		t.Helper()
		var hash string
		if err := conn.QueryRow(ctx, `select coalesce((select input_hash from sleep_duration_balance_snapshot where wake_date=$1),'')`, day).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	was := snapshotHash()
	var balance, reference api.SleepDurationBalanceResponse
	get(live, "/api/sleep/balance?date="+day, &balance)
	wantBalance := 14 * (7.5 - goalHours)
	wantDelta := -42
	core, total := 3.5, 7.0
	if phase == "after" && primary {
		wantBalance += 1.5
		wantDelta = -55
		core, total = 5, 8.5
		wakeAt = wakeAt.Add(30 * time.Minute)
	}
	if balance.BalanceHours == nil || *balance.BalanceHours != wantBalance || balance.State != "complete" ||
		len(balance.Periods) != 14 || snapshotHash() != was {
		t.Fatalf("read-only balance %+v; expected %v hours", balance, wantBalance)
	}
	get(original, "/api/sleep/balance?date="+day, &reference)
	if !reflect.DeepEqual(balance, reference) || snapshotHash() == "" || (phase == "after" && primary && snapshotHash() == was) {
		t.Fatal("ordinary balance GET did not persist its matching snapshot")
	}
	var missing api.SleepDurationBalanceResponse
	get(live, "/api/sleep/balance?date=1981-03-01", &missing)
	if missing.State != "incomplete" || missing.BalanceHours != nil || missing.IncompleteReason != "coverage_missing" {
		t.Fatalf("uncovered balance: %+v", missing)
	}
	var span api.MetricRangeResponse
	get(live, "/api/metrics/range?metric=sleep_total", &span)
	if span.Min == "" || span.Min > "2026-02-01" || span.Max < "2026-02-01" {
		t.Fatalf("chart range excluded February 1: %+v", span)
	}
	var wake api.DerivedMetricsResponse
	get(live, "/api/derived-metrics?metric=wake_time&from=2026-02-01&to=2026-02-01", &wake)
	if len(wake.Values) != 1 || wake.Values[0].ValueTimestamp == nil || !wake.Values[0].ValueTimestamp.Equal(wakeAt) ||
		wake.Values[0].MetricDate != "2026-02-01" || wake.Values[0].State != storage.DerivedMetricStateFinal {
		t.Fatalf("recorded wake time: %+v", wake)
	}
	for metric, want := range map[string]float64{"sleep_total": total, "sleep_core": core, "sleep_deep": 1, "sleep_rem": 2, "sleep_unspecified": 0.5, "sleep_awake": 0.25} {
		var chart api.MetricDataResponse
		get(live, "/api/metrics/data?from=2026-02-01&to=2026-02-01&bucket=day&agg=AVG&by_source=0&metric="+metric, &chart)
		if chart.Metric != metric || chart.Bucket != "day" || len(chart.Points) != 1 || chart.Points[0].Date != "2026-02-01" || chart.Points[0].Qty != want {
			t.Fatalf("daily chart %s: %+v, expected %v", metric, chart, want)
		}
	}
	// Give the ordinary hourly cache a different answer at this date to prove
	// which relation serves the chart before and after switching back.
	if _, err := conn.Exec(ctx, `insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
		values('sleep_total','2026-02-01 06:00','Screen Probe Watch',6,6,6,1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, `delete from hourly_metrics where metric_name='sleep_total' and hour='2026-02-01 06:00' and source='Screen Probe Watch'`); err != nil {
			t.Error(err)
		}
	}()
	for _, arm := range []struct {
		mux   *http.ServeMux
		total float64
	}{{original, 6}, {live, total}, {original, 6}} {
		var chart api.MetricDataResponse
		get(arm.mux, "/api/metrics/data?metric=sleep_total&bucket=day&from=2026-02-01&to=2026-02-01", &chart)
		if len(chart.Points) != 1 || chart.Points[0].Qty != arm.total {
			t.Fatalf("chart rollback: %+v; expected %v hours", chart, arm.total)
		}
	}
	var comparison api.SleepDurationComparisonResponse
	get(live, "/api/sleep/comparison?date="+day, &comparison)
	if f := comparison.Comparison; f == nil || f.Source != source || f.DeltaMinutes == nil || *f.DeltaMinutes != wantDelta {
		t.Fatalf("live comparison: %+v", f)
	}
	var history api.ReportHistoryResponse
	get(live, "/api/reports/history?from="+day+"&to="+day+"&kind=morning", &history)
	if len(history.Items) != 1 || history.Items[0].Status != "sent" || !history.Items[0].ContentAvailable {
		t.Fatalf("missing sent report: %+v", history)
	}
	id := history.Items[0].ID
	var detail storage.ReportHistoryDetail
	data := get(live, "/api/reports/history/"+id.String(), &detail)
	var archived health.MorningReportSleep
	if err := json.Unmarshal(detail.Context, &archived); err != nil || detail.ID != id || detail.LocalDate != day ||
		archived.Comparison == nil || archived.Comparison.Source != source || archived.Comparison.DeltaMinutes == nil ||
		*archived.Comparison.DeltaMinutes != -51 || !strings.Contains(detail.PlainBody, "-51 min") ||
		len(detail.Attempts) != 1 || detail.Attempts[0].Status != "sent" || detail.Attempts[0].Body != detail.RichBody {
		t.Fatalf("archived explanation or delivery attempt differs: %+v", detail)
	}
	digest := sha256.Sum256(data)
	if phase == "before" {
		if _, err := conn.Exec(ctx, `insert into screen_report_witness(id,body_digest) values($1,$2)`, id, digest[:]); err != nil {
			t.Fatal(err)
		}
	} else {
		var identical bool
		if err := conn.QueryRow(ctx, `select body_digest=$2 from screen_report_witness where id=$1`, id, digest[:]).Scan(&identical); err != nil || !identical {
			t.Fatalf("archived report ID or HTTP bytes changed after source correction: %v", err)
		}
	}
}
