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

	request := func(version string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/metrics?lang=ru", nil)
		r.Header.Set("X-API-Key", "catalog-fixture")
		if version != "" {
			r.Header.Set(metriccontract.Header, version)
		}
		mux.ServeHTTP(w, r)
		return w
	}
	pending := request("")
	if pending.Code != http.StatusServiceUnavailable || pending.Header().Get("Retry-After") == "" ||
		pending.Header().Get(metriccontract.Header) != "1" {
		t.Fatalf("pending catalog status=%d", pending.Code)
	}
	if err := db.SeedEmptyMetricCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	ready := request("2")
	if ready.Code != http.StatusOK || ready.Header().Get(metriccontract.Header) != "2" ||
		ready.Header().Get("Retry-After") != "" || strings.TrimSpace(ready.Body.String()) != "[]" {
		t.Fatalf("empty catalog status=%d", ready.Code)
	}
	db.Close()
	outage := request("2")
	if outage.Code != http.StatusInternalServerError || outage.Header().Get("Retry-After") != "" ||
		strings.TrimSpace(outage.Body.String()) != "metric catalog unavailable" {
		t.Fatalf("database outage status=%d", outage.Code)
	}
}
