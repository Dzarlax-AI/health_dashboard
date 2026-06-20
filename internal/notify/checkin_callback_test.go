package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/storage"
)

type fakeRouter struct {
	saveStatus   string
	saveErr      error
	saveCalls    int
	ackCalls     int
	triggers     []string
	lastDate     string
	lastSource   string
	lastAnswer   string
	contextCalls int
	lastPromptID string
	lastCategory string
}

func (f *fakeRouter) SaveAnswer(date, source, answer string, _ time.Time) (string, error) {
	f.saveCalls++
	f.lastDate, f.lastSource, f.lastAnswer = date, source, answer
	return f.saveStatus, f.saveErr
}
func (f *fakeRouter) AnswerCallbackQuery(qid, text string) error { f.ackCalls++; return nil }
func (f *fakeRouter) TriggerReport(schema string)                { f.triggers = append(f.triggers, schema) }
func (f *fakeRouter) SaveContextPromptAnswer(promptID, category, source string, _ time.Time) (string, error) {
	f.contextCalls++
	f.lastPromptID, f.lastCategory, f.lastSource = promptID, category, source
	return f.saveStatus, f.saveErr
}

func buildUpdateBody(t *testing.T, chatID, callbackData string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"update_id": 1,
		"callback_query": map[string]any{
			"id":   "qid-1",
			"from": map[string]any{"id": 1, "is_bot": false, "first_name": "u"},
			"message": map[string]any{
				"message_id": 42,
				"chat":       map[string]any{"id": chatID, "type": "private"},
				"date":       1700000000,
			},
			"data": callbackData,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestWebhook_RejectsBadSecret(t *testing.T) {
	router := &fakeRouter{}
	h := NewWebhookHandler(WebhookConfig{Secret: "good", TenantFinder: func(chat string) (CheckinTenant, bool) {
		return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
	}})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/bad", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-18")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if router.saveCalls > 0 {
		t.Fatal("router must not be touched on bad secret")
	}
}

func TestWebhook_RejectsBadTokenHeader(t *testing.T) {
	h := NewWebhookHandler(WebhookConfig{
		Secret:      "good",
		TokenHeader: "expected-token",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{}, true
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-18")))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-token")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestWebhook_AcceptsCorrectTokenHeader(t *testing.T) {
	router := &fakeRouter{saveStatus: "answered"}
	h := NewWebhookHandler(WebhookConfig{
		Secret:      "good",
		TokenHeader: "expected-token",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-18")))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "expected-token")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if router.saveCalls != 1 {
		t.Fatalf("save not called: %d", router.saveCalls)
	}
}

func TestWebhook_UnknownChatID(t *testing.T) {
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{}, false
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "999", "checkin:great:2026-05-18")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		// Telegram retries non-2xx — we silently 200 unknown chats so we don't get a permanent retry loop.
		t.Fatalf("want 200 (silent reject), got %d", rec.Code)
	}
}

func TestWebhook_HappyPath_AnsweredTriggersReport(t *testing.T) {
	router := &fakeRouter{saveStatus: "answered"}
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			if chat == "111" {
				return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
			}
			return CheckinTenant{}, false
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-18")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body)
	}
	if router.saveCalls != 1 {
		t.Fatalf("save not called: %d", router.saveCalls)
	}
	if router.lastAnswer != "great" || router.lastDate != "2026-05-18" || router.lastSource != "telegram" {
		t.Fatalf("wrong save payload: %+v", router)
	}
	if router.ackCalls != 1 {
		t.Fatalf("ack not called: %d", router.ackCalls)
	}
	if len(router.triggers) != 1 || router.triggers[0] != "health" {
		t.Fatalf("trigger report not called for schema: %v", router.triggers)
	}
}

func TestWebhook_LateAnswered_DoesNotTriggerReport(t *testing.T) {
	router := &fakeRouter{saveStatus: "late_answered"}
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:meh:2026-05-18")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if router.saveCalls != 1 || router.ackCalls != 1 {
		t.Fatalf("save/ack should be 1 each; got save=%d ack=%d", router.saveCalls, router.ackCalls)
	}
	if len(router.triggers) != 0 {
		t.Fatalf("late answer must NOT retrigger report (already sent); got triggers=%v", router.triggers)
	}
}

