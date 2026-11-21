package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
)

func TestMetricCatalogHTTPStatuses(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx := t.Context()
	admin, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := testdb.SchemaName("metric_catalog_http")
	if err := testdb.CreateSchema(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(ctx, admin, schema)
	db, err := storage.NewWithSchema(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := tenants.New(nil, dsn)
	if err := mgr.SetLegacyMode(db, "catalog-fixture", ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(mgr, nil, false).Register(mux)
	t.Setenv("HEALTH_METRICS_V1_ENABLED", "1")

	request := func(path, version string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-API-Key", "catalog-fixture")
		if version != "" {
			r.Header.Set(metriccontract.Header, version)
		}
		mux.ServeHTTP(w, r)
		return w
	}
	checkHTML := func(translationKey, unwantedKey string) {
		t.Helper()
		for _, path := range []string{"/metrics", "/fragments/metrics-list"} {
			html := request(path+"?lang=ru&q=Walk", "")
			body := html.Body.String()
			if html.Code != http.StatusOK || !strings.Contains(body, T("ru", translationKey)) ||
				strings.Contains(body, T("ru", unwantedKey)) {
				t.Errorf("%s: status=%d, expected state %s and no %s", path, html.Code, translationKey, unwantedKey)
			}
			if translationKey != "no_metrics_found" && !strings.Contains(body, "/metrics?lang=ru&amp;q=Walk") {
				t.Errorf("%s: retry URL should retain the query and language", path)
			}
		}
	}

	pending := request("/api/metrics?lang=ru", "")
	if pending.Code != http.StatusServiceUnavailable || pending.Header().Get("Retry-After") == "" ||
		pending.Header().Get(metriccontract.Header) != "1" {
		t.Fatalf("pending catalog status=%d", pending.Code)
	}
	checkHTML("metrics_catalog_preparing", "no_metrics_found")
	if err := db.SeedEmptyMetricCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	ready := request("/api/metrics?lang=ru", "2")
	if ready.Code != http.StatusOK || ready.Header().Get(metriccontract.Header) != "2" ||
		ready.Header().Get("Retry-After") != "" || strings.TrimSpace(ready.Body.String()) != "[]" {
		t.Fatalf("empty catalog status=%d", ready.Code)
	}
	checkHTML("no_metrics_found", "metrics_catalog_preparing")

	id, err := db.InsertRaw(storage.Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPoints(id, []storage.MetricPoint{{
		MetricName: "six_minute_walking_test_distance", Units: "m", Date: "2026-08-01 10:00:00 +0000", Source: "Fixture", Qty: 500,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PrepareMetricCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/metrics", "/fragments/metrics-list"} {
		html := request(path+"?lang=ru&q=Walk", "")
		if html.Code != http.StatusOK || !strings.Contains(html.Body.String(), `href="/metrics/six_minute_walking_test_distance"`) ||
			strings.Contains(html.Body.String(), T("ru", "metrics_catalog_preparing")) {
			t.Errorf("%s: expected the published metric after retry, status=%d", path, html.Code)
		}
	}
	db.Close()
	outage := request("/api/metrics?lang=ru", "2")
	if outage.Code != http.StatusInternalServerError || outage.Header().Get("Retry-After") != "" ||
		strings.TrimSpace(outage.Body.String()) != "metric catalog unavailable" {
		t.Fatalf("database outage status=%d", outage.Code)
	}
	checkHTML("metrics_catalog_unavailable", "no_metrics_found")
}
