package notify

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// CheckinAnswerRouter is what the webhook calls after secret + tenant
// validation succeed. Split from the bot abstraction so the same
// interface can be backed by *storage.DB in production and a fake in
// tests.
type CheckinAnswerRouter interface {
	// SaveAnswer persists the answer and returns the resulting status
	// ("answered" | "late_answered"). Implementation routes to the
	// right tenant's DB.
	SaveAnswer(date, source, answer string, answeredAt time.Time) (string, error)
	// AnswerCallbackQuery acks the Telegram callback (kills the
	// loading spinner on the button).
	AnswerCallbackQuery(callbackQueryID, text string) error
	// TriggerReport runs the morning-report send async after an
	// in-time answer. No-op when the report has already been sent
	// for today (idempotent on the tenant side).
	TriggerReport(schema string)
}

// CheckinTenant carries the per-tenant routing context the webhook
// needs after a chat_id lookup. Schema is for logging + report
// trigger; Lang drives the ack text; TodayInTZ is precomputed by the
// TenantFinder so the webhook can reject stale callbacks (e.g. user
// taps yesterday's already-answered button from chat history today —
// without the date check we'd trigger today's report on a re-tap of
// last morning's row). Router does the save + ack + (conditional)
// re-trigger.
type CheckinTenant struct {
	Schema    string
	Lang      string
	TodayInTZ string // YYYY-MM-DD in the tenant's REPORT_TZ
	Router    CheckinAnswerRouter
}

// WebhookConfig configures NewWebhookHandler.
//
//   - Secret is the URL-path segment (validated constant-time).
//   - TokenHeader is the optional Telegram `setWebhook?secret_token=...`
//     value (sent back on every update as `X-Telegram-Bot-Api-Secret-Token`).
//     Leave empty to disable the header check.
//   - TenantFinder maps the inbound chat_id to a CheckinTenant; returns
//     found=false to silently reject (200 OK to prevent Telegram retries).
type WebhookConfig struct {
	Secret       string
	TokenHeader  string
	TenantFinder func(chat string) (CheckinTenant, bool)
}

// NewWebhookHandler returns an http.HandlerFunc for the Telegram
// callback. Path shape: `/api/telegram/webhook/<secret>`. The handler
// is sync but fast: one store write + one outbound ack + an async
// report trigger that returns immediately. Telegram needs the
// response within ~10s.
func NewWebhookHandler(cfg WebhookConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// URL-path secret. Constant-time compare so a timing attack
		// cannot enumerate the secret byte by byte.
		secret := strings.TrimPrefix(r.URL.Path, "/api/telegram/webhook/")
		if subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.Secret)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if cfg.TokenHeader != "" {
			got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(cfg.TokenHeader)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}

		// Bound the request body before parsing. Telegram updates are
		// small (~1-2 KB); 1 MiB is generous headroom and shuts down
		// abusive payloads on this public endpoint.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var upd struct {
			CallbackQuery *struct {
				ID      string `json:"id"`
				Message struct {
					Chat struct {
						ID json.RawMessage `json:"id"` // Telegram sends int; coerce to string below
					} `json:"chat"`
				} `json:"message"`
				Data string `json:"data"`
			} `json:"callback_query"`
		}
		if err := json.Unmarshal(body, &upd); err != nil {
			// Don't 4xx on parse — Telegram will retry. Log + 200.
			log.Printf("telegram webhook: bad json: %v", err)
			fmt.Fprintln(w, "ignored: bad json")
			return
		}
		if upd.CallbackQuery == nil {
			fmt.Fprintln(w, "ignored: not a callback")
			return
		}
		// Telegram chat IDs come as JSON numbers; strip optional quotes
		// in case Telegram changes its mind.
		chat := strings.Trim(string(upd.CallbackQuery.Message.Chat.ID), `"`)

		tenant, ok := cfg.TenantFinder(chat)
		if !ok {
			log.Printf("telegram webhook: unknown chat_id %q", chat)
			fmt.Fprintln(w, "ignored: unknown chat")
			return
		}
		answer, date, ok := parseCheckinCallback(upd.CallbackQuery.Data)
		if !ok {
			log.Printf("telegram webhook: malformed callback %q from chat %s", upd.CallbackQuery.Data, chat)
			fmt.Fprintln(w, "ignored: malformed callback")
			return
		}
		// Stale-date guard: reject callbacks whose date doesn't match
		// today in the tenant's TZ BEFORE touching storage.
		//
		// SaveCheckinAnswer is not strictly idempotent — it overwrites
		// `answer` and `answered_at` even on a row that's already
		// `answered`. So tapping yesterday's button from chat scrollback
		// today would silently mutate yesterday's outcome row (e.g. flip
		// last morning's "ok" to "sick" and stamp today's timestamp).
		// That corrupts the validation dataset PR 3 will read.
		//
		// Today's late answers (date == TodayInTZ, past cap) still flow
		// through SaveCheckinAnswer below — that's the intended
		// late_answered path. Only cross-day taps are rejected here.
		//
		// TodayInTZ is empty in tests that don't bother setting it; we
		// treat empty as "no date check requested" so older test
		// fixtures keep working without churning every case.
		if tenant.TodayInTZ != "" && date != tenant.TodayInTZ {
			log.Printf("telegram webhook: stale callback for date=%s today=%s tenant=%s; acking without save", date, tenant.TodayInTZ, tenant.Schema)
			_ = tenant.Router.AnswerCallbackQuery(upd.CallbackQuery.ID, "")
			fmt.Fprintln(w, "ignored: stale date")
			return
		}
		status, err := tenant.Router.SaveAnswer(date, storage.CheckinSourceTelegram, answer, time.Now())
		if err != nil {
			log.Printf("telegram webhook: save %s tenant=%s err=%v", answer, tenant.Schema, err)
			// Best-effort ack with empty text to dismiss the Telegram
			// loading spinner — without it the button stays "spinning"
			// in the user's chat client until they tap again. The save
			// error itself is already logged.
			_ = tenant.Router.AnswerCallbackQuery(upd.CallbackQuery.ID, "")
			// 200 to suppress Telegram retry.
			fmt.Fprintln(w, "ignored: save error")
			return
		}
		ack := ackText(tenant.Lang, answer, status)
		if err := tenant.Router.AnswerCallbackQuery(upd.CallbackQuery.ID, ack); err != nil {
			log.Printf("telegram webhook: ack: %v", err)
		}
		// Trigger today's report ONLY when the row transitioned to
		// `answered` (in-time response to today's prompt). Late answers
		// (status=late_answered) skip the trigger by design — the report
		// already went out. Cross-day taps were rejected by the stale-
		// date guard above, so any `date` reaching this point is today's
		// in the tenant's TZ.
		if status == storage.CheckinStatusAnswered {
			tenant.Router.TriggerReport(tenant.Schema)
		}
		fmt.Fprintln(w, "ok")
	}
}

// ackText picks the localised toast string for the Telegram callback
// acknowledgement based on the answer and whether the save was in
// time or post-hoc.
func ackText(lang, answer, status string) string {
	strs := health.GetStrings(lang)
	if status == storage.CheckinStatusLateAnswered {
		return strs["checkin_ack_late"]
	}
	switch answer {
	case storage.CheckinAnswerGreat:
		return strs["checkin_ack_great"]
	case storage.CheckinAnswerOK:
		return strs["checkin_ack_ok"]
	case storage.CheckinAnswerMeh:
		return strs["checkin_ack_meh"]
	case storage.CheckinAnswerSick:
		return strs["checkin_ack_sick"]
	}
	return ""
}
