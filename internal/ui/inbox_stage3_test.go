package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/notify"
	"health-receiver/internal/storage"
)

func setupStage3UIHandler(t *testing.T) (*storage.DB, string, *Handler) {
	t.Helper()
	db, schema, cleanup := testTenantDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatalf("migrate v23: %v", err)
	}
	if err := db.SaveSettings(map[string]string{
		"timezone":                       "UTC",
		"report_lang":                    "en",
		storage.SettingInAppInboxEnabled: "true",
		storage.SettingAPNSPushEnabled:   "true",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return db, schema, &Handler{}
}

func TestInboxContractV1CompatibilityAndV2Endpoints(t *testing.T) {
	db, schema, h := setupStage3UIHandler(t)
	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")

	// Seed morning_report, evening_report, prompt_checkin, prompt_wake_feedback.
	mKey := "report:morning:" + today
	mh := storage.ReportHistory{DeliveryKey: &mKey, Kind: "morning", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Morning text"}
	mID, _, err := db.PublishMorningReportToInbox(ctx, &mh, false)
	if err != nil {
		t.Fatalf("publish morning: %v", err)
	}
	eKey := "report:evening:" + today
	eh := storage.ReportHistory{DeliveryKey: &eKey, Kind: "evening", LocalDate: today, Timezone: "UTC", Language: "en", PlainBody: "Evening text"}
	eID, _, err := db.PublishEveningReportToInbox(ctx, &eh, false)
	if err != nil {
		t.Fatalf("publish evening: %v", err)
	}
	ciID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, today, time.Now().UTC(), time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ensure checkin: %v", err)
	}
	wfID, _, _, err := db.EnsureCanonicalWakeFeedbackPrompt(ctx, today, json.RawMessage(`"2026-11-26T07:15:00Z"`), storage.WakeConfidenceHigh, time.Now().UTC())
	if err != nil {
		t.Fatalf("ensure wake feedback: %v", err)
	}

	// 1. TestFlight 33 (no X-Health-Inbox-Contract header -> v1):
	// Must see ONLY morning_report and prompt_checkin; evening_report detail returns 404.
	reqV1 := httptest.NewRequest(http.MethodGet, "/api/inbox", nil).WithContext(adminContext(db, schema))
	recV1 := httptest.NewRecorder()
	h.inboxList(recV1, reqV1)
	if recV1.Code != http.StatusOK {
		t.Fatalf("GET /api/inbox (v1) status=%d body=%s", recV1.Code, recV1.Body.String())
	}
	var feedV1 clientapi.InboxFeedResponse
	if err := json.Unmarshal(recV1.Body.Bytes(), &feedV1); err != nil {
		t.Fatalf("decode v1 feed: %v", err)
	}
	if len(feedV1.Items) != 2 || feedV1.UnreadCount != nil {
		t.Fatalf("expected 2 v1 items without unread_count, got %d unread=%v", len(feedV1.Items), feedV1.UnreadCount)
	}
	for _, it := range feedV1.Items {
		if it.Kind != storage.InboxKindMorningReport && it.Kind != storage.InboxKindPromptCheckin {
			t.Fatalf("unexpected v2 kind %q leaked to v1 client", it.Kind)
		}
	}
	recEvV1 := httptest.NewRecorder()
	reqEvV1 := httptest.NewRequest(http.MethodGet, "/api/inbox/"+eID.String(), nil).WithContext(adminContext(db, schema))
	reqEvV1.SetPathValue("id", eID.String())
	h.inboxDetail(recEvV1, reqEvV1)
	if recEvV1.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for v2 item on v1 client, got %d", recEvV1.Code)
	}
	recPendV1 := httptest.NewRecorder()
	h.inboxPending(recPendV1, httptest.NewRequest(http.MethodGet, "/api/inbox/pending", nil).WithContext(adminContext(db, schema)))
	var pendV1 clientapi.InboxPendingResponse
	_ = json.Unmarshal(recPendV1.Body.Bytes(), &pendV1)
	if len(pendV1.Prompts) != 1 || pendV1.Prompts[0].PromptID != ciID {
		t.Fatalf("expected only prompt_checkin for v1 pending, got %+v", pendV1.Prompts)
	}

	// 2. Contract v2 client sees all 4 items, unread_count=4, snapshot_at, and both pending prompts.
	reqV2 := httptest.NewRequest(http.MethodGet, "/api/inbox", nil).WithContext(adminContext(db, schema))
	reqV2.Header.Set("X-Health-Inbox-Contract", "2")
	recV2 := httptest.NewRecorder()
	h.inboxList(recV2, reqV2)
	if recV2.Code != http.StatusOK {
		t.Fatalf("GET /api/inbox (v2) status=%d", recV2.Code)
	}
	var feedV2 clientapi.InboxFeedResponse
	_ = json.Unmarshal(recV2.Body.Bytes(), &feedV2)
	if len(feedV2.Items) != 4 || feedV2.UnreadCount == nil || *feedV2.UnreadCount != 4 || feedV2.SnapshotAt == nil {
		t.Fatalf("expected 4 v2 items, unread_count=4, snapshot_at; got %+v", feedV2)
	}

	// 3. Mark morning_report read -> unread_count drops to 3.
	reqRead := httptest.NewRequest(http.MethodPost, "/api/inbox/"+mID.String()+"/read", nil).WithContext(adminContext(db, schema))
	reqRead.Header.Set("X-Health-Inbox-Contract", "2")
	reqRead.SetPathValue("id", mID.String())
	recRead := httptest.NewRecorder()
	h.inboxMarkRead(recRead, reqRead)
	if recRead.Code != http.StatusOK {
		t.Fatalf("POST /api/inbox/{id}/read status=%d body=%s", recRead.Code, recRead.Body.String())
	}
	var readResp clientapi.InboxReadResponse
	_ = json.Unmarshal(recRead.Body.Bytes(), &readResp)
	if readResp.UnreadCount != 3 {
		t.Fatalf("expected unread_count=3 after markRead, got %d", readResp.UnreadCount)
	}

	// 4. Answer wake_feedback via unified POST /api/prompts/{id}/answer -> sets read_at, unread_count=2.
	ansBody, _ := json.Marshal(clientapi.PromptAnswerRequest{Answer: storage.WakeFeedbackConfirmed, Channel: storage.CheckinChannelIOSApp})
	reqAns := httptest.NewRequest(http.MethodPost, "/api/prompts/"+wfID.String()+"/answer", bytes.NewReader(ansBody)).WithContext(adminContext(db, schema))
	reqAns.Header.Set("X-Health-Inbox-Contract", "2")
	reqAns.SetPathValue("prompt_id", wfID.String())
	recAns := httptest.NewRecorder()
	h.promptAnswer(recAns, reqAns)
	if recAns.Code != http.StatusOK {
		t.Fatalf("POST wake feedback answer status=%d body=%s", recAns.Code, recAns.Body.String())
	}
	var ansResp clientapi.PromptAnswerResponse
	_ = json.Unmarshal(recAns.Body.Bytes(), &ansResp)
	if ansResp.Status != storage.CheckinAnswerStatusSaved || ansResp.Answer != storage.WakeFeedbackConfirmed || ansResp.UnreadCount == nil || *ansResp.UnreadCount != 2 {
		t.Fatalf("unexpected wake feedback answer response: %+v", ansResp)
	}

	// 5. POST /api/inbox/read-all with snapshot_at marks remaining 2 read -> unread_count=0.
	readAllBody, _ := json.Marshal(clientapi.InboxReadAllRequest{Before: feedV2.SnapshotAt})
	reqAll := httptest.NewRequest(http.MethodPost, "/api/inbox/read-all", bytes.NewReader(readAllBody)).WithContext(adminContext(db, schema))
	reqAll.Header.Set("X-Health-Inbox-Contract", "2")
	recAll := httptest.NewRecorder()
	h.inboxReadAll(recAll, reqAll)
	if recAll.Code != http.StatusOK {
		t.Fatalf("POST /api/inbox/read-all status=%d body=%s", recAll.Code, recAll.Body.String())
	}
	var allResp clientapi.InboxReadAllResponse
	_ = json.Unmarshal(recAll.Body.Bytes(), &allResp)
	if allResp.UnreadCount != 0 || allResp.Updated != 2 {
		t.Fatalf("expected updated=2 unread=0, got %+v", allResp)
	}
}

