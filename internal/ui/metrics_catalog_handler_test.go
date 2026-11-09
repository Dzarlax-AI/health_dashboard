package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestMetricCatalogPreservesVariantsAndLocalizesIdentifiers(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	names := []string{"six_min_walk_distance", "six_minute_walking_test_distance", "walking_double_support", "walking_double_support_percentage", "walking_asymmetry", "walking_asymmetry_percentage", "headphone_audio", "headphone_audio_exposure", "active_energy"}
	var recordID int64
	if err := sharedTenantPool.QueryRow(context.Background(), `INSERT INTO health_records(processing_status) VALUES('complete') RETURNING id`).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := sharedTenantPool.Exec(context.Background(), `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count) VALUES($1,'2026-09-10 08:00','Synthetic',10,1)`, name); err != nil {
			t.Fatal(err)
		}
		if _, err := sharedTenantPool.Exec(context.Background(), `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source) VALUES($1,$2,'m','2026-09-10 08:00:00 +00:00',10,'Synthetic')`, recordID, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, lang := range []string{"en", "ru", "sr", "unexpected"} {
		t.Run(lang, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/metrics?lang="+lang+"&schema=untrusted", nil).WithContext(adminContext(db, schema))
			(&Handler{}).listMetrics(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d", rec.Code)
			}
			var items clientapi.MetricCatalogResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != len(names) {
				t.Fatalf("lost catalogue variants: %d", len(items))
			}
			if lang == "unexpected" {
				lang = "en"
			}
			for _, item := range items {
				if item.Units != "m" || item.UnitStatus != storage.MetricUnitKnown || item.Count != 1 || item.Min != "2026-09-10" || item.Max != "2026-09-10" {
					t.Errorf("changed metadata: %+v", item)
				}
				if item.Name == "active_energy" {
					if item.VariantLabel != "" {
						t.Error("unique metric must not show a technical identifier")
					}
				} else if item.VariantLabel != fmt.Sprintf(T(lang, "metric_variant_identifier"), item.Name) {
					t.Errorf("variant %s lacks truthful localized identifier", item.Name)
				}
			}
		})
	}
}

func TestMetricCatalogUsesOnlyRequestTenant(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()
	ctx := context.Background()
	otherSchema := testdb.SchemaName("catalog_other")
	if err := testdb.CreateSchema(ctx, sharedTenantPool, otherSchema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(ctx, sharedTenantPool, otherSchema)
	pool, err := testdb.NewPool(ctx, testdb.DSN(t), otherSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE hourly_metrics(metric_name text,hour text,source text,avg_val double precision); CREATE TABLE metric_points(metric_name text,units text); INSERT INTO hourly_metrics VALUES('other_private_metric','2026-09-10 08:00','Synthetic',1); INSERT INTO metric_points VALUES('other_private_metric','m')`); err != nil {
		t.Fatal(err)
	}
	otherDB := storage.NewFromPool(pool)
	for _, tc := range []struct {
		db     *storage.DB
		schema string
		want   int
	}{{db, schema, 0}, {otherDB, otherSchema, 1}} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/metrics?schema="+otherSchema, nil).WithContext(adminContext(tc.db, tc.schema))
		(&Handler{}).listMetrics(rec, req)
		var items clientapi.MetricCatalogResponse
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != tc.want {
			t.Fatalf("tenant catalogue rows=%d, want %d", len(items), tc.want)
		}
		if tc.want == 0 && strings.Contains(rec.Body.String(), "other_private_metric") {
			t.Fatal("cross-tenant catalogue leak")
		}
	}
}
