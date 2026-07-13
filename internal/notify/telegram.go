package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// telegramHTTPTimeout caps every outbound API call. Telegram itself
// usually responds in well under a second; anything longer is a
// network stall and we'd rather fail the send and let the caller's
// retry/backoff path decide than block the scheduler or webhook
// handler indefinitely.
const telegramHTTPTimeout = 5 * time.Second

var telegramAPIBase = "https://api.telegram.org"
var telegramHTTPClient = &http.Client{Timeout: telegramHTTPTimeout}

// Bot is a minimal Telegram bot client.
type Bot struct {
	token  string
	chatID string
}

type telegramTransportError struct{ cause error }

func (e telegramTransportError) Error() string {
	return "telegram transport failed (request URL redacted)"
}
func (e telegramTransportError) Unwrap() error { return e.cause }

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// decodeTelegramResponse treats an unreadable or malformed success response
// as ambiguous. Telegram may already have accepted the message, so callers
// must not retry or fall back to a second send when the acknowledgement is
// truncated after HTTP 200.
func decodeTelegramResponse(resp *http.Response, dst any) error {
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return telegramTransportError{cause: fmt.Errorf("read telegram response: %w", err)}
	}
	if err = json.Unmarshal(body, dst); err != nil {
		return telegramTransportError{cause: fmt.Errorf("decode telegram response: %w", err)}
	}
	return nil
}

func NewBot(token, chatID string) *Bot {
	return &Bot{token: token, chatID: chatID}
}

func botAPIURL(token, method string) string {
	return fmt.Sprintf("%s/bot%s/%s", telegramAPIBase, token, method)
}

// postJSON is the shared HTTP client path used by every Bot method. The
// client-level timeout remains active while callers read the response body;
// cancelling a request context as soon as Do returns would truncate valid
// acknowledgements that arrive after the response headers.
func postJSON(url string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return telegramHTTPClient.Do(req)
}

// Send sends an HTML-formatted message to the configured chat.
func (b *Bot) Send(text string) error {
	url := botAPIURL(b.token, "sendMessage")
	payload, _ := json.Marshal(map[string]string{
		"chat_id":    b.chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	resp, err := postJSON(url, payload)
	if err != nil {
		return telegramTransportError{cause: err}
	}
	defer resp.Body.Close()
	var parsed telegramAPIResponse
	if err = decodeTelegramResponse(resp, &parsed); err != nil {
		return err
	}
	if !parsed.OK {
		return fmt.Errorf("telegram API: ok=false description=%q", parsed.Description)
	}
	return nil
}

func buildRichMessagePayload(chatID, html string, replyMarkup any) ([]byte, error) {
	payload := map[string]any{
		"chat_id": chatID,
		"rich_message": map[string]any{
			"html": html,
		},
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	return json.Marshal(payload)
}

// SendRichHTML sends a Telegram Rich Message using InputRichMessage.html.
func (b *Bot) SendRichHTML(html string) error {
	url := botAPIURL(b.token, "sendRichMessage")
	payload, err := buildRichMessagePayload(b.chatID, html, nil)
	if err != nil {
		return err
	}
	resp, err := postJSON(url, payload)
	if err != nil {
		return telegramTransportError{cause: err}
	}
	defer resp.Body.Close()
	var parsed telegramAPIResponse
	if err = decodeTelegramResponse(resp, &parsed); err != nil {
		return err
	}
	if !parsed.OK {
		return fmt.Errorf("telegram API: ok=false description=%q", parsed.Description)
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
	url := botAPIURL(b.token, "sendMessage")
	payload, err := buildInlineKeyboardPayload(b.chatID, text, rows)
	if err != nil {
		return 0, err
	}
	resp, err := postJSON(url, payload)
	if err != nil {
		return 0, telegramTransportError{cause: err}
	}
	defer resp.Body.Close()
	var parsed struct {
		telegramAPIResponse
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err = decodeTelegramResponse(resp, &parsed); err != nil {
		return 0, err
	}
	if !parsed.OK {
		return 0, fmt.Errorf("telegram API: ok=false: %s", parsed.Description)
	}
	return parsed.Result.MessageID, nil
}

// AnswerCallbackQuery acknowledges an inline-keyboard callback so the
// user's Telegram client stops showing the "loading" spinner on the
// button. `text` is shown as a toast (or alert when alert=true; we
// always pass false in MVP — toast is less intrusive).
//
// Telegram returns HTTP 200 even on logical errors (e.g. "query is
// too old"), so we decode the body and check ok=false the same way
// SendInlineKeyboard does. Without that, a 200-with-ok-false would
// be silently treated as success.
func (b *Bot) AnswerCallbackQuery(callbackQueryID, text string) error {
	url := botAPIURL(b.token, "answerCallbackQuery")
	payload, _ := json.Marshal(map[string]any{
		"callback_query_id": callbackQueryID,
		"text":              text,
		"show_alert":        false,
	})
	resp, err := postJSON(url, payload)
	if err != nil {
		return telegramTransportError{cause: err}
	}
	defer resp.Body.Close()
	var parsed telegramAPIResponse
	if err = decodeTelegramResponse(resp, &parsed); err != nil {
		return err
	}
	if !parsed.OK {
		return fmt.Errorf("telegram API: ok=false description=%q", parsed.Description)
	}
	return nil
}
