package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Bot is a minimal Telegram bot client.
type Bot struct {
	token  string
	chatID string
}

func NewBot(token, chatID string) *Bot {
	return &Bot{token: token, chatID: chatID}
}

// Send sends an HTML-formatted message to the configured chat.
func (b *Bot) Send(text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.token)
	payload, _ := json.Marshal(map[string]string{
		"chat_id":    b.chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API: status %d", resp.StatusCode)
	}
	return nil
}

// InlineButton is a single Telegram inline-keyboard button. CallbackData
// lands in the webhook update verbatim — keep it short (Telegram caps at
// 64 bytes) and self-contained (parseable without session state).
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// buildInlineKeyboardPayload returns the JSON body for /sendMessage with
// an inline_keyboard reply markup. Exposed package-private so unit tests
// can verify the shape without any HTTP.
func buildInlineKeyboardPayload(chatID, text string, rows [][]InlineButton) ([]byte, error) {
	return json.Marshal(map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"parse_mode":   "HTML",
		"reply_markup": map[string]any{"inline_keyboard": rows},
	})
}

// SendInlineKeyboard sends a message with an inline-keyboard reply
// markup. Returns the Telegram message_id on success so callers can
// persist it (useful for edit-after-answer flows in later PRs).
func (b *Bot) SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.token)
	payload, err := buildInlineKeyboardPayload(b.chatID, text, rows)
	if err != nil {
		return 0, err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("telegram API: status %d body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("telegram response decode: %w", err)
	}
	if !parsed.OK {
		return 0, fmt.Errorf("telegram API: ok=false body=%s", body)
	}
	return parsed.Result.MessageID, nil
}

// AnswerCallbackQuery acknowledges an inline-keyboard callback so the
// user's Telegram client stops showing the "loading" spinner on the
// button. `text` is shown as a toast (or alert when alert=true; we
// always pass false in MVP — toast is less intrusive).
func (b *Bot) AnswerCallbackQuery(callbackQueryID, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", b.token)
	payload, _ := json.Marshal(map[string]any{
		"callback_query_id": callbackQueryID,
		"text":              text,
		"show_alert":        false,
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram answerCallbackQuery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API: status %d", resp.StatusCode)
	}
	return nil
}
