package apnspush

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

type stage3MockSender struct {
	sendFn func(ctx context.Context, env, tok, reqID, pushType, collapse string, priority int, exp time.Time, payload []byte) (Result, error)
}

func (m *stage3MockSender) Send(ctx context.Context, env, tok, reqID, pushType, collapse string, priority int, exp time.Time, payload []byte) (Result, error) {
	return m.sendFn(ctx, env, tok, reqID, pushType, collapse, priority, exp, payload)
}

func setupStage3WorkerDB(t *testing.T) (*storage.DB, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.DSN(t)
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	schema := testdb.SchemaName("apns_stage3")
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
	t.Cleanup(func() {
		db.Close()
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
	})
	return db, pool
}

func TestStage3PayloadsAreNeutralAndOmitStaticBadge(t *testing.T) {
	itemID := uuid.New()
	bindingID := uuid.New()
	kinds := []string{
		storage.InboxKindMorningReport,
		storage.InboxKindEveningReport,
		storage.InboxKindSleepUpdate,
		storage.InboxKindGuidanceChange,
		storage.InboxKindWeeklyDigest,
		storage.InboxKindNudge,
		storage.InboxKindPromptCheckin,
		storage.InboxKindPromptWakeFeedback,
		storage.InboxKindPromptContext,
	}
	for _, kind := range kinds {
		for _, locale := range []string{"en", "ru", "sr"} {
			raw, err := BuildPayloadForKind(kind, locale, itemID, bindingID)
			if err != nil {
				t.Fatalf("BuildPayloadForKind(%s, %s): %v", kind, locale, err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal payload(%s, %s): %v", kind, locale, err)
			}
			aps := decoded["aps"].(map[string]any)
			if _, hasBadge := aps["badge"]; hasBadge {
				t.Fatalf("payload(%s, %s) must not carry static aps.badge: %s", kind, locale, raw)
			}
			if decoded["reg_id"] != bindingID.String() {
				t.Fatalf("payload(%s, %s) missing reg_id", kind, locale)
			}
			if decoded["kind"] != kind {
				t.Fatalf("payload(%s, %s) wrong kind=%v", kind, locale, decoded["kind"])
			}
			if kind == storage.InboxKindPromptWakeFeedback || kind == storage.InboxKindPromptContext {
				if aps["category"] != CategoryPromptOpen {
					t.Fatalf("payload(%s, %s) expected category %q, got %v", kind, locale, CategoryPromptOpen, aps["category"])
				}
				if decoded["prompt_id"] != itemID.String() {
					t.Fatalf("payload(%s, %s) missing prompt_id", kind, locale)
				}
			}
			if strings.Contains(string(raw), "2026-") {
				t.Fatalf("payload(%s, %s) must not contain dates: %s", kind, locale, raw)
			}
		}
	}
}

func TestWorkerRejectsV2DeliveryAfterDeviceDowngradesToContractV1(t *testing.T) {
	db, pool := setupStage3WorkerDB(t)
	ctx := context.Background()
	if err := db.SaveSettings(map[string]string{
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatalf("enable flags: %v", err)
	}

	deviceID := uuid.New()
	token := strings.Repeat("a", 64)
	if _, err := db.UpsertPushDevice(ctx, storage.UpsertPushDeviceInput{
		DeviceID:             deviceID,
		APNsToken:            token,
		APNsEnv:              storage.PushEnvSandbox,
		BundleID:             "com.dzarlax.health-sync",
		Locale:               "en",
		InboxContractVersion: storage.InboxContractV2,
	}); err != nil {
		t.Fatalf("upsert v2 device: %v", err)
	}

	// Publish evening_report while device is on contract v2 -> enqueues push_deliveries.
	key := "report:evening:2026-11-26"
	h := storage.ReportHistory{
		DeliveryKey: &key,
		Kind:        "evening",
		LocalDate:   "2026-11-26",
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "Evening body",
	}
	inboxID, created, err := db.PublishEveningReportToInbox(ctx, &h, false)
	if err != nil || !created {
		t.Fatalf("publish evening: created=%v err=%v", created, err)
	}

	// Device downgrades back to TestFlight 33 (inbox_contract_version = 1) before worker sends!
	if _, err := db.UpsertPushDevice(ctx, storage.UpsertPushDeviceInput{
		DeviceID:             deviceID,
		APNsToken:            token,
		APNsEnv:              storage.PushEnvSandbox,
		BundleID:             "com.dzarlax.health-sync",
		Locale:               "en",
		InboxContractVersion: storage.InboxContractV1,
	}); err != nil {
		t.Fatalf("downgrade device to v1: %v", err)
	}

	var sentCount int
	mock := &stage3MockSender{
		sendFn: func(ctx context.Context, env, tok, reqID, pushType, collapse string, priority int, exp time.Time, payload []byte) (Result, error) {
			sentCount++
			return Result{Status: 200}, nil
		},
	}
	if err := ProcessTenantDeliveries(ctx, db, mock, storage.NotifyConfig{Timezone: "UTC"}, time.Now().UTC(), 10); err != nil {
		t.Fatalf("ProcessTenantDeliveries: %v", err)
	}
	if sentCount != 0 {
		t.Fatalf("expected 0 APNs sends after device downgraded to v1, got %d", sentCount)
	}
	var state, reason string
	if err := pool.QueryRow(ctx, `SELECT state, error_reason FROM push_deliveries WHERE inbox_item_id = $1`, inboxID).Scan(&state, &reason); err != nil {
		t.Fatalf("read delivery state: %v", err)
	}
	if state != storage.PushDeliveryStateRejected || reason != "device_contract_downgraded" {
		t.Fatalf("expected rejected/device_contract_downgraded, got %s/%s", state, reason)
	}
}

func TestWorkerRespectsCategoryPreferenceAndWakeFeedbackFreshness(t *testing.T) {
	db, pool := setupStage3WorkerDB(t)
	ctx := context.Background()
	if err := db.SaveSettings(map[string]string{
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatalf("enable flags: %v", err)
	}
	if _, err := db.UpsertPushDevice(ctx, storage.UpsertPushDeviceInput{
		DeviceID:             uuid.New(),
		APNsToken:            strings.Repeat("b", 64),
		APNsEnv:              storage.PushEnvSandbox,
		BundleID:             "com.dzarlax.health-sync",
		Locale:               "ru",
		InboxContractVersion: storage.InboxContractV2,
	}); err != nil {
		t.Fatalf("upsert v2 device: %v", err)
	}

	// Enqueue evening report, then disable push_pref_evening_report before worker sends.
	key := "report:evening:2026-11-26"
	h := storage.ReportHistory{DeliveryKey: &key, Kind: "evening", LocalDate: "2026-11-26", Timezone: "UTC", Language: "ru", PlainBody: "Evening"}
	evID, _, err := db.PublishEveningReportToInbox(ctx, &h, false)
	if err != nil {
		t.Fatalf("publish evening: %v", err)
	}
	if err := db.SaveSettings(map[string]string{storage.SettingPushPrefEveningReport: "false"}); err != nil {
		t.Fatalf("disable evening push pref: %v", err)
	}

	// Enqueue wake feedback prompt, then answer it BEFORE worker sends.
	today := time.Now().UTC().Format("2006-11-26")
	today = "2026-11-26"
	wfID, _, _, err := db.EnsureCanonicalWakeFeedbackPrompt(ctx, today, json.RawMessage(`"2026-11-26T07:00:00Z"`), storage.WakeConfidenceHigh, time.Now().UTC())
	if err != nil {
		t.Fatalf("ensure wake feedback: %v", err)
	}
	if _, err := db.AnswerWakeFeedbackByPromptID(ctx, wfID, today, storage.CheckinChannelIOSApp, storage.WakeFeedbackConfirmed, time.Now().UTC()); err != nil {
		t.Fatalf("answer wake feedback: %v", err)
	}

	var sentCount int
	mock := &stage3MockSender{
		sendFn: func(ctx context.Context, env, tok, reqID, pushType, collapse string, priority int, exp time.Time, payload []byte) (Result, error) {
			sentCount++
			return Result{Status: 200}, nil
		},
	}
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if err := ProcessTenantDeliveries(ctx, db, mock, storage.NotifyConfig{Timezone: "UTC"}, now, 10); err != nil {
		t.Fatalf("ProcessTenantDeliveries: %v", err)
	}
	if sentCount != 0 {
		t.Fatalf("expected 0 APNs sends, got %d", sentCount)
	}
	var evState, evReason, wfState, wfReason string
	_ = pool.QueryRow(ctx, `SELECT state, error_reason FROM push_deliveries WHERE inbox_item_id = $1`, evID).Scan(&evState, &evReason)
	_ = pool.QueryRow(ctx, `SELECT state, error_reason FROM push_deliveries WHERE inbox_item_id = $1`, wfID).Scan(&wfState, &wfReason)
	if evState != storage.PushDeliveryStateRejected || evReason != "category_disabled" {
		t.Fatalf("expected evening rejected/category_disabled, got %s/%s", evState, evReason)
	}
	if wfState != storage.PushDeliveryStateExpired || wfReason != "prompt_no_longer_active" {
		t.Fatalf("expected wake feedback expired/prompt_no_longer_active, got %s/%s", wfState, wfReason)
	}
}
