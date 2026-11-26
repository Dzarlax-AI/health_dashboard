package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func setupStage3DB(t *testing.T) *DB {
	t.Helper()
	db, cleanup := setupInboxTestDB(t, "stage3")
	t.Cleanup(cleanup)
	ctx := context.Background()
	if err := db.ensureFullSchemaContractObjectsContext(ctx); err != nil {
		t.Fatalf("ensure full schema contract v23: %v", err)
	}
	if err := db.SaveSettings(map[string]string{
		SettingInAppInboxEnabled: "true",
		SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatalf("enable inbox/apns: %v", err)
	}
	return db
}

func TestStage3SchemaContractV23Verify(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	if err := db.VerifySchemaContractContext(ctx); err != nil {
		t.Fatalf("VerifySchemaContractContext(v23) failed: %v", err)
	}
}

func TestStage3EveningReportPublicationDecoupledFromTelegram(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()

	// Register one v1 (TestFlight 33) device and one v2 device.
	devV1, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:             uuid.New(),
		APNsToken:            "1111111111111111111111111111111111111111111111111111111111111111",
		APNsEnv:              PushEnvSandbox,
		BundleID:             "com.dzarlax.health-sync",
		Locale:               "en",
		InboxContractVersion: InboxContractV1,
	})
	if err != nil {
		t.Fatalf("upsert v1 device: %v", err)
	}
	devV2, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:             uuid.New(),
		APNsToken:            "2222222222222222222222222222222222222222222222222222222222222222",
		APNsEnv:              PushEnvSandbox,
		BundleID:             "com.dzarlax.health-sync",
		Locale:               "ru",
		InboxContractVersion: InboxContractV2,
	})
	if err != nil {
		t.Fatalf("upsert v2 device: %v", err)
	}

	key := "report:evening:2026-11-26"
	h := ReportHistory{
		DeliveryKey: &key,
		Kind:        "evening",
		LocalDate:   "2026-11-26",
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "<b>Evening</b>\nSteps: 9200",
	}
	inboxID, created, err := db.PublishEveningReportToInbox(ctx, &h, true)
	if err != nil || !created || inboxID == uuid.Nil {
		t.Fatalf("PublishEveningReportToInbox: id=%s created=%v err=%v", inboxID, created, err)
	}

	// Second call is idempotent (created=false) and does not duplicate card or push.
	inboxID2, created2, err := db.PublishEveningReportToInbox(ctx, &h, true)
	if err != nil || created2 || inboxID2 != inboxID {
		t.Fatalf("repeat PublishEveningReportToInbox: id=%s created=%v err=%v", inboxID2, created2, err)
	}

	// Inbox-only publication must NOT create dangling rows in notification_deliveries.
	var ndCount int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE delivery_key = $1`, key).Scan(&ndCount); err != nil {
		t.Fatalf("count notification_deliveries: %v", err)
	}
	if ndCount != 0 {
		t.Fatalf("expected 0 notification_deliveries rows for Inbox-only evening publication, got %d", ndCount)
	}

	// APNs delivery must be enqueued ONLY for the v2 device, NEVER for v1 (TestFlight 33).
	var pushDeviceIDs []uuid.UUID
	rows, err := db.pool.Query(ctx, `SELECT device_id FROM push_deliveries WHERE inbox_item_id = $1`, inboxID)
	if err != nil {
		t.Fatalf("query push_deliveries: %v", err)
	}
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		pushDeviceIDs = append(pushDeviceIDs, id)
	}
	rows.Close()
	if len(pushDeviceIDs) != 1 || pushDeviceIDs[0] != devV2.DeviceID {
		t.Fatalf("expected push only for v2 device %s (not v1 %s), got %v", devV2.DeviceID, devV1.DeviceID, pushDeviceIDs)
	}

	// Anti-retroactivity: if Telegram already settled a date's evening report,
	// PublishEveningReportToInbox must skip without creating a duplicate card.
	settledKey := "report:evening:2026-11-27"
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO notification_deliveries(delivery_key, status, lease_token, reserved_at, completed_at)
		VALUES ($1, 'sent', $2, clock_timestamp(), clock_timestamp())`, settledKey, uuid.New()); err != nil {
		t.Fatalf("seed settled evening: %v", err)
	}
	hSettled := ReportHistory{
		DeliveryKey: &settledKey,
		Kind:        "evening",
		LocalDate:   "2026-11-27",
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "Evening 27",
	}
	sid, screated, err := db.PublishEveningReportToInbox(ctx, &hSettled, true)
	if err != nil || screated || sid != uuid.Nil {
		t.Fatalf("expected anti-retroactivity skip for settled evening, got id=%s created=%v err=%v", sid, screated, err)
	}
}

