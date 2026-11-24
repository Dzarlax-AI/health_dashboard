package ui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/testdb"
)

func TestCatalogSeriesHTTP(t *testing.T) {
	_, mux, schema := metricHTTPFixture(t)
	p, err := testdb.NewPool(t.Context(), testdb.DSN(t), schema)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	const base = "/api/metrics/data?view=catalog&metric=test"
	get := func(path string, code int) clientapi.CatalogSeriesResponse {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-API-Key", "catalog-fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("status %d, want %d: %s", w.Code, code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), schema) || strings.Contains(w.Body.String(), "private-id") {
			t.Fatal("private producer key leaked")
		}
		var out clientapi.CatalogSeriesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	get(base, 503)
	for _, suffix := range []string{"&schema=other", "&limit=0", "&cursor=abc", "&date_field=invalid", "&bucket=day", "&from=bad", "&metric=other"} {
		get(base+suffix, 400)
	}
	_, err = p.Exec(t.Context(), `insert into settings(key,value) values ('dbt_native_catalog_reads_v1','true') on conflict(key) do update set value='true';
	create view tenant_native_metric_read_v1 with(security_invoker=true) as
	select metric_id, status as value_status,'active'::text disposition,null::text reason,'{}'::jsonb definition,'{}'::jsonb dbt_binding,'[]'::jsonb read_sources,
	current_schema()||'.metric_points' as source,jsonb_build_object('date',day,'profile','private-id') as sample_key,'{}'::jsonb source_metadata,
	null::text original_observation_time,null::text available_at,null::text source_version,val as value,case when jsonb_typeof(val)='number' then val::text::numeric end numeric_value,case when jsonb_typeof(val)='boolean' then val::text::boolean end boolean_value,null::text text_value
	from (values ('test','available','2026-01-01','0'::jsonb),('test','available','2026-01-02','false'::jsonb),('absent','missing',null,null::jsonb)) v(metric_id,status,day,val)`)
	if err != nil {
		t.Fatal(err)
	}
	first := get(base+"&limit=1", 200)
	if len(first.Samples) != 1 || string(first.Samples[0].Value) != "0" || first.Samples[0].Source != "metric_points" || first.NextCursor == "" || first.Samples[0].NumericValue == nil || *first.Samples[0].NumericValue != "0" {
		t.Fatal("first page did not preserve zero and public provenance")
	}
	second := get(base+"&limit=1&cursor="+first.NextCursor, 200)
	if len(second.Samples) != 1 || string(second.Samples[0].Value) != "false" || second.NextCursor != "" || second.Samples[0].BooleanValue == nil || *second.Samples[0].BooleanValue {
		t.Fatal("continuation did not preserve false")
	}
	get(base+"&from=2026-01-01&cursor="+first.NextCursor, 400)
	empty := get(base+"&from=2026-01-03", 200)
	if empty.Samples == nil || len(empty.Samples) != 0 {
		t.Fatal("filtered lookup should return an empty array")
	}
	missing := get(strings.Replace(base, "metric=test", "metric=absent", 1), 200)
	if len(missing.Samples) != 1 || missing.Samples[0].Status != "missing" || missing.Samples[0].Value != nil {
		t.Fatal("missing value did not retain status")
	}
}
