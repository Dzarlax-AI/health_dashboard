package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"health-receiver/internal/notify"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
)

func setupServerInboxTestDB(t *testing.T, prefix string) (*storage.DB, *pgxpool.Pool, string, func()) {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := testdb.SchemaName(prefix)
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		bootstrap.Close()
		t.Fatal(err)
	}
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		db.Close()
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
		t.Fatal(err)
	}
	return db, pool, schema, func() {
		db.Close()
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
	}
}

func TestMorningTriggerRecoversStaleZeroAttemptReservationThroughMinuteTick(t *testing.T) {
	t.Setenv("IN_APP_INBOX_ENABLED", "")
	db, pool, schema, cleanup := setupServerInboxTestDB(t, "srv_morning_recover")
	defer cleanup()

	ctx := context.Background()
	// Default profile without explicit opt-in stays off.
	if storage.IsInAppInboxEnabled(db) {
		t.Fatal("fresh profile must have in_app_inbox_enabled=false by default")
	}

	// Pick a timezone where current local hour is between 10 and 18 so the
	// morning trigger is within active hours and past MorningCapHour=6.
	loc := time.FixedZone("TestMorning", 0)
	nowUTC := time.Now().UTC()
	offsetHours := 12 - nowUTC.Hour()
	if offsetHours > 12 {
		offsetHours -= 24
	}
	if offsetHours < -11 {
		offsetHours += 24
	}
	// Map offsetHours to an IANA Etc/GMT zone (note POSIX sign inversion).
	tzName := "UTC"
	switch {
	case offsetHours > 0:
		tzName = "Etc/GMT-" + string(rune('0'+offsetHours))
		if offsetHours >= 10 {
			tzName = "Etc/GMT-1" + string(rune('0'+(offsetHours-10)))
		}
	case offsetHours < 0:
		abs := -offsetHours
		tzName = "Etc/GMT+" + string(rune('0'+abs))
		if abs >= 10 {
			tzName = "Etc/GMT+1" + string(rune('0'+(abs-10)))
		}
	}
	var err error
	loc, err = time.LoadLocation(tzName)
	if err != nil {
		t.Fatalf("load %s: %v", tzName, err)
	}
	today := time.Now().In(loc).Format("2006-01-02")

	var sends atomic.Int32
	fakeTG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": int64(100 + sends.Load())},
		})
	}))
	defer fakeTG.Close()
	restoreBase := notify.SetTelegramAPIBaseForTest(fakeTG.URL)
	defer restoreBase()

	if err := db.SaveSettings(map[string]string{
		storage.SettingInAppInboxEnabled: "true",
		"timezone":                       tzName,
		"report_lang":                    "en",
		"telegram_token":                 "test-token",
		"telegram_chat_id":               "12345",
		"report_morning_weekday":         "5",
		"report_morning_weekend":         "5",
	}); err != nil {
		t.Fatal(err)
	}

	// Publish today's morning report into report_history + inbox_items, then
	// simulate a crash right after ReserveReportDeliveryForExisting (status=reserved,
	// zero report_delivery_attempts, aged past 30s).
	key := "report:morning:" + today
	initial := &storage.ReportHistory{
		DeliveryKey: &key,
		Kind:        "morning",
		LocalDate:   today,
		Timezone:    tzName,
		Language:    "en",
		PlainBody:   "<b>Morning Report</b>\nSleep 7h 15m",
	}
	inboxID, created, err := db.PublishMorningReportToInbox(ctx, initial, false)
	if err != nil || !created {
		t.Fatalf("seed morning inbox report: created=%v err=%v", created, err)
	}
	if _, reserved, err := db.ReserveReportDeliveryForExisting(ctx, initial.ID); err != nil || !reserved {
		t.Fatalf("seed delivery reservation: reserved=%v err=%v", reserved, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notification_deliveries SET reserved_at = NOW() - INTERVAL '35 seconds' WHERE delivery_key = $1`, key); err != nil {
		t.Fatalf("age reservation: %v", err)
	}

	// Before the minute tick: lifecycle must NOT consider the morning report done.
	if morningReportDoneForLifecycle(db, today, true, true) {
		t.Fatal("stale >=30s zero-attempt reservation must remain unfinished in morningReportDoneForLifecycle")
	}

	// Run the real morning trigger (same path invoked by the 1-minute ticker).
	mgr := &tenants.Manager{}
	var sendMu sync.Mutex
	defaults := storage.NotifyConfig{MorningCapHour: 6}
	trigger := makeMorningTrigger(ctx, db, &sendMu, mgr, nil, schema, defaults)
	trigger()

	if sends.Load() != 1 {
		t.Fatalf("expected exactly 1 Telegram send on recovery tick, got %d", sends.Load())
	}
	if !morningReportDoneForLifecycle(db, today, true, true) {
		t.Fatal("morning report should be settled after recovery tick succeeded")
	}

	// Verify report_history and inbox_items still have exactly 1 row pointing to the same report_id.
	var reportCount, inboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_history WHERE delivery_key = $1`, key).Scan(&reportCount); err != nil || reportCount != 1 {
		t.Fatalf("report_history count = %d (err=%v), want 1 (same report_id reused)", reportCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE id = $1 AND report_id = $2`, inboxID, initial.ID).Scan(&inboxCount); err != nil || inboxCount != 1 {
		t.Fatalf("inbox_items count = %d (err=%v), want 1 pointing to original report_id", inboxCount, err)
	}

	// Subsequent minute tick must be a no-op (no duplicate Telegram send).
	trigger()
	if sends.Load() != 1 {
		t.Fatalf("second tick sent duplicate message: sends=%d", sends.Load())
	}
}