func TestStage3ProactivePublicationBudgetAndLastDeliveredGuidance(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	loc := time.UTC

	// Publish morning report to Inbox ONLY (no Telegram delivery attempts).
	mKey := "report:morning:2026-11-26"
	mh := ReportHistory{
		DeliveryKey: &mKey,
		Kind:        "morning",
		LocalDate:   "2026-11-26",
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "Morning body",
		Context:     json.RawMessage(`{"decision":{"mode":"moderate"}}`),
	}
	if _, _, err := db.PublishMorningReportToInbox(ctx, &mh, false); err != nil {
		t.Fatalf("publish morning: %v", err)
	}

	// LastDeliveredGuidance must see Inbox-only morning report when Inbox is enabled.
	guided, err := db.LastDeliveredGuidance(ctx, "2026-11-26", "morning")
	if err != nil || guided == nil || guided.ReportID != mh.ID {
		t.Fatalf("expected LastDeliveredGuidance to find Inbox morning report %s, got %+v err=%v", mh.ID, guided, err)
	}

	// Publish 1st proactive item at 10:00.
	t1 := time.Date(2026, 11, 26, 10, 0, 0, 0, loc)
	h1 := ReportHistory{Kind: "proactive", LocalDate: "2026-11-26", Timezone: "UTC", Language: "en", PlainBody: "Digest 1", FallbackReason: "weekly_digest"}
	id1, created1, err := db.PublishProactiveToInbox(ctx, &h1, "weekly_digest:2026-11-26", "proactive:weekly_digest:2026-11-26", nil, t1, loc, InboxKindWeeklyDigest, 24*time.Hour, true)
	if err != nil || !created1 || id1 == uuid.Nil {
		t.Fatalf("1st proactive publish failed: id=%s created=%v err=%v", id1, created1, err)
	}
	// Verify no dangling notification_deliveries row was created.
	var ndCount int
	_ = db.pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE delivery_key LIKE 'proactive:%'`).Scan(&ndCount)
	if ndCount != 0 {
		t.Fatalf("expected 0 notification_deliveries rows for Inbox-only proactive, got %d", ndCount)
	}

	// Backdate 1st inbox item created_at to 10:00 so cooldown check reflects simulated time.
	_, _ = db.pool.Exec(ctx, `UPDATE inbox_items SET created_at = $2 WHERE id = $1`, id1, t1)

	// Within 1h cooldown (10:30), 2nd proactive item must be suppressed.
	tCooldown := t1.Add(30 * time.Minute)
	hCool := ReportHistory{Kind: "proactive", LocalDate: "2026-11-26", Timezone: "UTC", Language: "en", PlainBody: "Nudge cool", FallbackReason: "energy_backfill"}
	idCool, createdCool, err := db.PublishProactiveToInbox(ctx, &hCool, "nudge:energy_backfill:2026-11-26", "proactive:energy_backfill:2026-11-26", nil, tCooldown, loc, InboxKindNudge, 24*time.Hour, true)
	if err != nil || createdCool || idCool != uuid.Nil {
		t.Fatalf("expected cooldown block at +30m, got id=%s created=%v err=%v", idCool, createdCool, err)
	}

	// After 1h cooldown (11:15), 2nd proactive item succeeds.
	t2 := t1.Add(75 * time.Minute)
	h2 := ReportHistory{Kind: "proactive", LocalDate: "2026-11-26", Timezone: "UTC", Language: "en", PlainBody: "Nudge 2", FallbackReason: "energy_backfill"}
	id2, created2, err := db.PublishProactiveToInbox(ctx, &h2, "nudge:energy_backfill:2026-11-26", "proactive:energy_backfill:2026-11-26", nil, t2, loc, InboxKindNudge, 24*time.Hour, true)
	if err != nil || !created2 || id2 == uuid.Nil {
		t.Fatalf("2nd proactive publish failed: id=%s created=%v err=%v", id2, created2, err)
	}
	_, _ = db.pool.Exec(ctx, `UPDATE inbox_items SET created_at = $2 WHERE id = $1`, id2, t2)

	// 3rd proactive item on the same day (13:00) must be blocked by daily limit <=2.
	t3 := t2.Add(2 * time.Hour)
	h3 := ReportHistory{Kind: "proactive", LocalDate: "2026-11-26", Timezone: "UTC", Language: "en", PlainBody: "Nudge 3", FallbackReason: "stress_validation_validated"}
	id3, created3, err := db.PublishProactiveToInbox(ctx, &h3, "nudge:stress_validation_validated:2026-11-26", "proactive:stress_validation_validated:2026-11-26", nil, t3, loc, InboxKindNudge, 24*time.Hour, true)
	if err != nil || created3 || id3 != uuid.Nil {
		t.Fatalf("expected daily limit block for 3rd proactive item, got id=%s created=%v err=%v", id3, created3, err)
	}
}

func TestStage3WakeFeedbackCanonicalAndTelegramIOSRaceBothOrders(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()

	// Order 1: Telegram answers first, then iOS tries a different answer -> Telegram wins, read_at set.
	d1 := "2026-11-25"
	proposed := json.RawMessage(`"2026-11-25T07:15:00Z"`)
	promptID1, _, created1, err := db.EnsureCanonicalWakeFeedbackPrompt(ctx, d1, proposed, WakeConfidenceHigh, time.Now().UTC())
	if err != nil || !created1 {
		t.Fatalf("ensure wake feedback d1: created=%v err=%v", created1, err)
	}
	tgRes, err := db.AnswerWakeFeedbackByDate(ctx, d1, d1, CheckinChannelTelegram, WakeFeedbackConfirmed, time.Now().UTC())
	if err != nil || tgRes.Status != CheckinAnswerStatusSaved || tgRes.Response != WakeFeedbackConfirmed {
		t.Fatalf("telegram answer d1: res=%+v err=%v", tgRes, err)
	}
	iosRes, err := db.AnswerWakeFeedbackByPromptID(ctx, promptID1, d1, CheckinChannelIOSApp, WakeFeedbackEarlier, time.Now().UTC())
	if err != nil || iosRes.Status != CheckinAnswerStatusAlreadyAnswered || iosRes.Response != WakeFeedbackConfirmed {
		t.Fatalf("ios second answer d1: res=%+v err=%v", iosRes, err)
	}
	item1, err := db.GetInboxItem(ctx, promptID1)
	if err != nil || item1.ReadAt == nil {
		t.Fatalf("expected inbox_items.read_at to be set after answer, got %+v err=%v", item1, err)
	}

	// High-confidence 7-day cadence check must detect inbox-channel feedback.
	recent, err := db.HasRecentDerivedMetricFeedback(DerivedMetricWakeTime, "", "2026-11-26", 7)
	if err != nil || !recent {
		t.Fatalf("expected HasRecentDerivedMetricFeedback(any channel) to be true, got %v err=%v", recent, err)
	}

	// Order 2: Mid-day transition (existing telegram row) -> iOS answers first -> Telegram gets iOS's choice.
	d2 := "2026-11-26"
	if _, err := db.SaveDerivedMetricFeedbackPrompted(DerivedMetricFeedback{
		MetricName:    DerivedMetricWakeTime,
		MetricDate:    d2,
		Channel:       DerivedMetricFeedbackTelegram,
		ProposedValue: json.RawMessage(`"2026-11-26T07:30:00Z"`),
		PromptedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy telegram wake feedback: %v", err)
	}
	promptID2, fb2, created2, err := db.EnsureCanonicalWakeFeedbackPrompt(ctx, d2, json.RawMessage(`"2026-11-26T07:30:00Z"`), WakeConfidenceHigh, time.Now().UTC())
	if err != nil || created2 || fb2.Channel != DerivedMetricFeedbackTelegram {
		t.Fatalf("expected reuse of legacy telegram row, got created=%v channel=%q err=%v", created2, fb2.Channel, err)
	}
	iosFirst, err := db.AnswerWakeFeedbackByPromptID(ctx, promptID2, d2, CheckinChannelIOSApp, WakeFeedbackLater, time.Now().UTC())
	if err != nil || iosFirst.Status != CheckinAnswerStatusSaved || iosFirst.Response != WakeFeedbackLater {
		t.Fatalf("ios first answer d2: res=%+v err=%v", iosFirst, err)
	}
	tgSecond, err := db.AnswerWakeFeedbackByDate(ctx, d2, d2, CheckinChannelTelegram, WakeFeedbackConfirmed, time.Now().UTC())
	if err != nil || tgSecond.Status != CheckinAnswerStatusAlreadyAnswered || tgSecond.Response != WakeFeedbackLater {
		t.Fatalf("telegram second answer d2: res=%+v err=%v", tgSecond, err)
	}

	// Cross-day guard returns ErrWakeFeedbackExpired without mutating.
	_, err = db.AnswerWakeFeedbackByPromptID(ctx, promptID2, "2026-11-27", CheckinChannelIOSApp, WakeFeedbackEarlier, time.Now().UTC())
	if !errors.Is(err, ErrWakeFeedbackExpired) {
		t.Fatalf("expected ErrWakeFeedbackExpired on cross-day tap, got %v", err)
	}
}

func TestStage3ContextPromptRaceAndRetentionPruning(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedContextPrompt := func(promptID, date string, createdAt, expiresAt time.Time) uuid.UUID {
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO context_prompt_interactions
				(prompt_id, signal_date, prompt_local_date, detected_reason, detector_version,
				 status, source, prompted_at, expires_at, allowed_categories, metadata, created_at, updated_at)
			VALUES ($1, $2::date, $2::date, 'low_sleep', 'low_sleep_v2',
			        'prompted', 'inbox', $3, $4, '["poor_sleep_context","stress_context","skip_context"]'::jsonb, '{}'::jsonb, $3, $3)`,
			promptID, date, createdAt, expiresAt); err != nil {
			t.Fatalf("seed context prompt %s: %v", promptID, err)
		}
		inboxID := uuid.New()
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO inbox_items (id, logical_key, kind, local_date, created_at, context_prompt_id, read_at)
			VALUES ($1, $2, 'prompt_context', $3, clock_timestamp(), $4, NULL)`,
			inboxID, ContextPromptInboxLogicalKey(promptID), date, promptID); err != nil {
			t.Fatalf("seed context inbox item %s: %v", promptID, err)
		}
		return inboxID
	}

	// Order 1: iOS answers first -> Telegram gets iOS's choice; read_at set atomically.
	inbox1 := seedContextPrompt("ctx_ios_first", "2026-11-25", now, now.Add(24*time.Hour))
	res1, err := db.AnswerContextPromptByInboxID(ctx, inbox1, CheckinChannelIOSApp, ContextPromptCategoryStress, now)
	if err != nil || res1.Status != CheckinAnswerStatusSaved || res1.Category != ContextPromptCategoryStress {
		t.Fatalf("ios context answer: res=%+v err=%v", res1, err)
	}
	tgRes1, err := db.AnswerContextPromptByPromptID(ctx, "ctx_ios_first", CheckinChannelTelegram, ContextPromptCategoryPoorSleep, now.Add(time.Minute))
	if err != nil || tgRes1.Status != CheckinAnswerStatusAlreadyAnswered || tgRes1.Category != ContextPromptCategoryStress {
		t.Fatalf("telegram repeat context answer: res=%+v err=%v", tgRes1, err)
	}
	item1, err := db.GetInboxItem(ctx, inbox1)
	if err != nil || item1.ReadAt == nil {
		t.Fatalf("expected context inbox item read_at set, got %+v err=%v", item1, err)
	}

	// Order 2: Telegram skips first -> iOS gets already_answered with skip_context.
	inbox2 := seedContextPrompt("ctx_tg_first", "2026-11-26", now, now.Add(24*time.Hour))
	tgRes2, err := db.AnswerContextPromptByPromptID(ctx, "ctx_tg_first", CheckinChannelTelegram, ContextPromptCategorySkip, now)
	if err != nil || tgRes2.Status != CheckinAnswerStatusSaved || tgRes2.EffectiveStatus != ContextPromptStatusSkipped {
		t.Fatalf("telegram skip context answer: res=%+v err=%v", tgRes2, err)
	}
	iosRes2, err := db.AnswerContextPromptByInboxID(ctx, inbox2, CheckinChannelIOSApp, ContextPromptCategoryStress, now.Add(time.Minute))
	if err != nil || iosRes2.Status != CheckinAnswerStatusAlreadyAnswered || iosRes2.Category != ContextPromptCategorySkip {
		t.Fatalf("ios repeat after telegram skip: res=%+v err=%v", iosRes2, err)
	}

	// Expired prompt returns ErrContextPromptExpired.
	inboxExp := seedContextPrompt("ctx_expired", "2026-11-24", now.Add(-48*time.Hour), now.Add(-1*time.Hour))
	_, err = db.AnswerContextPromptByInboxID(ctx, inboxExp, CheckinChannelWeb, ContextPromptCategoryPoorSleep, now)
	if !errors.Is(err, ErrContextPromptExpired) {
		t.Fatalf("expected ErrContextPromptExpired, got %v", err)
	}

	// Retention pruning with linked inbox_items + push_deliveries + push_delivery_attempts must succeed without FK errors.
	oldCreated := now.AddDate(0, 0, -200)
	oldInbox := seedContextPrompt("ctx_old_retention", "2026-04-01", oldCreated, oldCreated.Add(36*time.Hour))
	dev, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:             uuid.New(),
		APNsToken:            "3333333333333333333333333333333333333333333333333333333333333333",
		APNsEnv:              PushEnvSandbox,
		BundleID:             "com.dzarlax.health-sync",
		Locale:               "en",
		InboxContractVersion: InboxContractV2,
	})
	if err != nil {
		t.Fatalf("upsert device for retention test: %v", err)
	}
	delID := uuid.New()
	leaseID := uuid.New()
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO push_deliveries (
			id, delivery_key, inbox_item_id, kind, device_id, binding_id, registration_version,
			token_hash, apns_env, locale, apns_id, collapse_id, state, attempts, next_attempt_at,
			expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, 'prompt_context', $4, $5, 1, $6, 'sandbox', 'en', $7, 'cx-old', 'accepted', 1, $8, $8, $8, $8)`,
		delID, "push:prompt_context:old", oldInbox, dev.DeviceID, dev.BindingID, dev.TokenHash, uuid.New(), oldCreated); err != nil {
		t.Fatalf("seed old push delivery: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO push_delivery_attempts (id, delivery_id, lease_token, status, attempted_at, completed_at)
		VALUES ($1, $2, $3, 'accepted', $4, $4)`, uuid.New(), delID, leaseID, oldCreated); err != nil {
		t.Fatalf("seed old push attempt: %v", err)
	}

	pruned, err := db.PruneContextPromptInteractions(now)
	if err != nil {
		t.Fatalf("PruneContextPromptInteractions failed (possible FK block): %v", err)
	}
	if pruned != 1 {
		t.Fatalf("expected 1 pruned old context prompt, got %d", pruned)
	}
	var remainingOldInbox int
	_ = db.pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE id = $1`, oldInbox).Scan(&remainingOldInbox)
	if remainingOldInbox != 0 {
		t.Fatalf("expected old inbox item removed during retention pruning, got %d", remainingOldInbox)
	}
}

