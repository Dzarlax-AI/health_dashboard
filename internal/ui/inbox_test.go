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
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestInboxEndpointsFeedPendingDetailAndAnswers(t *testing.T) {
	db, schema, cleanup := testTenantDB(t)
	defer cleanup()

	ctx := context.Background()
	dsn := testdb.DSN(t)
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	otherSchema := testdb.SchemaName("inbox_other")
	if err := testdb.CreateSchema(ctx, bootstrap, otherSchema); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = testdb.DropSchema(context.Background(), bootstrap, otherSchema) }()
	otherPool, err := testdb.NewPool(ctx, dsn, otherSchema)
	if err != nil {
		t.Fatal(err)
	}
	otherDB := storage.NewFromPool(otherPool)
	defer otherDB.Close()

	if err := db.EnsureReportHistoryTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureInboxItemsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := otherDB.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{"timezone": "UTC", "report_lang": "en"}); err != nil {
		t.Fatal(err)
	}
	if err := otherDB.SaveSettings(map[string]string{"timezone": "UTC", "report_lang": "en", storage.SettingInAppInboxEnabled: "true"}); err != nil {
		t.Fatal(err)
	}

	// Default rollout state: Inbox is disabled until explicitly opted in for a profile.
	t.Setenv("IN_APP_INBOX_ENABLED", "")
	h := &Handler{}
	wDefault := httptest.NewRecorder()
	rDefault := httptest.NewRequest(http.MethodGet, "/api/inbox/pending", nil).WithContext(adminContext(db, schema))
	h.inboxPending(wDefault, rDefault)
	if wDefault.Code != http.StatusNotFound {
		t.Fatalf("default profile without in_app_inbox_enabled status=%d, want 404", wDefault.Code)
	}

	// Verify empty/invalid timezone uses the same time.Local boundary as scheduler and Telegram.
	if err := db.SaveSettings(map[string]string{"timezone": "Invalid/Zone"}); err != nil {
		t.Fatal(err)
	}
	checkNow := time.Now()
	if got, want := h.inboxToday(db, schema, checkNow), checkNow.In(time.Local).Format("2006-01-02"); got != want {
		t.Fatalf("inboxToday with invalid tz = %q, want time.Local date %q", got, want)
	}

	// Explicitly opt this profile into Inbox for the remainder of the test.
	if err := db.SaveSettings(map[string]string{"timezone": "UTC", "report_lang": "en", storage.SettingInAppInboxEnabled: "true"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	// Publish today's morning report + active check-in + yesterday's expired check-in.
	key := "report:morning:" + today
	report := &storage.ReportHistory{
		DeliveryKey: &key,
		Kind:        "morning",
		LocalDate:   today,
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "<b>Morning Report</b><br>Sleep: 7h 40m &amp; solid.",
	}
	reportInboxID, _, err := db.PublishMorningReportToInbox(ctx, report, false)
	if err != nil {
		t.Fatal(err)
	}
	promptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, today, now.Add(-5*time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	oldPromptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, yesterday, now.Add(-25*time.Hour), now.Add(-23*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	refreshCount := 0
	h.ConfigureCheckinRefresh(func(_ *storage.DB, _ string) { refreshCount++ })

	// 1. GET /api/inbox with pagination limit=1
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/inbox?limit=1", nil).WithContext(adminContext(db, schema))
	h.inboxList(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("inboxList status=%d body=%s", w.Code, w.Body.String())
	}
	var page1 clientapi.InboxFeedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 1 || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v, want 1 item and next_cursor", page1)
	}

	// 2. GET /api/inbox/{id} for morning report normalizes HTML to plain text
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/inbox/"+reportInboxID.String(), nil).WithContext(adminContext(db, schema))
	r.SetPathValue("id", reportInboxID.String())
	h.inboxDetail(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("inboxDetail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail clientapi.InboxDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail.Body, "<b>") || !strings.Contains(detail.Body, "Sleep: 7h 40m & solid.") {
		t.Fatalf("unexpected normalized body: %q", detail.Body)
	}

	// 3. Foreign tenant gets 404 on detail and answer
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/inbox/"+reportInboxID.String(), nil).WithContext(adminContext(otherDB, otherSchema))
	r.SetPathValue("id", reportInboxID.String())
	h.inboxDetail(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign detail status=%d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+promptID.String()+"/answer", bytes.NewBufferString(`{"answer":"great","channel":"web"}`)).WithContext(adminContext(otherDB, otherSchema))
	r.SetPathValue("prompt_id", promptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign answer status=%d, want 404", w.Code)
	}

	// 4. GET /api/inbox/pending returns only today's active prompt with 4 ordered options
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/inbox/pending?lang=en", nil).WithContext(adminContext(db, schema))
	h.inboxPending(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("inboxPending status=%d body=%s", w.Code, w.Body.String())
	}
	var pending clientapi.InboxPendingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending.Prompts) != 1 || pending.Prompts[0].PromptID != promptID || len(pending.Prompts[0].Options) != 4 {
		t.Fatalf("pending = %+v", pending)
	}

	// 5. Rejects channel=telegram with 400
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+promptID.String()+"/answer", bytes.NewBufferString(`{"answer":"great","channel":"telegram"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", promptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("telegram channel status=%d, want 400", w.Code)
	}

	// 6. Yesterday's prompt reports expired with no options on detail and returns 409 {"status":"expired"} on answer
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/inbox/"+oldPromptID.String()+"?lang=en", nil).WithContext(adminContext(db, schema))
	r.SetPathValue("id", oldPromptID.String())
	h.inboxDetail(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("old prompt detail status=%d body=%s", w.Code, w.Body.String())
	}
	var oldDetail clientapi.InboxDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &oldDetail); err != nil {
		t.Fatal(err)
	}
	if oldDetail.Status != storage.CheckinStatusExpired || len(oldDetail.Options) != 0 {
		t.Fatalf("old prompt detail = %+v, want status=expired and no options", oldDetail)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+oldPromptID.String()+"/answer", bytes.NewBufferString(`{"answer":"ok","channel":"ios_app"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", oldPromptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("cross-day status=%d, want 409; body=%s", w.Code, w.Body.String())
	}
	var expiredResp clientapi.PromptExpiredResponse
	if err := json.Unmarshal(w.Body.Bytes(), &expiredResp); err != nil || expiredResp.Status != storage.CheckinAnswerStatusExpired {
		t.Fatalf("expired body=%s", w.Body.String())
	}

	// 6b. Today's prompt expired after its 2h window still exposes options on detail so same-day late answers work
	if _, err := db.ExpireCanonicalCheckin(today, now.Add(3*time.Hour)); err != nil {
		t.Fatalf("expire today prompt: %v", err)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/inbox/"+promptID.String()+"?lang=en", nil).WithContext(adminContext(db, schema))
	r.SetPathValue("id", promptID.String())
	h.inboxDetail(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("same-day expired detail status=%d body=%s", w.Code, w.Body.String())
	}
	var sameDayExpiredDetail clientapi.InboxDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &sameDayExpiredDetail); err != nil {
		t.Fatal(err)
	}
	if sameDayExpiredDetail.Status != storage.CheckinStatusExpired || len(sameDayExpiredDetail.Options) != 4 {
		t.Fatalf("same-day expired detail = %+v, want status=expired with 4 options for late answer", sameDayExpiredDetail)
	}
	// Restore status to prompted before testing on-time answer below.
	schemaPool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatalf("schema pool: %v", err)
	}
	defer schemaPool.Close()
	if _, err := schemaPool.Exec(ctx, `UPDATE subjective_checkins SET status = 'prompted' WHERE date = $1`, today); err != nil {
		t.Fatalf("restore prompted status: %v", err)
	}

	// 7. First valid answer -> 200 saved + triggers refresh; repeat with different answer -> 200 already_answered + no second refresh
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+promptID.String()+"/answer", bytes.NewBufferString(`{"answer":"great","channel":"ios_app"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", promptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", w.Code, w.Body.String())
	}
	var ans1 clientapi.PromptAnswerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ans1); err != nil {
		t.Fatal(err)
	}
	if ans1.Status != storage.CheckinAnswerStatusSaved || ans1.Answer != storage.CheckinAnswerGreat || ans1.Timing != storage.CheckinTimingOnTime || refreshCount != 1 {
		t.Fatalf("ans1=%+v refreshCount=%d", ans1, refreshCount)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+promptID.String()+"/answer", bytes.NewBufferString(`{"answer":"sick","channel":"web"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", promptID.String())
	h.promptAnswer(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("repeat status=%d body=%s", w.Code, w.Body.String())
	}
	var ans2 clientapi.PromptAnswerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ans2); err != nil {
		t.Fatal(err)
	}
	if ans2.Status != storage.CheckinAnswerStatusAlreadyAnswered || ans2.Answer != storage.CheckinAnswerGreat || ans2.Timing != storage.CheckinTimingOnTime || refreshCount != 1 {
		t.Fatalf("ans2=%+v refreshCount=%d", ans2, refreshCount)
	}

	// 8. Unknown UUID returns 404
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/prompts/"+uuid.NewString()+"/answer", bytes.NewBufferString(`{"answer":"ok","channel":"web"}`)).WithContext(adminContext(db, schema))
	r.SetPathValue("prompt_id", uuid.NewString())
	h.promptAnswer(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown prompt status=%d, want 404", w.Code)
	}

	// 9. Disabling in_app_inbox_enabled hides inbox endpoints with 404
	if err := db.SaveSettings(map[string]string{storage.SettingInAppInboxEnabled: "false"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/inbox/pending", nil).WithContext(adminContext(db, schema))
	h.inboxPending(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled inbox status=%d, want 404", w.Code)
	}
}
