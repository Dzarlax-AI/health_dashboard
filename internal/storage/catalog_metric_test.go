package storage

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCatalogMetricRequestBoundaries(t *testing.T) {
	ctx := context.Background()
	// A regular application connection must not be able to choose a graph.
	if _, err := (&DB{}).ReadCatalogMetricPage(ctx, CatalogMetricRequest{MetricID: "daily.steps", Limit: 10}); err == nil {
		t.Fatal("unscoped catalog read")
	}
	for _, request := range []CatalogMetricRequest{
		{MetricID: "daily.steps", Limit: 1, DateField: "source"}, {MetricID: "daily.steps"}, {MetricID: "daily.steps", Limit: 257},
		{MetricID: "daily.steps", Limit: 1, SampleKey: json.RawMessage("null")},
		{MetricID: "daily.steps", Limit: 1, SampleKey: json.RawMessage("[]")},
		{MetricID: "daily.steps", Limit: 1, After: &CatalogMetricCursor{}},
	} {
		if _, err := (&DB{profileGraphSchema: "disposable"}).ReadCatalogMetricPage(ctx, request); err == nil {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
}

func TestInstalledCatalogValuePage(t *testing.T) {
	graph := os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" {
		t.Skip("requires the isolated dbt profile build")
	}
	db, err := NewLocalProfileGraphReader(t.Context(), os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"), graph)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var jit string
	if err := db.pool.QueryRow(t.Context(), "show jit").Scan(&jit); err != nil || jit != "off" {
		t.Fatalf("bounded catalog reader JIT setting = %q, err = %v", jit, err)
	}
	read := func(req CatalogMetricRequest) CatalogMetricPage {
		t.Helper()
		page, err := db.ReadCatalogMetricPage(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	req := CatalogMetricRequest{MetricID: "daily.steps", Source: "daily_metrics", Limit: 2}
	first := read(req)
	if len(first.Samples) != 2 || first.Next == nil {
		t.Fatal("expected multiple daily steps pages")
	}
	req.After = first.Next
	second := read(req)
	if len(second.Samples) != 2 || string(second.Samples[0].SampleKey) == string(first.Next.SampleKey) {
		t.Fatal("unstable continuation")
	}
	req.After = nil
	req.SampleKey = json.RawMessage(`{"date":"2026-01-01"}`)
	points := read(req).Samples
	if len(points) != 1 {
		t.Fatalf("expected one dated value, got %d", len(points))
	}
	sample := points[0]
	if sample.Status != "available" || sample.Source == nil || *sample.Source != "daily_metrics" ||
		sample.Number == nil || sample.Boolean != nil || sample.Text != nil || sample.SourceMetadata == nil ||
		sample.Binding == nil || sample.Definition == nil {
		t.Fatal("dated measurement lost typed value or provenance")
	}
	var actual json.Number
	if err = json.Unmarshal(sample.Value, &actual); err != nil || actual != *sample.Number {
		t.Fatal("numeric value differs from JSON contract")
	}
	dated := CatalogMetricRequest{MetricID: "readiness.readiness_raw_score", Source: "semantic_readiness_score_v1", DateField: "target_date", FromDate: "2026-11-22", ToDate: "2026-11-22", Limit: 1}
	if got := read(dated); len(got.Samples) != 1 {
		t.Fatal("native target_date filter lost Readiness")
	}
	dated.FromDate, dated.ToDate = "2030-01-01", "2030-01-01"
	if got := read(dated); len(got.Samples) != 0 {
		t.Fatal("native target_date filter leaked dates")
	}
	// Half an hour into the producer's January 2 is still January 1 UTC.
	// Preserve this exact original date regardless of the PostgreSQL timezone.
	tx, err := db.pool.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	_, err = tx.Exec(t.Context(), `create temp view metric_read_v1 as select metric_id,value_status,disposition,reason,
        definition,dbt_binding,read_sources,source,jsonb_build_object('original_date','2026-01-02 00:30:00 +1400')as sample_key,
        source_metadata,original_observation_time,available_at,source_version,value,numeric_value,boolean_value,text_value
        from `+db.profileGraphRelation("metric_read_v1")+` where metric_id='daily.steps' limit 1`)
	if err != nil {
		t.Fatal(err)
	}
	local := &DB{pool: &cacheTransaction{Tx: tx, ctx: t.Context()}, profileGraphSchema: "pg_temp"}
	for date, want := range map[string]int{"2026-01-02": 1, "2026-01-01": 0} {
		page, err := local.ReadCatalogMetricPage(t.Context(), CatalogMetricRequest{MetricID: "daily.steps", DateField: "original_date", FromDate: date, ToDate: date, Limit: 1})
		if err != nil || len(page.Samples) != want {
			t.Fatalf("source-local calendar expected %d samples: %d, %v", want, len(page.Samples), err)
		}
	}
	dormant := read(CatalogMetricRequest{MetricID: "athletic_readiness.target_wo_residual", Limit: 1}).Samples
	if len(dormant) != 1 || dormant[0].Status != "dormant" || dormant[0].Reason == nil || dormant[0].Number != nil {
		t.Fatal("dormant definition lost its disposition")
	}
	if got := read(CatalogMetricRequest{MetricID: "unknown.synthetic.test", Limit: 1}); len(got.Samples) != 0 {
		t.Fatal("unexpected sample for unknown metric")
	}
}

func TestInstalledCatalogMetricCoverage(t *testing.T) {
	graph := os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" {
		t.Skip("requires the isolated dbt profile build")
	}
	db, err := NewLocalProfileGraphReader(t.Context(), os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"), graph)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.pool.Query(t.Context(), "select metric_id from "+db.profileGraphRelation("semantic_metric_catalog_v1")+" order by metric_id")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		t.Fatalf("catalog IDs: %d, %v", len(ids), err)
	}
	statuses := make(map[string]int)
	for _, id := range ids {
		page, err := db.ReadCatalogMetricPage(t.Context(), CatalogMetricRequest{MetricID: id, Limit: 1})
		if err != nil || len(page.Samples) != 1 {
			t.Fatalf("%s: expected one value or disposition, error %v", id, err)
		}
		row := page.Samples[0]
		if row.MetricID != id || row.Status == "" || row.Disposition == "" || len(row.Definition) == 0 {
			t.Fatalf("%s: incomplete definition or disposition", id)
		}
		statuses[row.Status]++
	}
	t.Logf("%d catalog IDs read; first-sample statuses %v", len(ids), statuses)
}