func TestStage3ReadAtAndReadAllWatermarkSemantics(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()

	publishMorning := func(date string) uuid.UUID {
		k := "report:morning:" + date
		h := ReportHistory{DeliveryKey: &k, Kind: "morning", LocalDate: date, Timezone: "UTC", Language: "en", PlainBody: "Report " + date}
		id, _, err := db.PublishMorningReportToInbox(ctx, &h, false)
		if err != nil {
			t.Fatalf("publish %s: %v", date, err)
		}
		return id
	}

	id1 := publishMorning("2026-11-24")
	id2 := publishMorning("2026-11-25")

	// Mark item 1 read idempotently.
	readRes, err := db.MarkInboxItemRead(ctx, id1, InboxContractV2)
	if err != nil || readRes.UnreadCount != 1 {
		t.Fatalf("MarkInboxItemRead: res=%+v err=%v", readRes, err)
	}

	// Capture server snapshot_at while only id1 and id2 exist (page limit=1 leaves id2 on older page or vice versa).
	_, snapshotAt, err := db.ListInboxItemsWithSnapshot(ctx, InboxQuery{Limit: 1, ContractVersion: InboxContractV2})
	if err != nil {
		t.Fatalf("ListInboxItemsWithSnapshot: %v", err)
	}

	// Ensure strictly later timestamp for item 3 published after snapshot_at.
	time.Sleep(15 * time.Millisecond)
	id3 := publishMorning("2026-11-26")

	// Mark all read up to snapshotAt: id2 (<= snapshotAt) MUST be marked read;
	// id3 (> snapshotAt) MUST stay unread!
	allRes, err := db.MarkAllInboxRead(ctx, &snapshotAt, InboxContractV2)
	if err != nil {
		t.Fatalf("MarkAllInboxRead: %v", err)
	}
	if allRes.Updated != 1 || allRes.UnreadCount != 1 {
		t.Fatalf("expected Updated=1 (id2) and UnreadCount=1 (id3), got %+v", allRes)
	}
	item2, _ := db.GetInboxItem(ctx, id2)
	item3, _ := db.GetInboxItem(ctx, id3)
	if item2.ReadAt == nil {
		t.Fatalf("expected id2 (<= snapshotAt) to be marked read")
	}
	if item3.ReadAt != nil {
		t.Fatalf("expected id3 (> snapshotAt) to stay unread, got read_at=%v", item3.ReadAt)
	}
}

