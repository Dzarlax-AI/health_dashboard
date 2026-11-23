// Package servingarchive exercises the real application and its restricted tenant connection on a disposable local restore.
package servingarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/metriccontract"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/ui"
)

// Retain only which data query executed. Recorded SQL and bind values stay private.
type chartRoute struct{ shadow atomic.Bool }

func (r *chartRoute) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(strings.ToLower(q.SQL), "from hdbt_daily_metrics") {
		r.shadow.Store(true)
	}
	return ctx
}
func (r *chartRoute) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestServingArchive(t *testing.T) {
	phase := os.Getenv("SERVING_ARCHIVE_PHASE")
	if phase == "" {
		t.Skip("requires disposable archive")
	}
	scratch := os.Getenv("SERVING_ARCHIVE_SCRATCH")
	secret, err := os.ReadFile(os.Getenv("HEALTH_DBT_TENANT_DSN_FILE"))
	if err != nil {
		t.Fatal("missing disposable connection")
	}
	cfg, err := pgxpool.ParseConfig(string(secret))
	if err != nil {
		t.Fatal("invalid disposable connection")
	}
	id, err := uuid.Parse(os.Getenv("HEALTH_DBT_TENANT_ID"))
	c := cfg.ConnConfig
	trace := &chartRoute{}
	c.Tracer = trace
	if err != nil || !net.ParseIP(c.Host).IsLoopback() || len(c.Fallbacks) != 0 || c.Database != "serving_archive" || c.User != tenants.TenantRoleName(id) {
		t.Fatal("unexpected archive connection")
	}
	c.RuntimeParams["search_path"] = "health"
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var guarded bool
	err = pool.QueryRow(ctx, `select exists(select from public.serving_archive_guard where tenant_role=current_user)
		and not has_database_privilege(current_user,current_database(),'CREATE')`).Scan(&guarded)
	if err != nil || !guarded {
		t.Fatal("unverified local tenant")
	}
	db := storage.NewFromPool(pool)
	if phase == "baseline" {
		if storage.SchemaContractVersion != 16 {
			t.Fatal("baseline writer must be v16")
		}
		var rows int
		if err = pool.QueryRow(ctx, "select count(*) from __tenant_identity").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows == 0 {
			if err = db.EnsureTenantIdentity(ctx, id, uuid.New()); err != nil {
				t.Fatal(err)
			}
		}
	}
	var op, markedID uuid.UUID
	var old storage.SchemaContractState
	err = pool.QueryRow(ctx, `select tenant_id,operation_id,schema_contract_version,schema_contract_checksum
		from __tenant_identity where singleton`).Scan(&markedID, &op, &old.Version, &old.Checksum)
	if err != nil || id != markedID {
		t.Fatal("profile marker mismatch")
	}
	if phase == "baseline" || phase == "migrate" {
		if err = db.AssertIdentity(ctx, c.User, "health"); err != nil {
			t.Fatal(err)
		}
		if err = db.MigrateSchemaContractContext(ctx); err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateTenantIdentityMarker(ctx, pool, "", id, op, old); err != nil {
			t.Fatal(err)
		}
		if err = db.VerifySchemaContractContext(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase == "journal" {
		if _, err := db.LoadCacheMaintenance(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase == "warm" {
		rows, err := pool.Query(ctx, `select date from daily_scores where date between
		    (select (max(date)::date-6)::text from daily_scores where steps>0)
		    and (select max(date) from daily_scores where steps>0) order by date`)
		if err != nil {
			t.Fatal(err)
		}
		days, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(days) != 7 {
			t.Fatal("incomplete seven-day reference")
		}
		if err = db.UpsertRecentCache(days, false); err != nil {
			t.Fatal(err)
		}
		return
	}
	mgr := &tenants.Manager{}
	const key = "local-archive-test-only"
	if err = mgr.SetLegacyMode(db, key, ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if phase == "chart" || phase == "range" {
		ui.New(mgr, nil, false).Register(mux)
		var end string
		if err = pool.QueryRow(ctx, "select max(date) from daily_scores").Scan(&end); err != nil {
			t.Fatal(err)
		}
		path := "/fragments/step-daily-series?to=" + end
		if phase == "range" {
			path += "&from=" + end
		}
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(w, req)
		ms := time.Since(start).Milliseconds()
		var body struct {
			Bucket string
			Points []storage.DailyStepPoint
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Bucket != "day" || (phase == "chart" && len(body.Points) == 0) {
			t.Fatal("step chart unavailable")
		}
		if err = os.WriteFile(filepath.Join(scratch, "chart.json"), w.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("chart_ms=%d chart_shadow=%t", ms, trace.shadow.Load())
		return
	}
	if phase != "first" && phase != "retry" {
		t.Fatal("unexpected phase")
	}
	var payload []byte
	var version string
	if phase == "retry" {
		payload, err = os.ReadFile(filepath.Join(scratch, "accepted.json"))
		if err != nil {
			t.Fatal(err)
		}
		v, err := os.ReadFile(filepath.Join(scratch, "contract"))
		if err != nil {
			t.Fatal(err)
		}
		version = string(v)
	} else {
		err = pool.QueryRow(ctx, `with day as (select max(date) as d from daily_scores where steps>0),
        good as (select p.date,p.source,p.qty,r.content_type='application/json; metrics-contract=2' as v2
            from metric_points p join health_records r on r.id=p.health_record_id join day on substring(p.date,1,10)=day.d
            where p.metric_name='step_count' and p.origin='live' and p.quality='ok' and p.units='count' and p.qty>0),
        chosen as (select v2 from good order by date desc,v2 desc limit 1)
        select jsonb_build_object('data',jsonb_build_object('metrics',jsonb_build_array(
            jsonb_build_object('name','step_count','units','count','data',jsonb_agg(jsonb_build_object(
            'date',g.date,'source',g.source,'qty',g.qty+17) order by g.date,g.source))
            || case when c.v2 then '{"grain":"hour_sum"}'::jsonb else '{}'::jsonb end))),
            case when c.v2 then '2' else '1' end from chosen c join good g using(v2) group by c.v2`).Scan(&payload, &version)
		if err != nil {
			t.Fatal("missing eligible late replacement")
		}
		if err = os.WriteFile(filepath.Join(scratch, "accepted.json"), payload, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(scratch, "contract"), []byte(version), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := db.PendingHealthRecords(1)
	if err != nil || len(pending) != 0 {
		t.Fatal("unrelated pending data")
	}
	refreshed := make(chan bool, 1)
	ingest := handler.New(mgr, func(_ *storage.DB, _ []string, ok bool) { refreshed <- ok }, health.HRZones{})
	ingest.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/health", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)
	req.Header.Set(metriccontract.Header, version)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var receipt struct{ ID int64 }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.ID <= 0 {
		t.Fatal("local receipt rejected")
	}
	if err = ingest.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-refreshed:
		if !ok {
			t.Fatal("inline refresh failed")
		}
	default:
		if phase != "retry" {
			t.Fatal("inline refresh missing")
		}
	}
	var complete bool
	if err = pool.QueryRow(ctx, "select processing_status='complete' from health_records where id=$1", receipt.ID).Scan(&complete); err != nil || !complete {
		t.Fatal("missing completed receipt")
	}
}