func TestWebhook_ContextPromptAnswer_DoesNotTriggerReport(t *testing.T) {
	router := &fakeRouter{saveStatus: storage.ContextPromptStatusAnswered}
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "ctx:cp_abc123:stress")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if router.saveCalls != 0 {
		t.Fatalf("check-in save must not run for context prompt; got %d", router.saveCalls)
	}
	if router.contextCalls != 1 {
		t.Fatalf("context save not called: %d", router.contextCalls)
	}
	if router.lastPromptID != "cp_abc123" || router.lastCategory != storage.ContextPromptCategoryStress || router.lastSource != storage.ContextPromptSourceTelegram {
		t.Fatalf("wrong context payload: %+v", router)
	}
	if router.ackCalls != 1 {
		t.Fatalf("ack not called: %d", router.ackCalls)
	}
	if len(router.triggers) != 0 {
		t.Fatalf("context answer must not trigger report; got %v", router.triggers)
	}
}

func TestWebhook_RejectsMalformedCallback(t *testing.T) {
	router := &fakeRouter{}
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "ping:great:bad")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		// 200 so Telegram doesn't retry, but no save.
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if router.saveCalls > 0 {
		t.Fatalf("router invoked on malformed callback")
	}
	// The body's "ignored: ..." marker is part of the contract — log
	// readers grep for it. Assert, don't just log.
	if !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("body should contain 'ignored' marker; got: %s", rec.Body.String())
	}
}

// Tapping yesterday's button from chat history today: SaveCheckinAnswer
// is not strictly idempotent (it overwrites answer/answered_at on
// already-answered rows), so the webhook MUST reject stale callbacks
// before touching storage — otherwise the user can silently corrupt
// past outcome rows by tapping old buttons. Today's late answers
// (date == TodayInTZ, past cap) still flow through SaveCheckinAnswer.
func TestWebhook_StaleDateRejectedBeforeSave(t *testing.T) {
	router := &fakeRouter{saveStatus: "answered"} // wouldn't matter — save shouldn't run
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{
				Schema:    "health",
				Lang:      "ru",
				TodayInTZ: "2026-05-18",
				Router:    router,
			}, true
		},
	})
	// Callback for yesterday's date.
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-17")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if router.saveCalls != 0 {
		t.Fatalf("stale-date callback must NOT touch storage; got saveCalls=%d", router.saveCalls)
	}
	if router.ackCalls != 1 {
		t.Fatalf("ack must still fire to dismiss the Telegram spinner; got ackCalls=%d", router.ackCalls)
	}
	if len(router.triggers) != 0 {
		t.Fatalf("stale date must NOT retrigger today's report; got triggers=%v", router.triggers)
	}
	if !strings.Contains(rec.Body.String(), "stale date") {
		t.Errorf("body should mark the rejection reason; got: %s", rec.Body.String())
	}
}

// Save errors must still dismiss the Telegram spinner via an empty
// ack, otherwise the user's button stays "loading" indefinitely.
func TestWebhook_SaveErrorStillAcks(t *testing.T) {
	router := &fakeRouter{saveErr: errors.New("boom")}
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{Schema: "health", Lang: "ru", Router: router}, true
		},
	})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-18")))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (Telegram retry suppressed), got %d", rec.Code)
	}
	if router.ackCalls != 1 {
		t.Fatalf("ack must fire even on save error to kill the spinner; got %d", router.ackCalls)
	}
	if len(router.triggers) != 0 {
		t.Fatalf("save error must NOT trigger report; got triggers=%v", router.triggers)
	}
}