func TestStage3PushRegistrationAttemptV22ToV23ReplayCompatibility(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()

	deviceID := uuid.New()
	bindingID := uuid.New()
	attemptID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	token := "4444444444444444444444444444444444444444444444444444444444444444"
	tokenHash := sha256.Sum256([]byte(token))
	// Exact pre-migration v22 4-field request hash (before inbox_contract_version existed).
	v22Hash := sha256.Sum256([]byte(strings.Join([]string{token, PushEnvSandbox, "com.dzarlax.health-sync", "ru"}, "\x00")))

	// Seed a v22-era push_devices + push_registration_attempts pair as it exists
	// right after ALTER TABLE push_devices ADD COLUMN inbox_contract_version DEFAULT 1.
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO push_devices (
			device_id, binding_id, apns_token, token_hash, apns_env, bundle_id, locale,
			enabled, registration_version, inbox_contract_version, last_seen_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, 3, 1, clock_timestamp(), clock_timestamp(), clock_timestamp())`,
		deviceID, bindingID, token, tokenHash[:], PushEnvSandbox, "com.dzarlax.health-sync", "ru",
	); err != nil {
		t.Fatalf("seed v22 push_devices row: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO push_registration_attempts (
			id, device_id, request_hash, binding_id, registration_version, apns_env, created_at
		) VALUES ($1, $2, $3, $4, 3, $5, clock_timestamp())`,
		attemptID, deviceID, v22Hash[:], bindingID, PushEnvSandbox,
	); err != nil {
		t.Fatalf("seed v22 push_registration_attempts row: %v", err)
	}

	// TestFlight 33 retries the lost POST after migration (inbox_contract_version omitted / 0).
	replayed, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:              deviceID,
		APNsToken:             token,
		APNsEnv:               PushEnvSandbox,
		BundleID:              "com.dzarlax.health-sync",
		Locale:                "ru",
		InboxContractVersion:  0,
		RegistrationAttemptID: attemptID,
	})
	if err != nil {
		t.Fatalf("expected v22 TestFlight 33 replay to succeed after v23 migration, got: %v", err)
	}
	if replayed.BindingID != bindingID || replayed.RegistrationVersion != 3 || replayed.InboxContractVersion != InboxContractV1 {
		t.Fatalf("unexpected replayed device: %+v", replayed)
	}

	// Explicit inbox_contract_version=1 replay must also match the v22 receipt.
	replayedExplicitV1, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:              deviceID,
		APNsToken:             token,
		APNsEnv:               PushEnvSandbox,
		BundleID:              "com.dzarlax.health-sync",
		Locale:                "ru",
		InboxContractVersion:  InboxContractV1,
		RegistrationAttemptID: attemptID,
	})
	if err != nil || replayedExplicitV1.BindingID != bindingID {
		t.Fatalf("expected explicit v1 replay to succeed: dev=%+v err=%v", replayedExplicitV1, err)
	}

	// Replaying the same attempt_id with inbox_contract_version=2 must fail with 409 Conflict.
	_, err = db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:              deviceID,
		APNsToken:             token,
		APNsEnv:               PushEnvSandbox,
		BundleID:              "com.dzarlax.health-sync",
		Locale:                "ru",
		InboxContractVersion:  InboxContractV2,
		RegistrationAttemptID: attemptID,
	})
	if !errors.Is(err, ErrPushRegistrationAttemptConflict) {
		t.Fatalf("expected ErrPushRegistrationAttemptConflict for v2 payload on v1 attempt, got %v", err)
	}
}

