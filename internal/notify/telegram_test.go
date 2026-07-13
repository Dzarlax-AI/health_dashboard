package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Bot only emits HTTP, so we test the JSON payload builder
// directly. This catches the shape Telegram requires before live
// integration sees a 400.
func TestBuildInlineKeyboardPayload(t *testing.T) {
	buttons := [][]InlineButton{
		{{Text: "Отлично", CallbackData: "checkin:great:2026-05-18"}, {Text: "Нормально", CallbackData: "checkin:ok:2026-05-18"}},
		{{Text: "Не очень", CallbackData: "checkin:meh:2026-05-18"}, {Text: "Болен(а)", CallbackData: "checkin:sick:2026-05-18"}},
	}
	raw, err := buildInlineKeyboardPayload("9999", "Как вы себя чувствуете?", buttons)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if got["chat_id"] != "9999" {
		t.Errorf("chat_id mismatch: %v", got["chat_id"])
	}
	if !strings.Contains(got["text"].(string), "чувствуете") {
		t.Errorf("text not propagated")
	}
	rm, ok := got["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing or wrong shape")
	}
	kb, ok := rm["inline_keyboard"].([]any)
	if !ok || len(kb) != 2 {
		t.Fatalf("inline_keyboard not 2 rows: %v", rm["inline_keyboard"])
	}
	firstRow := kb[0].([]any)
	if len(firstRow) != 2 {
		t.Fatalf("first row not 2 buttons")
	}
	first := firstRow[0].(map[string]any)
	if first["text"] != "Отлично" || first["callback_data"] != "checkin:great:2026-05-18" {
		t.Errorf("first button mismatch: %v", first)
	}
}

func TestBuildRichMessagePayload(t *testing.T) {
	raw, err := buildRichMessagePayload("9999", "<h2>Morning</h2>", map[string]any{"inline_keyboard": []any{}})
	if err != nil {
		t.Fatalf("build rich payload: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if got["chat_id"] != "9999" {
		t.Errorf("chat_id mismatch: %v", got["chat_id"])
	}
	rm, ok := got["rich_message"].(map[string]any)
	if !ok {
		t.Fatalf("rich_message missing or wrong shape: %v", got["rich_message"])
	}
	if rm["html"] != "<h2>Morning</h2>" {
		t.Errorf("rich html mismatch: %v", rm["html"])
	}
	if _, ok := got["reply_markup"]; !ok {
		t.Fatal("reply_markup should be preserved when supplied")
	}
}

func TestSendRichHTMLChecksTelegramOKFalse(t *testing.T) {
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()

	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":false,"description":"Bad Request: invalid rich message"}`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL

	bot := NewBot("token", "chat")
	err := bot.SendRichHTML("<h2>Bad</h2>")
	if err == nil {
		t.Fatal("expected ok=false error")
	}
	if !strings.Contains(err.Error(), "invalid rich message") {
		t.Fatalf("error should include Telegram description, got %v", err)
	}
	if path != "/bottoken/sendRichMessage" {
		t.Fatalf("unexpected Telegram path %q", path)
	}
}

func TestSendInlineKeyboardChecksTelegramOKFalse(t *testing.T) {
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":false,"description":"Bad Request: invalid callback data"}`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL

	bot := NewBot("token", "chat")
	_, err := bot.SendInlineKeyboard("Prompt", [][]InlineButton{{{Text: "Answer", CallbackData: "bad"}}})
	if err == nil {
		t.Fatal("expected ok=false error")
	}
	if !strings.Contains(err.Error(), "invalid callback data") {
		t.Fatalf("error should include Telegram description, got %v", err)
	}
}

func TestTelegramMalformedSuccessResponsesAreAmbiguous(t *testing.T) {
	oldBase := telegramAPIBase
	defer func() { telegramAPIBase = oldBase }()

	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":`)
	}))
	defer ts.Close()
	telegramAPIBase = ts.URL
	bot := NewBot("token", "chat")

	t.Run("plain send", func(t *testing.T) {
		err := bot.Send("hello")
		var transport telegramTransportError
		if !errors.As(err, &transport) {
			t.Fatalf("malformed HTTP 200 must be ambiguous, got %T: %v", err, err)
		}
	})

	t.Run("inline keyboard", func(t *testing.T) {
		_, err := bot.SendInlineKeyboard("prompt", [][]InlineButton{{{Text: "ok", CallbackData: "ok"}}})
		var transport telegramTransportError
		if !errors.As(err, &transport) {
			t.Fatalf("malformed HTTP 200 must be ambiguous, got %T: %v", err, err)
		}
	})

	t.Run("rich report does not fall back", func(t *testing.T) {
		before := requests
		err := sendReportHTML(bot, Config{TelegramRichMessages: true}, "morning", "<h2>rich</h2>", "fallback")
		var transport telegramTransportError
		if !errors.As(err, &transport) {
			t.Fatalf("malformed rich acknowledgement must stay ambiguous, got %T: %v", err, err)
		}
		if requests-before != 1 {
			t.Fatalf("ambiguous rich send triggered fallback: requests=%d, want 1", requests-before)
		}
	})

	t.Run("durable delivery remains ambiguous and is not retried", func(t *testing.T) {
		store := &recordingDeliveryStore{}
		before := requests
		sent, err := sendDurableReport(store, "report:malformed", func() error { return bot.Send("hello") })
		if !sent || err == nil || store.status != "ambiguous" {
			t.Fatalf("first send = sent:%v status:%q err:%v, want ambiguous completion", sent, store.status, err)
		}
		sent, err = sendDurableReport(store, "report:malformed", func() error { return bot.Send("duplicate") })
		if sent || err != nil {
			t.Fatalf("ambiguous delivery retry = sent:%v err:%v, want skipped", sent, err)
		}
		if requests-before != 1 {
			t.Fatalf("ambiguous delivery was sent more than once: requests=%d", requests-before)
		}
	})
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("truncated body") }
func (failingReadCloser) Close() error             { return nil }

func TestDecodeTelegramResponseReadFailureIsAmbiguous(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusOK, Body: failingReadCloser{}}
	err := decodeTelegramResponse(resp, &telegramAPIResponse{})
	var transport telegramTransportError
	if !errors.As(err, &transport) {
		t.Fatalf("response read failure must be ambiguous, got %T: %v", err, err)
	}
	if !strings.Contains(errors.Unwrap(transport).Error(), "truncated body") {
		t.Fatalf("transport cause lost response read error: %v", transport)
	}
}
