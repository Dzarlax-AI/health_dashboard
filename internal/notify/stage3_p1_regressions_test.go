package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func setupStage3NotifyDB(t *testing.T) *storage.DB {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx := context.Background()
	adminPool, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := testdb.SchemaName("stage3_notify")
	if err := testdb.CreateSchema(ctx, adminPool, schema); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_ = testdb.DropSchema(context.Background(), adminPool, schema)
		adminPool.Close()
	})
	db, err := storage.NewWithSchema(ctx, dsn, schema)
	if err != nil {
		t.Fatalf("NewWithSchema: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatalf("MigrateSchemaContractContext: %v", err)
	}
	if err := db.SaveSettings(map[string]string{
		"timezone":                       "UTC",
		"report_lang":                    "en",
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingTelegramNotificationsEnabled: "true",
		storage.SettingAPNSPushEnabled:              "false",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return db
}

func TestStage3MidFlightTelegramDisableAfterReserveReportAttemptSkipsHTTPAndFinalizes(t *testing.T) {
	db := setupStage3NotifyDB(t)
	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")

	var httpCalls atomic.Int32
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":777}}`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL

	key := "report:evening:" + today
	h := storage.ReportHistory{
		DeliveryKey: &key,
		Kind:        "evening",
		LocalDate:   today,
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "Evening summary body",
		RichBody:    "<b>Evening summary</b>",
	}
	if _, _, err := db.PublishEveningReportToInbox(ctx, &h, true); err != nil {
		t.Fatalf("PublishEveningReportToInbox: %v", err)
	}

	// Pause immediately after ReserveReportAttempt commits and flip
	// telegram_notifications_enabled=false before any HTTP request can start.
	restoreHook := setBeforeReportAttemptSendHook(func() {
		if err := db.SaveSettings(map[string]string{
			storage.SettingTelegramNotificationsEnabled: "false",
		}); err != nil {
			t.Errorf("disable telegram mid-flight: %v", err)
		}
	})
	defer restoreHook()

	bot := NewBot("test-token", "123456")
	cfg := Config{
		Token:                "test-token",
		ChatID:               "123456",
		Lang:                 "en",
		Timezone:             "UTC",
		TelegramRichMessages: true,
	}

	sent, err := DeliverArchivedReportToTelegram(db, bot, cfg, &h)
	if err != nil {
		t.Fatalf("expected clean opt-out skip without error, got %v", err)
	}
	if sent {
		t.Fatalf("expected sent=false when Telegram was disabled after ReserveReportAttempt")
	}
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("expected 0 Telegram HTTP calls after mid-flight disable, got %d", got)
	}

	// Verify report_delivery_attempts row was finalized as failed/telegram_disabled (no dangling reserved row).
	detail, err := db.GetReportHistory(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetReportHistory: %v", err)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("expected 1 finalized attempt row, got %d", len(detail.Attempts))
	}
	if detail.Attempts[0].Status != "failed" || detail.Attempts[0].ErrorCode != "telegram_disabled" || detail.Attempts[0].CompletedAt == nil {
		t.Fatalf("unexpected attempt finalization: %+v", detail.Attempts[0])
	}

	// Verify notification_deliveries is finalized as failed/telegram_disabled and NOT retried even if re-enabled later.
	if err := db.SaveSettings(map[string]string{storage.SettingTelegramNotificationsEnabled: "true"}); err != nil {
		t.Fatalf("re-enable telegram: %v", err)
	}
	restoreHook()
	retried, err := RetryPendingInboxTelegramReports(ctx, bot, db, cfg, today, nil)
	if err != nil {
		t.Fatalf("RetryPendingInboxTelegramReports: %v", err)
	}
	if retried != 0 || httpCalls.Load() != 0 {
		t.Fatalf("expected telegram_disabled delivery not to be retroactively retried, retried=%d calls=%d", retried, httpCalls.Load())
	}
}

func TestStage3InboxTelegramRetriesSucceedAfterFailureAndNeverRetryAmbiguous(t *testing.T) {
	db := setupStage3NotifyDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	var mode atomic.Int32 // 0 = 500 reject, 1 = 200 ok, 2 = ambiguous malformed 200
	var httpCalls atomic.Int32
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		switch mode.Load() {
		case 0:
			// 429/4xx with ok=false is a proven provider rejection (safely retryable).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"ok":false,"description":"Too Many Requests: retry later"}`)
		case 1:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":901}}`)
		case 2:
			// Malformed 200 triggers telegramTransportError -> ambiguous delivery status.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL

	bot := NewBot("test-token", "123456")
	cfg := Config{
		Token:            "test-token",
		ChatID:           "123456",
		Lang:             "en",
		Timezone:         "UTC",
		DaytimeEnabled:   true,
		DaytimeStartHour: 0,
		DaytimeEndHour:   23,
	}

	// --- Scenario A: Proactive nudge/digest retries even after cadence date is persisted ---
	ruleName := "stage3_retry_test_" + uuid.NewString()[:8]
	registryMu.Lock()
	origRegistry := append([]ProactiveNotification(nil), registry...)
	registry = []ProactiveNotification{{
		Name:      ruleName,
		Cadence:   7 * 24 * time.Hour,
		HourOfDay: -1,
		Eligible:  func(context.Context, *storage.DB, Config) (bool, string) { return true, "" },
		Render:    func(context.Context, *storage.DB, Config, string) (string, error) { return "Nudge body", nil },
	}}
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = origRegistry
		registryMu.Unlock()
	})

	mode.Store(0) // Telegram rejects initially
	MaybeFireAll(bot, db, cfg, "https://health.example.com")
	if got := db.GetSetting(proactiveSentKey(ruleName), ""); got != today {
		t.Fatalf("expected cadence date %s persisted on Inbox publication, got %q", today, got)
	}
	if httpCalls.Load() != 1 {
		t.Fatalf("expected 1 failed initial Telegram attempt for nudge, got %d", httpCalls.Load())
	}

	// Next tick: Telegram recovers -> MaybeFireAll must bypass cadence gate and deliver saved report_id!
	mode.Store(1)
	MaybeFireAll(bot, db, cfg, "https://health.example.com")
	if httpCalls.Load() != 2 {
		t.Fatalf("expected retry of saved nudge report_id to succeed on second MaybeFireAll, calls=%d", httpCalls.Load())
	}
	// Third tick: already sent -> must NOT send again!
	MaybeFireAll(bot, db, cfg, "https://health.example.com")
	if httpCalls.Load() != 2 {
		t.Fatalf("expected no duplicate Telegram send after nudge succeeded, calls=%d", httpCalls.Load())
	}

	// Verify only 1 Inbox card exists for the nudge.
	feed, _, err := db.ListInboxItemsWithSnapshot(ctx, storage.InboxQuery{Limit: 20, Kind: storage.InboxKindNudge, ContractVersion: storage.InboxContractV2})
	if err != nil || len(feed) != 1 {
		t.Fatalf("expected exactly 1 nudge Inbox item, got len=%d err=%v", len(feed), err)
	}

	// --- Scenario B: Evening report retries via RetryPendingInboxTelegramReports ---
	evKey := "report:evening:" + today
	ev := storage.ReportHistory{DeliveryKey: &evKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening body"}
	if _, _, err := db.PublishEveningReportToInbox(ctx, &ev, true); err != nil {
		t.Fatalf("publish evening: %v", err)
	}
	mode.Store(0)
	if _, err := DeliverArchivedReportToTelegram(db, bot, cfg, &ev); err == nil {
		t.Fatalf("expected initial evening Telegram failure")
	}
	mode.Store(1)
	retried, err := RetryPendingInboxTelegramReports(ctx, bot, db, cfg, today, []string{storage.InboxKindEveningReport})
	if err != nil || retried != 1 {
		t.Fatalf("expected evening report retry=1 err=nil, got retried=%d err=%v", retried, err)
	}
	// Repeat call is a no-op once sent.
	retriedAgain, err := RetryPendingInboxTelegramReports(ctx, bot, db, cfg, today, []string{storage.InboxKindEveningReport})
	if err != nil || retriedAgain != 0 {
		t.Fatalf("expected 0 retries after evening sent, got retried=%d err=%v", retriedAgain, err)
	}

	// --- Scenario C: Daytime update retries on provider failure, NEVER retries ambiguous ---
	// Use a dedicated tenant schema so the 2/day proactive budget permits both
	// guidance_change and sleep_update on the same day.
	daytimeDB := setupStage3NotifyDB(t)
	gc := storage.ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Guidance changed", Context: json.RawMessage(`{}`)}
	if id, _, err := daytimeDB.PublishProactiveToInbox(ctx, &gc, "guidance_change:ep-retry:1", "proactive:daytime:ep-retry:1", nil, now, time.UTC, storage.InboxKindGuidanceChange, 8*time.Hour, true); err != nil || id == uuid.Nil {
		t.Fatalf("publish guidance_change: id=%s err=%v", id, err)
	}
	mode.Store(0)
	if _, err := DeliverArchivedReportToTelegram(daytimeDB, bot, cfg, &gc); err == nil {
		t.Fatalf("expected initial guidance_change Telegram failure")
	}

	// Publish a sleep_update whose Telegram attempt ends as `ambiguous`.
	su := storage.ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Sleep updated", Context: json.RawMessage(`{}`)}
	if id, _, err := daytimeDB.PublishProactiveToInbox(ctx, &su, "sleep_update:ep-ambig:1", "proactive:daytime:ep-ambig:1", nil, now.Add(2*time.Hour), time.UTC, storage.InboxKindSleepUpdate, 8*time.Hour, true); err != nil || id == uuid.Nil {
		t.Fatalf("publish sleep_update: id=%s err=%v", id, err)
	}
	mode.Store(2) // ambiguous response
	if _, err := DeliverArchivedReportToTelegram(daytimeDB, bot, cfg, &su); err == nil {
		t.Fatalf("expected ambiguous error for malformed Telegram 200")
	}

	callsBeforeDaytimeRetry := httpCalls.Load()
	mode.Store(1)
	retriedDaytime, err := RetryPendingInboxTelegramReports(ctx, bot, daytimeDB, cfg, today, []string{storage.InboxKindSleepUpdate, storage.InboxKindGuidanceChange})
	if err != nil {
		t.Fatalf("daytime retry error: %v", err)
	}
	if retriedDaytime != 1 || httpCalls.Load()-callsBeforeDaytimeRetry != 1 {
		t.Fatalf("expected ONLY failed guidance_change to retry (not ambiguous sleep_update): retried=%d callsDelta=%d", retriedDaytime, httpCalls.Load()-callsBeforeDaytimeRetry)
	}
}

func TestStage3CrashAfterInboxPublishBeforeFirstTelegramReservationRecoversWithoutRetroactiveOptOutSpam(t *testing.T) {
	db := setupStage3NotifyDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	var httpCalls atomic.Int32
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":999}}`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL

	bot := NewBot("test-token", "123456")
	cfg := Config{
		Token:    "test-token",
		ChatID:   "123456",
		Lang:     "en",
		Timezone: "UTC",
	}

	// Simulate crash right after Inbox publication commits (and cadence marker is saved)
	// BEFORE DeliverArchivedReportToTelegram ever creates a notification_deliveries row.
	evKey := "report:evening:" + today
	ev := storage.ReportHistory{DeliveryKey: &evKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening after crash"}
	if id, created, err := db.PublishEveningReportToInbox(ctx, &ev, true); err != nil || !created || id == uuid.Nil {
		t.Fatalf("publish evening: id=%s created=%v err=%v", id, created, err)
	}
	wd := storage.ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Digest after crash"}
	if id, created, err := db.PublishProactiveToInbox(ctx, &wd, "weekly_digest:"+today, "proactive:weekly_digest:"+today, nil, now, time.UTC, storage.InboxKindWeeklyDigest, 24*time.Hour, true); err != nil || !created || id == uuid.Nil {
		t.Fatalf("publish digest: id=%s created=%v err=%v", id, created, err)
	}
	_ = db.SaveSettings(map[string]string{proactiveSentKey("weekly_digest"): today})

	// Recovery tick discovers both unreserved cards (d.delivery_key IS NULL) and sends both to Telegram.
	retried, err := RetryPendingInboxTelegramReports(ctx, bot, db, cfg, today, nil)
	if err != nil {
		t.Fatalf("RetryPendingInboxTelegramReports after crash: %v", err)
	}
	if retried != 2 || httpCalls.Load() != 2 {
		t.Fatalf("expected both crash-stranded reports to be delivered on recovery tick, retried=%d calls=%d", retried, httpCalls.Load())
	}
	// Second recovery tick must be a complete no-op.
	retried2, err := RetryPendingInboxTelegramReports(ctx, bot, db, cfg, today, nil)
	if err != nil || retried2 != 0 || httpCalls.Load() != 2 {
		t.Fatalf("expected 0 duplicate deliveries on second recovery tick, retried=%d calls=%d err=%v", retried2, httpCalls.Load(), err)
	}

	// Contrast case 1: card published while telegram_notifications_enabled=false must NOT
	// be retroactively delivered if Telegram is re-enabled later on the same day.
	optOutDB := setupStage3NotifyDB(t)
	if err := optOutDB.SaveSettings(map[string]string{storage.SettingTelegramNotificationsEnabled: "false"}); err != nil {
		t.Fatalf("disable telegram: %v", err)
	}
	optOutEvKey := "report:evening:" + today
	optOutEv := storage.ReportHistory{DeliveryKey: &optOutEvKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Opt-out evening"}
	if id, created, err := optOutDB.PublishEveningReportToInbox(ctx, &optOutEv, true); err != nil || !created || id == uuid.Nil {
		t.Fatalf("publish opt-out evening: id=%s created=%v err=%v", id, created, err)
	}
	if err := optOutDB.SaveSettings(map[string]string{storage.SettingTelegramNotificationsEnabled: "true"}); err != nil {
		t.Fatalf("re-enable telegram: %v", err)
	}
	callsBeforeOptOutCheck := httpCalls.Load()
	retriedOptOut, err := RetryPendingInboxTelegramReports(ctx, bot, optOutDB, cfg, today, nil)
	if err != nil {
		t.Fatalf("RetryPendingInboxTelegramReports on opt-out profile: %v", err)
	}
	if retriedOptOut != 0 || httpCalls.Load() != callsBeforeOptOutCheck {
		t.Fatalf("expected opted-out card NOT to be retroactively sent after re-enable, retried=%d callsDelta=%d", retriedOptOut, httpCalls.Load()-callsBeforeOptOutCheck)
	}

	// Contrast case 2 (P1): card published when Telegram bot is NOT configured
	// (flag still default true, telegramDeliveryAllowed=false) followed immediately
	// by a crash and later bot configuration must NOT retroactively send old cards.
	unconfiguredDB := setupStage3NotifyDB(t)
	unconfEvKey := "report:evening:" + today
	unconfEv := storage.ReportHistory{DeliveryKey: &unconfEvKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Unconfigured evening"}
	if id, created, err := unconfiguredDB.PublishEveningReportToInbox(ctx, &unconfEv, false); err != nil || !created || id == uuid.Nil {
		t.Fatalf("publish unconfigured evening: id=%s created=%v err=%v", id, created, err)
	}
	unconfWd := storage.ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Unconfigured digest"}
	if id, created, err := unconfiguredDB.PublishProactiveToInbox(ctx, &unconfWd, "weekly_digest:"+today, "proactive:weekly_digest:"+today, nil, now, time.UTC, storage.InboxKindWeeklyDigest, 24*time.Hour, false); err != nil || !created || id == uuid.Nil {
		t.Fatalf("publish unconfigured digest: id=%s created=%v err=%v", id, created, err)
	}
	// Simulate configuring Telegram bot later on the same day and running recovery tick.
	callsBeforeUnconfCheck := httpCalls.Load()
	retriedUnconf, err := RetryPendingInboxTelegramReports(ctx, bot, unconfiguredDB, cfg, today, nil)
	if err != nil {
		t.Fatalf("RetryPendingInboxTelegramReports on later-configured profile: %v", err)
	}
	if retriedUnconf != 0 || httpCalls.Load() != callsBeforeUnconfCheck {
		t.Fatalf("expected cards published without configured bot NOT to be retroactively sent, retried=%d callsDelta=%d", retriedUnconf, httpCalls.Load()-callsBeforeUnconfCheck)
	}
}

func TestStage3TelegramFlagReadErrorFailsClosedAndDefersRetryably(t *testing.T) {
	db := setupStage3NotifyDB(t)
	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")

	var httpCalls atomic.Int32
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":555}}`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL

	bot := NewBot("test-token", "123456")
	cfg := Config{
		Token:    "test-token",
		ChatID:   "123456",
		Lang:     "en",
		Timezone: "UTC",
	}

	// Verify CheckTelegramNotificationsEnabled itself returns (false, err) on cancelled context / DB error,
	// never failing open to true even when flag is absent or false.
	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if enabled, err := storage.CheckTelegramNotificationsEnabled(canceledCtx, db); err == nil || enabled {
		t.Fatalf("expected CheckTelegramNotificationsEnabled to fail closed on DB/context error, got enabled=%v err=%v", enabled, err)
	}

	// Publish an evening report while Telegram is enabled.
	evKey := "report:evening:" + today
	ev := storage.ReportHistory{DeliveryKey: &evKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening"}
	if id, created, err := db.PublishEveningReportToInbox(ctx, &ev, true); err != nil || !created || id == uuid.Nil {
		t.Fatalf("publish evening: id=%s created=%v err=%v", id, created, err)
	}

	// Inject a DB read failure specifically AFTER ReserveReportAttempt commits and
	// before HTTP send: must NOT send HTTP, must finalize attempt + delivery as
	// failed/archive_unavailable so a later tick can retry!
	var failReadAfterReserve atomic.Bool
	restoreAfterReserve := setBeforeReportAttemptSendHook(func() {
		failReadAfterReserve.Store(true)
	})
	defer restoreAfterReserve()

	restoreCheck := setCheckTelegramEnabledHook(func(callCtx context.Context, callDB *storage.DB) (bool, error) {
		if failReadAfterReserve.Load() {
			return false, errors.New("simulated postgres connection reset")
		}
		return storage.CheckTelegramNotificationsEnabled(callCtx, callDB)
	})
	defer restoreCheck()

	_, err := DeliverArchivedReportToTelegram(db, bot, cfg, &ev)
	if err == nil {
		t.Fatalf("expected DB flag read error to be surfaced and defer delivery")
	}
	if httpCalls.Load() != 0 {
		t.Fatalf("expected 0 Telegram HTTP calls when flag read errored, got %d", httpCalls.Load())
	}

	detail, err := db.GetReportHistory(ctx, ev.ID)
	if err != nil {
		t.Fatalf("GetReportHistory: %v", err)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].Status != "failed" || detail.Attempts[0].ErrorCode != "archive_unavailable" {
		t.Fatalf("expected attempt finalized as failed/archive_unavailable, got %+v", detail.Attempts)
	}

	// Once PostgreSQL recovers, RetryPendingInboxTelegramReports MUST be able to retry and deliver!
	failReadAfterReserve.Store(false)
	restoreAfterReserve()
	restoreCheck()

	retried, err := RetryPendingInboxTelegramReports(ctx, bot, db, cfg, today, []string{storage.InboxKindEveningReport})
	if err != nil || retried != 1 || httpCalls.Load() != 1 {
		t.Fatalf("expected deferred report to succeed after DB recovery, retried=%d calls=%d err=%v", retried, httpCalls.Load(), err)
	}
}

func TestStage3TelegramOnlyWakeSenderReusesStoredInboxProposal(t *testing.T) {
	db := setupStage3NotifyDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	date := now.Format("2006-01-02")
	proposed := json.RawMessage(`"2026-11-26T07:15:00Z"`)
	if _, _, created, err := db.EnsureCanonicalWakeFeedbackPrompt(ctx, date, proposed, storage.WakeConfidenceHigh, now); err != nil || !created {
		t.Fatalf("publish Inbox wake feedback: created=%v err=%v", created, err)
	}
	if err := db.SaveSettings(map[string]string{storage.SettingInAppInboxEnabled: "false"}); err != nil {
		t.Fatal(err)
	}

	bot := &wakeFeedbackBot{}
	sent, err := SendWakeFeedbackPrompt(bot, db, "en", date, now)
	if err != nil || !sent {
		t.Fatalf("Telegram-only wake feedback: sent=%v err=%v", sent, err)
	}
	if !strings.Contains(bot.text, "07:15") {
		t.Fatalf("Telegram did not show the saved Inbox proposal: %q", bot.text)
	}
	fb, exists, err := db.GetCanonicalWakeFeedback(ctx, date)
	if err != nil || !exists || fb.Channel != storage.DerivedMetricFeedbackInbox || fb.PromptMessageID == nil || *fb.PromptMessageID != 42 {
		t.Fatalf("expected the original row with attached Telegram message: %+v exists=%v err=%v", fb, exists, err)
	}
	legacy, err := db.GetDerivedMetricFeedback(storage.DerivedMetricWakeTime, date, storage.DerivedMetricFeedbackTelegram)
	if err != nil || legacy != nil {
		t.Fatalf("unexpected second wake feedback row: %+v (%v)", legacy, err)
	}
}

func TestStage3ForcedDigestRechecksTelegramAfterPreparingReport(t *testing.T) {
	db := setupStage3NotifyDB(t)
	var checks, calls atomic.Int32
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL
	defer setCheckTelegramEnabledHook(func(ctx context.Context, store *storage.DB) (bool, error) {
		if checks.Add(1) == 2 {
			if err := store.SaveSettings(map[string]string{storage.SettingTelegramNotificationsEnabled: "false"}); err != nil {
				return false, err
			}
		}
		return storage.CheckTelegramNotificationsEnabled(ctx, store)
	})()

	cfg := Config{Token: "test-token", ChatID: "123456", Lang: "en"}
	err := SendWeeklyDigestForce(NewBot(cfg.Token, cfg.ChatID), db, cfg, 7)
	if !errors.Is(err, ErrTelegramNotificationsDisabled) || calls.Load() != 0 {
		t.Fatalf("digest should be blocked at the last check: checks=%d calls=%d err=%v", checks.Load(), calls.Load(), err)
	}
}