func TestStage3ListRetryableInboxTelegramReportsBypassesGatesAndRespectsAmbiguous(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	// 1. Evening report -> failed (provider_rejected): MUST be retryable.
	evKey := "report:evening:" + today
	ev := ReportHistory{DeliveryKey: &evKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening"}
	if id, _, err := db.PublishEveningReportToInbox(ctx, &ev, true); err != nil || id == uuid.Nil {
		t.Fatalf("publish evening: id=%s err=%v", id, err)
	}
	evToken, ok, err := db.ReserveReportDeliveryForExisting(ctx, ev.ID)
	if err != nil || !ok {
		t.Fatalf("reserve evening: ok=%v err=%v", ok, err)
	}
	evAtt, err := db.ReserveReportAttempt(ctx, ev.ID, "html", ev.PlainBody, evToken)
	if err != nil {
		t.Fatalf("reserve evening attempt: %v", err)
	}
	_ = db.CompleteReportAttempt(ctx, evAtt, "failed", "provider_rejected", nil)
	_ = db.CompleteNotificationDelivery(ctx, evKey, evToken, "failed", "provider_rejected")

	// 2. Sleep update -> ambiguous: MUST NEVER be listed or reserved.
	su := ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Sleep update"}
	if id, _, err := db.PublishProactiveToInbox(ctx, &su, "sleep_update:ep1:1", "proactive:daytime:ep1:1", nil, now, time.UTC, InboxKindSleepUpdate, 8*time.Hour, true); err != nil || id == uuid.Nil {
		t.Fatalf("publish sleep update: id=%s err=%v", id, err)
	}
	suToken, ok, err := db.ReserveReportDeliveryForExisting(ctx, su.ID)
	if err != nil || !ok {
		t.Fatalf("reserve sleep update: ok=%v err=%v", ok, err)
	}
	_ = db.CompleteNotificationDelivery(ctx, *su.DeliveryKey, suToken, "ambiguous", "transport_ambiguous")

	// 3. Weekly digest -> failed with telegram_disabled: MUST NOT be listed or reserved.
	wd := ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Digest"}
	if id, _, err := db.PublishProactiveToInbox(ctx, &wd, "weekly_digest:"+today, "proactive:weekly_digest:"+today, nil, now.Add(2*time.Hour), time.UTC, InboxKindWeeklyDigest, 24*time.Hour, true); err != nil || id == uuid.Nil {
		t.Fatalf("publish digest: id=%s err=%v", id, err)
	}
	wdToken, ok, err := db.ReserveReportDeliveryForExisting(ctx, wd.ID)
	if err != nil || !ok {
		t.Fatalf("reserve digest: ok=%v err=%v", ok, err)
	}
	_ = db.CompleteNotificationDelivery(ctx, *wd.DeliveryKey, wdToken, "failed", "telegram_disabled")

	if _, reservedDisabled, err := db.ReserveReportDeliveryForExisting(ctx, wd.ID); err != nil || reservedDisabled {
		t.Fatalf("expected telegram_disabled delivery not to be re-reserved: reserved=%v err=%v", reservedDisabled, err)
	}

	retryable, err := db.ListRetryableInboxTelegramReports(ctx, today, nil, 3, 20)
	if err != nil {
		t.Fatalf("ListRetryableInboxTelegramReports: %v", err)
	}
	if len(retryable) != 1 || retryable[0].ID != ev.ID {
		t.Fatalf("expected only failed evening report (%s) to be retryable, got %+v", ev.ID, retryable)
	}
}

func TestStage3ProviderRejectedCapEnforcedUnderLockInReserveReportDeliveryForExisting(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")

	evKey := "report:evening:" + today
	ev := ReportHistory{DeliveryKey: &evKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening"}
	if id, _, err := db.PublishEveningReportToInbox(ctx, &ev, true); err != nil || id == uuid.Nil {
		t.Fatalf("publish evening: id=%s err=%v", id, err)
	}

	// Cycle 1: rich + html both fail with provider_rejected under the SAME lease_token (counts as 1 cycle).
	tok1, ok, err := db.ReserveReportDeliveryForExisting(ctx, ev.ID)
	if err != nil || !ok {
		t.Fatalf("cycle 1 reserve: ok=%v err=%v", ok, err)
	}
	a1Rich, err := db.ReserveReportAttempt(ctx, ev.ID, "rich", "<b>Evening</b>", tok1)
	if err != nil {
		t.Fatalf("cycle 1 rich attempt: %v", err)
	}
	_ = db.CompleteReportAttempt(ctx, a1Rich, "failed", "provider_rejected", nil)
	a1HTML, err := db.ReserveReportAttempt(ctx, ev.ID, "html", "Evening", tok1)
	if err != nil {
		t.Fatalf("cycle 1 html attempt: %v", err)
	}
	_ = db.CompleteReportAttempt(ctx, a1HTML, "failed", "provider_rejected", nil)
	_ = db.CompleteNotificationDelivery(ctx, evKey, tok1, "failed", "provider_rejected")

	// Cycle 2: fails with provider_rejected.
	tok2, ok, err := db.ReserveReportDeliveryForExisting(ctx, ev.ID)
	if err != nil || !ok {
		t.Fatalf("cycle 2 reserve: ok=%v err=%v", ok, err)
	}
	a2, err := db.ReserveReportAttempt(ctx, ev.ID, "html", "Evening", tok2)
	if err != nil {
		t.Fatalf("cycle 2 attempt: %v", err)
	}
	_ = db.CompleteReportAttempt(ctx, a2, "failed", "provider_rejected", nil)
	_ = db.CompleteNotificationDelivery(ctx, evKey, tok2, "failed", "provider_rejected")

	// Two callers both observe the report as retryable right before cycle 3.
	batchA, err := db.ListRetryableInboxTelegramReports(ctx, today, []string{InboxKindEveningReport}, MaxReportProviderRejectionCycles, 10)
	if err != nil || len(batchA) != 1 {
		t.Fatalf("batchA before cycle 3: len=%d err=%v", len(batchA), err)
	}
	batchB, err := db.ListRetryableInboxTelegramReports(ctx, today, []string{InboxKindEveningReport}, MaxReportProviderRejectionCycles, 10)
	if err != nil || len(batchB) != 1 {
		t.Fatalf("batchB before cycle 3: len=%d err=%v", len(batchB), err)
	}

	// Caller A executes cycle 3 and fails with provider_rejected.
	tok3, ok, err := db.ReserveReportDeliveryForExisting(ctx, batchA[0].ID)
	if err != nil || !ok {
		t.Fatalf("cycle 3 reserve: ok=%v err=%v", ok, err)
	}
	a3, err := db.ReserveReportAttempt(ctx, batchA[0].ID, "html", "Evening", tok3)
	if err != nil {
		t.Fatalf("cycle 3 attempt: %v", err)
	}
	_ = db.CompleteReportAttempt(ctx, a3, "failed", "provider_rejected", nil)
	_ = db.CompleteNotificationDelivery(ctx, evKey, tok3, "failed", "provider_rejected")

	// Caller B (holding the stale report_id selected before cycle 3) tries to reserve:
	// ReserveReportDeliveryForExisting MUST enforce the cap under FOR UPDATE lock and refuse!
	if _, ok4, err := db.ReserveReportDeliveryForExisting(ctx, batchB[0].ID); err != nil || ok4 {
		t.Fatalf("expected 4th reservation after %d provider_rejected cycles to be denied, got ok=%v err=%v", MaxReportProviderRejectionCycles, ok4, err)
	}

	remaining, err := db.ListRetryableInboxTelegramReports(ctx, today, []string{InboxKindEveningReport}, MaxReportProviderRejectionCycles, 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("expected 0 retryable reports after hitting provider_rejected cap, got len=%d err=%v", len(remaining), err)
	}
}

func TestStage3OptedOutPublicationSuppressesExistingLegacyFailures(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	eveningKey := "report:evening:" + today
	proactiveKey := "proactive:weekly_digest:" + today
	for _, key := range []string{eveningKey, proactiveKey} {
		lease, reserved, err := db.ReserveNotificationDelivery(ctx, key)
		if err != nil || !reserved {
			t.Fatalf("reserve legacy delivery: reserved=%v err=%v", reserved, err)
		}
		if err := db.CompleteNotificationDelivery(ctx, key, lease, "failed", "provider_rejected"); err != nil {
			t.Fatal(err)
		}
	}

	evening := ReportHistory{Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening", DeliveryKey: &eveningKey}
	if _, created, err := db.PublishEveningReportToInbox(ctx, &evening, false); err != nil || !created {
		t.Fatalf("publish evening: created=%v err=%v", created, err)
	}
	proactive := ReportHistory{Kind: "proactive", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Digest"}
	if _, created, err := db.PublishProactiveToInbox(ctx, &proactive, "weekly_digest:"+today, proactiveKey, nil, now, time.UTC, InboxKindWeeklyDigest, time.Hour, false); err != nil || !created {
		t.Fatalf("publish digest: created=%v err=%v", created, err)
	}
	rows, err := db.ListRetryableInboxTelegramReports(ctx, today, nil, MaxReportProviderRejectionCycles, 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("expected neither opted-out legacy failure to be retried, got %d: %v", len(rows), err)
	}
	for _, reportID := range []uuid.UUID{evening.ID, proactive.ID} {
		if _, reserved, err := db.ReserveReportDeliveryForExisting(ctx, reportID); err != nil || reserved {
			t.Fatalf("opt-out must also block direct reservation: reserved=%v err=%v", reserved, err)
		}
	}
}

func TestStage3ConcurrentTelegramAndInboxWakePublicationKeepsOneProposal(t *testing.T) {
	db := setupStage3DB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	type result struct {
		fb  DerivedMetricFeedback
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	go func() {
		<-start
		_, fb, _, err := db.EnsureCanonicalWakeFeedbackPrompt(ctx, today, json.RawMessage(`"2026-11-26T07:15:00Z"`), "high", now)
		results <- result{fb, err}
	}()
	go func() {
		<-start
		fb, err := db.EnsureTelegramWakeFeedback(ctx, today, json.RawMessage(`"2026-11-26T08:10:00Z"`), "high", now)
		results <- result{fb, err}
	}()
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("publish feedback: %v / %v", first.err, second.err)
	}
	if first.fb.Channel != second.fb.Channel || string(first.fb.ProposedValue) != string(second.fb.ProposedValue) {
		t.Fatalf("publication paths disagreed on proposal: %s/%s vs %s/%s", first.fb.Channel, first.fb.ProposedValue, second.fb.Channel, second.fb.ProposedValue)
	}
	var rows int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM derived_metric_feedback WHERE metric_name=$1 AND metric_date=$2::date`, DerivedMetricWakeTime, today).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("expected one canonical row, got %d: %v", rows, err)
	}
}
