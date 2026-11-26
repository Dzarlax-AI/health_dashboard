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

	"health-receiver/internal/apnspush"
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
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "")
	t.Setenv("TELEGRAM_WEBHOOK_TOKEN_HEADER", "")
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
	trigger := makeMorningTrigger(ctx, db, &sendMu, mgr, nil, schema, defaults, nil)
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

type noopAPNsTransport struct {
	calls atomic.Int32
}

func (n *noopAPNsTransport) Send(_ context.Context, _, _, _, _, _ string, _ int, _ time.Time, _ []byte) (apnspush.Result, error) {
	n.calls.Add(1)
	return apnspush.Result{Status: 200}, nil
}

// Regression test: enabling in_app_inbox_enabled and apns_push_enabled later in
// the day after today's morning report was already settled (e.g. sent via
// Telegram earlier) must NOT generate a late morning report revision or enqueue
// a retroactive APNs push.
func TestEnablingInboxAndAPNsAfterSettledMorningReportDoesNotResendOrEnqueuePush(t *testing.T) {
	t.Setenv("IN_APP_INBOX_ENABLED", "")
	t.Setenv("APNS_PUSH_ENABLED", "")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "")
	t.Setenv("TELEGRAM_WEBHOOK_TOKEN_HEADER", "")
	db, pool, schema, cleanup := setupServerInboxTestDB(t, "srv_late_enable_no_retro_push")
	defer cleanup()

	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")

	// Simulate an already-sent morning report from earlier today (pre-Inbox).
	if err := db.MarkMorningReportSent(today); err != nil {
		t.Fatalf("MarkMorningReportSent: %v", err)
	}

	// The independent check-in lane may legitimately create a new event during
	// daytime. Seed an answered check-in so this test only measures report replay.
	now := time.Now()
	promptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, today, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AnswerCheckinByPromptID(ctx, promptID, today, storage.CheckinChannelWeb, storage.CheckinAnswerGreat, now); err != nil {
		t.Fatal(err)
	}

	// Later in the day, register an active push device and enable both Inbox + APNs.
	if _, err := db.UpsertPushDevice(ctx, storage.UpsertPushDeviceInput{
		DeviceID:  [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		APNsToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		APNsEnv:   storage.PushEnvProduction,
		BundleID:  apnspush.DefaultTopic,
		Locale:    "en",
	}); err != nil {
		t.Fatalf("UpsertPushDevice: %v", err)
	}
	if err := db.SaveSettings(map[string]string{
		"timezone":                       "UTC",
		"report_lang":                    "en",
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
		"report_morning_weekday":         "5",
		"report_morning_weekend":         "5",
	}); err != nil {
		t.Fatal(err)
	}

	if !morningReportDoneForLifecycle(db, today, true, false) {
		t.Fatal("already-settled morning report must stay done after enabling Inbox/APNs")
	}
	if !morningReportDoneForLifecycle(db, today, true, true) {
		t.Fatal("already-settled morning report must stay done with Telegram enabled too")
	}

	apnsMock := &noopAPNsTransport{}
	mgr := &tenants.Manager{}
	var sendMu sync.Mutex
	defaults := storage.NotifyConfig{Timezone: "UTC", MorningCapHour: 6}
	trigger := makeMorningTrigger(ctx, db, &sendMu, mgr, nil, schema, defaults, apnsMock)
	trigger()

	var inboxReports, pushRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE kind = 'morning_report'`).Scan(&inboxReports); err != nil || inboxReports != 0 {
		t.Fatalf("retroactive morning_report inbox_items = %d (err=%v), want 0", inboxReports, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_deliveries WHERE kind = 'morning_report'`).Scan(&pushRows); err != nil || pushRows != 0 {
		t.Fatalf("retroactive morning_report push_deliveries = %d (err=%v), want 0", pushRows, err)
	}
	if apnsMock.calls.Load() != 0 {
		t.Fatalf("retroactive APNs sends = %d, want 0", apnsMock.calls.Load())
	}
}

func TestConfiguredAPNSTransportPreservesMissingSender(t *testing.T) {
	var sender *apnspush.Sender
	if configuredAPNSTransport(sender) != nil {
		t.Fatal("missing credentials must produce a nil transport interface")
	}
	sender = &apnspush.Sender{}
	if configuredAPNSTransport(sender) != sender {
		t.Fatal("configured transport must preserve the sender")
	}
}

func TestExistingInboxWakePromptKeepsSuppressingContextWithoutTelegram(t *testing.T) {
	db, _, _, cleanup := setupServerInboxTestDB(t, "srv_wake_inbox_repeat")
	defer cleanup()
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	proposed, err := json.Marshal(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := db.EnsureCanonicalWakeFeedbackPrompt(context.Background(), date, proposed, storage.WakeConfidenceHigh, now); err != nil || !created {
		t.Fatalf("publish wake feedback: created=%v err=%v", created, err)
	}

	for range 2 {
		if !trySendWakeFeedbackAfterMorning(nil, db, notify.Config{}, date, now, true, false) {
			t.Fatal("existing Inbox wake feedback must keep suppressing context prompts")
		}
	}
}