func TestNotificationPreferencesAndTelegramOptOutBarrier(t *testing.T) {
	db, schema, h := setupStage3UIHandler(t)
	ctx := context.Background()

	// Seed Telegram credentials so telegram_configured=true.
	if err := db.SaveSettings(map[string]string{
		"telegram_token":   "123456:fake-bot-token",
		"telegram_chat_id": "987654321",
	}); err != nil {
		t.Fatalf("seed telegram creds: %v", err)
	}

	// GET preferences returns defaults (all push prefs true, telegram_notifications_enabled true).
	recGet := httptest.NewRecorder()
	h.notificationPreferencesGet(recGet, httptest.NewRequest(http.MethodGet, "/api/notifications/preferences", nil).WithContext(adminContext(db, schema)))
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET /api/notifications/preferences status=%d body=%s", recGet.Code, recGet.Body.String())
	}
	var prefs clientapi.NotificationPreferencesResponse
	_ = json.Unmarshal(recGet.Body.Bytes(), &prefs)
	if !prefs.TelegramConfigured || !prefs.TelegramNotificationsEnabled || !prefs.PushDigestsNudges || !prefs.PushEveningReport {
		t.Fatalf("unexpected default preferences: %+v", prefs)
	}

	// PUT disables Telegram notifications and evening pushes.
	f := false
	putBody, _ := json.Marshal(clientapi.NotificationPreferencesRequest{
		TelegramNotificationsEnabled: &f,
		PushEveningReport:            &f,
	})
	recPut := httptest.NewRecorder()
	h.notificationPreferencesPut(recPut, httptest.NewRequest(http.MethodPut, "/api/notifications/preferences", bytes.NewReader(putBody)).WithContext(adminContext(db, schema)))
	if recPut.Code != http.StatusOK {
		t.Fatalf("PUT /api/notifications/preferences status=%d body=%s", recPut.Code, recPut.Body.String())
	}
	var updated clientapi.NotificationPreferencesResponse
	_ = json.Unmarshal(recPut.Body.Bytes(), &updated)
	if updated.TelegramNotificationsEnabled || updated.PushEveningReport || !updated.TelegramConfigured {
		t.Fatalf("unexpected updated preferences: %+v", updated)
	}

	// Send-time barrier: DeliverArchivedReportToTelegram must NOT call Telegram HTTP when disabled!
	tgCalls := 0
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tgCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer tgServer.Close()
	restore := notify.SetTelegramAPIBaseForTest(tgServer.URL)
	defer restore()

	key := "report:evening:2026-11-26"
	eh := storage.ReportHistory{DeliveryKey: &key, Kind: "evening", LocalDate: "2026-11-26", Timezone: "UTC", Language: "en", PlainBody: "Evening"}
	if _, _, err := db.PublishEveningReportToInbox(ctx, &eh, true); err != nil {
		t.Fatalf("publish evening: %v", err)
	}
	bot := notify.NewBot("123456:fake-bot-token", "987654321")
	cfg := notify.Config{Token: "123456:fake-bot-token", ChatID: "987654321", Lang: "en", Timezone: "UTC"}
	// Even with a stale cfg where TelegramDisabled=false, live DB barrier must block send!
	sent, err := notify.DeliverArchivedReportToTelegram(db, bot, cfg, &eh)
	if err != nil || sent || tgCalls != 0 {
		t.Fatalf("expected live Telegram barrier to block send (sent=%v err=%v tgCalls=%d)", sent, err, tgCalls)
	}

	// Routine previews must return ErrTelegramNotificationsDisabled when flag is off.
	if err := notify.SendEveningPreview(bot, db, cfg); err != notify.ErrTelegramNotificationsDisabled {
		t.Fatalf("expected ErrTelegramNotificationsDisabled for SendEveningPreview, got %v", err)
	}
	if err := notify.SendWeeklyDigestForce(bot, db, cfg, 7); err != notify.ErrTelegramNotificationsDisabled {
		t.Fatalf("expected ErrTelegramNotificationsDisabled for SendWeeklyDigestForce, got %v", err)
	}
	if tgCalls != 0 {
		t.Fatalf("expected 0 Telegram HTTP calls while disabled, got %d", tgCalls)
	}

	_ = strings.Repeat("a", 64)
	_ = uuid.New()
}
