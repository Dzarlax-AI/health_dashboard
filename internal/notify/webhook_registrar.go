package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WebhookRegistrar is the interface the settings service depends on.
// Production binds to a real HTTP client; tests use a fake to assert
// "what would have been sent" without touching the Telegram API.
//
// Methods return (reason, description, err):
//   - On success: reason="" description="" err=nil.
//   - On failure: reason is a short stable token (e.g. "unauthorized",
//     "telegram_5xx") for filtering/badging; description is the raw
//     Telegram API error text (e.g. "Bad Request: bad webhook: An
//     HTTPS URL must be provided") for operator-actionable UI; err
//     is the full wrapped error for the log line.
//
// The split between reason and description is load-bearing: the
// reason gives the badge a tight colour-coded chip, the description
// usually names the exact fix.
type WebhookRegistrar interface {
	Register(token, url, secretToken string) (reason, description string, err error)
	Delete(token string) (reason, description string, err error)
}

// telegramWebhookRegistrar is the production implementation. Reuses
// the package-private postJSON helper from telegram.go so every
// outbound call shares the same 5-second timeout + context handling.
type telegramWebhookRegistrar struct{}

// NewTelegramWebhookRegistrar returns the production registrar.
// Stateless — safe to share across goroutines.
func NewTelegramWebhookRegistrar() WebhookRegistrar { return &telegramWebhookRegistrar{} }

// Register calls Telegram's setWebhook for the given bot token.
// Idempotent on the Telegram side — calling with the same arguments
// twice returns success both times. allowed_updates is pinned to
// ["callback_query"] to match what the webhook handler actually
// reads; allowing more update types would invite payloads we ignore.
func (telegramWebhookRegistrar) Register(token, url, secretToken string) (string, string, error) {
	api := fmt.Sprintf("https://api.telegram.org/bot%s/setWebhook", token)
	payload, _ := json.Marshal(map[string]any{
		"url":             url,
		"secret_token":    secretToken,
		"allowed_updates": []string{"callback_query"},
	})
	return execAndCheck(api, payload)
}

// Delete calls Telegram's deleteWebhook for the given token. Use it
// only when the operator removes the token entirely — for rotation
// the new token is a different bot, the old bot's webhook becomes
// orphaned naturally, and trying to deleteWebhook on it would
// require API access we no longer have or care about.
func (telegramWebhookRegistrar) Delete(token string) (string, string, error) {
	api := fmt.Sprintf("https://api.telegram.org/bot%s/deleteWebhook", token)
	payload, _ := json.Marshal(map[string]any{})
	return execAndCheck(api, payload)
}

// execAndCheck wraps postJSON + Telegram-style response decoding into
// a (reason, description, error) result. On success: ("", "", nil).
// On failure: a short stable reason token + the raw Telegram
// description for operator-actionable UI + the full error for logs.
func execAndCheck(api string, payload []byte) (string, string, error) {
	resp, err := postJSON(api, payload)
	if err != nil {
		// Network/timeout failure — postJSON's context already capped
		// at 5s. Treat as a transient reason.
		return "network", "", fmt.Errorf("network: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// Treat as a network-class failure: we don't know what
		// Telegram actually responded, so don't pretend by trying to
		// classify by status code alone.
		return "network", "", fmt.Errorf("read response body: %w (status=%d)", readErr, resp.StatusCode)
	}
	var parsed struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	// Decode failures count as bad_response — Telegram returning
	// non-JSON is unusual but possible (e.g. a CDN error page).
	if jerr := json.Unmarshal(body, &parsed); jerr != nil {
		return "bad_response", "", fmt.Errorf("bad response: %v body=%q", jerr, body)
	}
	if resp.StatusCode == 200 && parsed.OK {
		return "", "", nil
	}
	reason := classifyTelegramAPIError(resp.StatusCode, parsed.OK, parsed.Description)
	return reason, parsed.Description, fmt.Errorf("telegram API failed: status=%d ok=%v desc=%q",
		resp.StatusCode, parsed.OK, parsed.Description)
}

// classifyTelegramAPIError reduces a (HTTP status, ok, description)
// triple to a short, stable token suitable for the WebhookStatus
// reason field. Operator-visible: keep these tokens short (≤32 chars)
// and snake_case so the UI badge can render them inline.
//
// Pinned by TestClassifyTelegramAPIError — any future remap should
// add a test row before changing the switch.
func classifyTelegramAPIError(httpStatus int, ok bool, description string) string {
	if httpStatus == 200 && ok {
		return "ok"
	}
	switch httpStatus {
	case 200:
		// HTTP 200 but ok=false — Telegram-side logical rejection
		// (stale callback, query too old, etc.).
		return "rejected"
	case 401:
		return "unauthorized"
	case 404:
		return "not_found"
	case 400:
		// Bad Request covers many shapes; the description has the
		// real story but we keep the badge tight. Operators check
		// logs for the full text.
		_ = description
		return "bad_request"
	case 429:
		return "rate_limited"
	}
	if httpStatus >= 500 && httpStatus < 600 {
		return "telegram_5xx"
	}
	// Unknown HTTP status — fall back to the raw code as a token.
	return strings.TrimSpace(strconv.Itoa(httpStatus))
}
