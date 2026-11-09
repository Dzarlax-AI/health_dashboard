package ui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestReportHistoryReadIsolationAndPagination(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	schema := testdb.SchemaName("history_ui")
	if err = testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(context.Background(), bootstrap, schema)
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	defer db.Close()
	if err = db.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureReportHistoryTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"report:morning:2026-11-08", "report:morning:2026-11-09"} {
		h := storage.ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: key[len(key)-10:], Timezone: "UTC", Language: "en", PlainBody: "body"}
		if _, ok, e := db.ReserveArchivedReport(ctx, &h); e != nil || !ok {
			t.Fatalf("reserve: %v", e)
		}
	}
	h := &Handler{}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(ctxdb.WithDB(r.Context(), db, schema))
		w := httptest.NewRecorder()
		h.reportHistoryList(w, r)
		return w
	}
	w := get("/api/reports/history?from=2026-11-01&to=2026-11-30&limit=1")
	var page struct {
		Items []storage.ReportHistoryItem `json:"items"`
		Next  string                      `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == "" {
		t.Fatalf("first page %d %s", w.Code, w.Body.String())
	}
	first := page.Items[0].ID
	w = get("/api/reports/history?from=2026-11-01&to=2026-11-30&limit=1&cursor=" + page.Next)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID == first {
		t.Fatalf("second page %d %s", w.Code, w.Body.String())
	}
	w = get("/api/reports/history?to=bad")
	if w.Code != 400 {
		t.Fatalf("bad date status %d", w.Code)
	}
	otherSchema := testdb.SchemaName("history_other")
	if err = testdb.CreateSchema(ctx, bootstrap, otherSchema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(context.Background(), bootstrap, otherSchema)
	otherPool, err := testdb.NewPool(ctx, dsn, otherSchema)
	if err != nil {
		t.Fatal(err)
	}
	otherDB := storage.NewFromPool(otherPool)
	defer otherDB.Close()
	if err = otherDB.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	if err = otherDB.EnsureReportHistoryTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	foreignReq := httptest.NewRequest("GET", "/api/reports/history/"+first.String(), nil)
	foreignReq.SetPathValue("id", first.String())
	foreignReq = foreignReq.WithContext(ctxdb.WithDB(foreignReq.Context(), otherDB, otherSchema))
	foreignW := httptest.NewRecorder()
	h.reportHistoryDetail(foreignW, foreignReq)
	if foreignW.Code != 404 {
		t.Fatalf("other tenant status %d", foreignW.Code)
	}
	r := httptest.NewRequest("GET", "/api/reports/history/00000000-0000-0000-0000-000000000123", nil)
	r.SetPathValue("id", "00000000-0000-0000-0000-000000000123")
	r = r.WithContext(ctxdb.WithDB(r.Context(), db, schema))
	w = httptest.NewRecorder()
	h.reportHistoryDetail(w, r)
	if w.Code != 404 {
		t.Fatalf("foreign ID status %d", w.Code)
	}
}
