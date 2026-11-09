package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestArchivedReportTransportFailures(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := testdb.SchemaName("report_archive")
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
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
	var requests atomic.Int32
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.Contains(r.URL.Path, "sendRichMessage") {
			if mode.Load() == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if mode.Load() == 2 {
				fmt.Fprint(w, `{"ok":true}`)
				return
			}
			if mode.Load() == 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer srv.Close()
	old := telegramAPIBase
	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = old }()
	bot := NewBot("token", "chat")
	cfg := Config{TelegramRichMessages: true}
	report := func() *storage.ReportHistory {
		return &storage.ReportHistory{Kind: "morning", LocalDate: "2026-11-09", Timezone: "UTC", Language: "en", PlainBody: "plain", RichBody: "rich"}
	}
	first := report()
	sent, err := sendArchivedReport(reportDeliveryDurable, db, bot, cfg, "report:morning:2026-11-09", first)
	if err != nil || !sent || requests.Load() != 2 {
		t.Fatalf("fallback %v %v requests=%d", sent, err, requests.Load())
	}
	detail, err := db.GetReportHistory(ctx, first.ID)
	if err != nil || len(detail.Attempts) != 2 || detail.Attempts[0].Status != "failed" || detail.Attempts[1].Status != "sent" || detail.Attempts[1].MessageID == nil || *detail.Attempts[1].MessageID != 42 {
		t.Fatalf("attempts %+v %v", detail, err)
	}
	mode.Store(1)
	second := report()
	sent, err = sendArchivedReport(reportDeliveryDurable, db, bot, cfg, "report:morning:2026-11-10", second)
	if !sent || err == nil || requests.Load() != 3 {
		t.Fatalf("ambiguous %v %v requests=%d", sent, err, requests.Load())
	}
	sent, err = sendArchivedReport(reportDeliveryDurable, db, bot, cfg, "report:morning:2026-11-10", report())
	if sent || err != nil || requests.Load() != 3 {
		t.Fatalf("ambiguous retry %v %v requests=%d", sent, err, requests.Load())
	}
	bad := report()
	bad.Kind = "invalid"
	sent, err = sendArchivedReport(reportDeliveryDurable, db, bot, cfg, "report:morning:bad", bad)
	if sent || err == nil || requests.Load() != 3 {
		t.Fatalf("bad reservation %v %v requests=%d", sent, err, requests.Load())
	}
	for _, scenario := range []int32{2, 3} {
		mode.Store(scenario)
		key := fmt.Sprintf("report:morning:ambiguous-%d", scenario)
		history := report()
		before := requests.Load()
		if reserved, err := sendArchivedReport(reportDeliveryDurable, db, bot, cfg, key, history); !reserved || err == nil || requests.Load() != before+1 {
			t.Fatalf("scenario %d must be ambiguous without plain fallback: reserved=%v err=%v requests=%d", scenario, reserved, err, requests.Load()-before)
		}
		detail, err := db.GetReportHistory(ctx, history.ID)
		if err != nil || len(detail.Attempts) != 1 || detail.Attempts[0].Status != "ambiguous" {
			t.Fatalf("scenario %d archive: %+v err=%v", scenario, detail, err)
		}
		if reserved, err := sendArchivedReport(reportDeliveryDurable, db, bot, cfg, key, report()); reserved || err != nil || requests.Load() != before+1 {
			t.Fatalf("scenario %d retried ambiguous transport: reserved=%v err=%v", scenario, reserved, err)
		}
	}
}
