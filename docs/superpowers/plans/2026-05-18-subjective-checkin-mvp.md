# Subjective Daily Check-in MVP — PR 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a one-tap Telegram morning check-in (`great` / `ok` / `meh` / `sick`) that records a user's subjective state before the morning report, persists per-tenant, and shows on the dashboard as a one-line confirmation. Scores must not change.

**Architecture:** Telegram inline-keyboard prompt sent on the same scheduler tick that would have sent the morning report, once `SleepSettled` returns true. The user taps a button → Telegram POSTs `/api/telegram/webhook/<secret>` (HTTPS, validated by `X-Telegram-Bot-Api-Secret-Token` and `chat_id` lookup) → we save the answer and async-trigger the report. If no answer arrives before `MorningCapHour`, the row is marked `expired` and the report sends with a one-line soft note. Late taps after expiry transition to `late_answered` for analytics, separated from primary-validation `answered` rows. Tenants without Telegram configured are skipped entirely — the morning flow proceeds as before.

**Tech Stack:** Go 1.22+, pgx/v5, Postgres (text-typed `date` to match existing schema convention), Telegram Bot API (sendMessage with `reply_markup`, `answerCallbackQuery`, `setWebhook`), embedded HTML templates.

---

## File Structure

**Create:**
- `internal/storage/subjective_checkin.go` — table DDL (`EnsureSubjectiveCheckinsTable`), CRUD (`SaveCheckinPrompted`, `MarkCheckinAnswered`, `MarkCheckinLateAnswered`, `ExpireStaleCheckin`, `GetTodayCheckin`).
- `internal/storage/subjective_checkin_test.go` — pure-logic tests for the state-transition helper (no DB), structural test asserting the table-creation SQL contains required columns + PK.
- `internal/notify/checkin_prompt.go` — `SendCheckinPrompt(bot, lang)` builds the inline keyboard and POSTs `sendMessage`.
- `internal/notify/checkin_prompt_test.go` — JSON-shape test on the payload builder.
- `internal/notify/checkin_callback.go` — `WebhookHandler` factory: signature verify, payload parse, tenant lookup, save, ack, async-trigger report.
- `internal/notify/checkin_callback_test.go` — table-driven tests for header/secret validation, payload parsing, chat_id matching.
- `internal/tenants/manager_telegram.go` — `DBForTelegramChatID` lookup (one row per tenant, walks the pool map).
- `internal/tenants/manager_telegram_test.go` — pure-logic test for the lookup (mock manager state).

**Modify:**
- `internal/notify/telegram.go` — add `SendInlineKeyboard(text string, buttons [][]InlineButton) (messageID int64, err error)` and an `InlineButton` struct.
- `internal/storage/briefing.go` — `GetHealthBriefing` decoration: read `GetTodayCheckin` and put the answer enum into the existing response struct (new field on the Go type returned to UI).
- `internal/health/types.go` — add `SubjectiveCheckin *SubjectiveCheckin` field to whatever struct is rendered into the dashboard (today's morning briefing payload).
- `internal/ui/page_data.go` — add `Checkin *SubjectiveCheckin` field on the dashboard page-data struct (or pass through the existing briefing struct).
- `internal/ui/templates/pages/dashboard.html` — render one-line "Ваш утренний ответ: …" when answer present.
- `internal/ui/i18n_{en,ru,sr}.go` — strings: 4 button labels, "feeling today?" prompt, "answer saved" callback ack, "morning answer" confirmation line, expired-note text.
- `cmd/server/main.go` — call `EnsureSubjectiveCheckinsTable` in all three table-creation sites (legacy, multi-user existing, multi-user on-create); register `/api/telegram/webhook/{secret}` on `mux`; refactor `runMorningSmartRetry` to send the prompt before the first report attempt and gate the report on `GetTodayCheckin().Answered() || past_cap`.
- `internal/notify/report.go` — surface `SendMorningSmart` to consume a "checkin answered/late note" hint (or read it from DB inside the existing function — choice locked in Task 9).

---

## Tasks

### Task 1: Storage table — DDL + Ensure function

**Files:**
- Create: `internal/storage/subjective_checkin.go`
- Modify: `cmd/server/main.go:99-102`, `cmd/server/main.go:162-165`, `cmd/server/main.go:205-208`

- [ ] **Step 1: Write the failing test**

Create `internal/storage/subjective_checkin_test.go`:

```go
package storage

import (
	"strings"
	"testing"
)

// Structural test: the DDL the Ensure function emits must declare the
// columns the rest of the code expects. Reading the SQL is cheap; a
// live DB test would require Postgres in CI which the repo currently
// avoids.
func TestSubjectiveCheckinDDL_HasRequiredColumns(t *testing.T) {
	ddl := subjectiveCheckinTableDDL()
	for _, col := range []string{
		"date TEXT NOT NULL",
		"source TEXT NOT NULL",
		"status TEXT NOT NULL",
		"answer TEXT",
		"prompt_message_id BIGINT",
		"prompted_at TIMESTAMPTZ NOT NULL",
		"answered_at TIMESTAMPTZ",
		"expires_at TIMESTAMPTZ NOT NULL",
		"metadata JSONB NOT NULL DEFAULT '{}'::jsonb",
		"PRIMARY KEY (date, source)",
	} {
		if !strings.Contains(ddl, col) {
			t.Errorf("DDL missing %q\n\nfull DDL:\n%s", col, ddl)
		}
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

```bash
cd "/d/Projects/Code/Personal/Health server/health_processing" && go test ./internal/storage/ -run TestSubjectiveCheckinDDL_HasRequiredColumns -v
```

Expected: FAIL with `undefined: subjectiveCheckinTableDDL`.

- [ ] **Step 3: Create the DDL constant + Ensure function**

Create `internal/storage/subjective_checkin.go`:

```go
package storage

import (
	"context"
	"log"
	"time"
)

// subjectiveCheckinTableDDL returns the CREATE TABLE statement for
// subjective_checkins. Exposed package-internal so the structural test
// can assert column shape without a live DB.
//
// Schema rationale:
//   - `date TEXT` matches every other dated table in this schema
//     (daily_scores, energy_snapshots, target_snapshots). The writer
//     formats it in tenant REPORT_TZ before INSERT.
//   - PK (date, source) so multiple entry points (Telegram now,
//     dashboard later) can co-exist without overwriting each other's
//     coverage signal.
//   - status: prompted | answered | expired | late_answered.
//     Closed enum, validated at the Go layer; column is plain TEXT
//     so adding a state never requires a migration.
//   - answer: NULL until status moves out of `prompted`. When set,
//     one of {great, ok, meh, sick}.
//   - metadata JSONB: open-ended box for callback nonces, retry
//     counts, and post-MVP analytics tags. Starts empty.
func subjectiveCheckinTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS subjective_checkins (
		date              TEXT NOT NULL,
		source            TEXT NOT NULL,
		status            TEXT NOT NULL,
		answer            TEXT,
		prompt_message_id BIGINT,
		prompted_at       TIMESTAMPTZ NOT NULL,
		answered_at       TIMESTAMPTZ,
		expires_at        TIMESTAMPTZ NOT NULL,
		metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
		PRIMARY KEY (date, source)
	)`
}

// EnsureSubjectiveCheckinsTable creates the subjective_checkins table.
// Idempotent — safe to call on every startup.
func (s *DB) EnsureSubjectiveCheckinsTable() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, subjectiveCheckinTableDDL()); err != nil {
		log.Printf("EnsureSubjectiveCheckinsTable: %v", err)
	}
}
```

- [ ] **Step 4: Run test — verify it passes**

```bash
go test ./internal/storage/ -run TestSubjectiveCheckinDDL_HasRequiredColumns -v
```

Expected: PASS.

- [ ] **Step 5: Wire Ensure into all three table-creation call sites in main**

Edit `cmd/server/main.go`. After every `db.EnsureReadinessRedesignTables()` call (three locations: legacy at line 102, multi-user existing at line 165, on-create at line 208) append:

```go
		db.EnsureSubjectiveCheckinsTable()
```

The legacy site uses `legacyDB.EnsureReadinessRedesignTables()` so the new line there reads `legacyDB.EnsureSubjectiveCheckinsTable()`. Same indentation level as siblings.

- [ ] **Step 6: Build to verify the wire-up compiles**

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/subjective_checkin.go internal/storage/subjective_checkin_test.go cmd/server/main.go
git commit -m "feat(subjective-checkin): storage table for daily Telegram check-in"
```

---

### Task 2: Storage — typed enums + Save/Get/Transition helpers

**Files:**
- Modify: `internal/storage/subjective_checkin.go`
- Test: `internal/storage/subjective_checkin_test.go`

- [ ] **Step 1: Write the failing test for the pure transition helper**

Append to `internal/storage/subjective_checkin_test.go`:

```go
import "time"

func TestNextCheckinStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	expiresFuture := now.Add(1 * time.Hour)
	expiresPast := now.Add(-1 * time.Hour)

	cases := []struct {
		name     string
		current  string
		action   string
		expires  time.Time
		now      time.Time
		want     string
		wantErr  bool
	}{
		{"prompted+tap before cap", CheckinStatusPrompted, "tap", expiresFuture, now, CheckinStatusAnswered, false},
		{"prompted+tap after cap", CheckinStatusPrompted, "tap", expiresPast, now, CheckinStatusLateAnswered, false},
		{"expired+tap (very late)", CheckinStatusExpired, "tap", expiresPast, now, CheckinStatusLateAnswered, false},
		{"prompted+expire after cap", CheckinStatusPrompted, "expire", expiresPast, now, CheckinStatusExpired, false},
		{"prompted+expire before cap", CheckinStatusPrompted, "expire", expiresFuture, now, "", true}, // can't expire early
		{"answered+tap (idempotent re-tap)", CheckinStatusAnswered, "tap", expiresFuture, now, CheckinStatusAnswered, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextCheckinStatus(tc.current, tc.action, tc.expires, tc.now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got status=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAnswer(t *testing.T) {
	for _, ans := range []string{"great", "ok", "meh", "sick"} {
		if err := ValidateCheckinAnswer(ans); err != nil {
			t.Errorf("answer %q should be valid: %v", ans, err)
		}
	}
	for _, ans := range []string{"", "GREAT", "good", "fine", "amazing"} {
		if err := ValidateCheckinAnswer(ans); err == nil {
			t.Errorf("answer %q should be rejected", ans)
		}
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
go test ./internal/storage/ -run "TestNextCheckinStatus|TestValidateAnswer" -v
```

Expected: FAIL — `undefined: CheckinStatusPrompted`, `nextCheckinStatus`, `ValidateCheckinAnswer`.

- [ ] **Step 3: Add the enums and pure transition helper**

Append to `internal/storage/subjective_checkin.go`:

```go
import (
	"errors"
	"fmt"
)

// Status enum (column subjective_checkins.status). Stored as plain
// TEXT in Postgres; values validated at the Go layer.
const (
	CheckinStatusPrompted     = "prompted"
	CheckinStatusAnswered     = "answered"
	CheckinStatusExpired      = "expired"
	CheckinStatusLateAnswered = "late_answered"
)

// Answer enum (column subjective_checkins.answer). NULL until status
// transitions out of `prompted`.
const (
	CheckinAnswerGreat = "great"
	CheckinAnswerOK    = "ok"
	CheckinAnswerMeh   = "meh"
	CheckinAnswerSick  = "sick"
)

// ValidateCheckinAnswer reports whether ans is one of the four allowed
// strings. Returns an error with the rejected value so callers can
// surface it in callback acks / logs without re-formatting.
func ValidateCheckinAnswer(ans string) error {
	switch ans {
	case CheckinAnswerGreat, CheckinAnswerOK, CheckinAnswerMeh, CheckinAnswerSick:
		return nil
	}
	return fmt.Errorf("invalid checkin answer %q", ans)
}

// nextCheckinStatus computes the target status for a row given its
// current state, the action being applied, the row's expires_at and
// the current wall clock. Pure function — exercised by tests without
// a DB so the policy stays explicit and trivially auditable.
//
// Actions:
//   - "tap"    — user pressed an inline-keyboard button. Before
//                expires_at → answered (primary validation pool).
//                At/after expires_at → late_answered (analytics-only).
//                Already answered → idempotent re-tap, stay answered.
//   - "expire" — scheduler decided the cap has passed. Only valid
//                from prompted, and only when expires_at is reached.
//
// Returns ("", err) when the transition is not valid (e.g. expire
// called before cap).
func nextCheckinStatus(current, action string, expiresAt, now time.Time) (string, error) {
	switch action {
	case "tap":
		if current == CheckinStatusAnswered {
			return CheckinStatusAnswered, nil
		}
		if now.Before(expiresAt) && current == CheckinStatusPrompted {
			return CheckinStatusAnswered, nil
		}
		// Anything else (expired, prompted-past-cap, late_answered) → late_answered.
		return CheckinStatusLateAnswered, nil
	case "expire":
		if current != CheckinStatusPrompted {
			return "", errors.New("can only expire a prompted row")
		}
		if now.Before(expiresAt) {
			return "", errors.New("expires_at not reached yet")
		}
		return CheckinStatusExpired, nil
	}
	return "", fmt.Errorf("unknown action %q", action)
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
go test ./internal/storage/ -run "TestNextCheckinStatus|TestValidateAnswer" -v
```

Expected: PASS.

- [ ] **Step 5: Add the CRUD helpers (no test — they're trivial Exec wrappers verified by end-to-end smoke)**

Append to `internal/storage/subjective_checkin.go`:

```go
// CheckinRow is the read-shape of one subjective_checkins row.
type CheckinRow struct {
	Date            string
	Source          string
	Status          string
	Answer          string // "" when NULL
	PromptMessageID int64  // 0 when NULL
	PromptedAt      time.Time
	AnsweredAt      time.Time // zero when NULL
	ExpiresAt       time.Time
}

// SaveCheckinPrompted upserts a `prompted` row for (date, source).
// Idempotent on retry: a second prompt for the same date overwrites
// prompt_message_id and prompted_at without erasing an existing
// answer (the WHERE-on-status guards that).
func (s *DB) SaveCheckinPrompted(date, source string, msgID int64, promptedAt, expiresAt time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO subjective_checkins
			(date, source, status, prompt_message_id, prompted_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (date, source) DO UPDATE SET
			prompt_message_id = EXCLUDED.prompt_message_id,
			prompted_at       = EXCLUDED.prompted_at,
			expires_at        = EXCLUDED.expires_at
		WHERE subjective_checkins.status = $3`,
		date, source, CheckinStatusPrompted, msgID, promptedAt, expiresAt)
	return err
}

// SaveCheckinAnswer transitions a row based on the pure helper's
// decision. Reads the current status + expires_at in a transaction
// so a racing scheduler-expire cannot clobber a real answer.
func (s *DB) SaveCheckinAnswer(date, source, answer string, answeredAt time.Time) (string, error) {
	if err := ValidateCheckinAnswer(answer); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var status string
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT status, expires_at FROM subjective_checkins
		WHERE date = $1 AND source = $2 FOR UPDATE`, date, source).Scan(&status, &expiresAt); err != nil {
		return "", err
	}
	nextStatus, err := nextCheckinStatus(status, "tap", expiresAt, answeredAt)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE subjective_checkins
		   SET status = $3, answer = $4, answered_at = $5
		 WHERE date = $1 AND source = $2`,
		date, source, nextStatus, answer, answeredAt); err != nil {
		return "", err
	}
	return nextStatus, tx.Commit(ctx)
}

// ExpireCheckin transitions a prompted row to expired when cap has
// passed. No-op if the row is already answered / late_answered /
// expired (returns "", nil so callers can ignore the return).
func (s *DB) ExpireCheckin(date, source string, now time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var status string
	var expiresAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT status, expires_at FROM subjective_checkins
		WHERE date = $1 AND source = $2`, date, source).Scan(&status, &expiresAt)
	if err != nil {
		return "", err
	}
	if status != CheckinStatusPrompted {
		return "", nil
	}
	nextStatus, err := nextCheckinStatus(status, "expire", expiresAt, now)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE subjective_checkins SET status = $3 WHERE date = $1 AND source = $2`,
		date, source, nextStatus)
	return nextStatus, err
}

// GetTodayCheckin returns the row for (date, source) or nil if none.
func (s *DB) GetTodayCheckin(date, source string) (*CheckinRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row := &CheckinRow{Date: date, Source: source}
	var answer *string
	var msgID *int64
	var answeredAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT status, answer, prompt_message_id, prompted_at, answered_at, expires_at
		  FROM subjective_checkins
		 WHERE date = $1 AND source = $2`, date, source).
		Scan(&row.Status, &answer, &msgID, &row.PromptedAt, &answeredAt, &row.ExpiresAt)
	if err != nil {
		// pgx returns ErrNoRows-equivalent; let the caller treat as "no checkin".
		return nil, err
	}
	if answer != nil {
		row.Answer = *answer
	}
	if msgID != nil {
		row.PromptMessageID = *msgID
	}
	if answeredAt != nil {
		row.AnsweredAt = *answeredAt
	}
	return row, nil
}
```

- [ ] **Step 6: Build**

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/subjective_checkin.go internal/storage/subjective_checkin_test.go
git commit -m "feat(subjective-checkin): typed enums + Save/Get/Expire helpers"
```

---

### Task 3: Tenant lookup by Telegram chat_id

**Files:**
- Create: `internal/tenants/manager_telegram.go`, `internal/tenants/manager_telegram_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tenants/manager_telegram_test.go`:

```go
package tenants

import (
	"testing"
)

func TestSchemaForChatID(t *testing.T) {
	chatIDs := map[string]string{
		"111": "health",
		"222": "health_mariia",
	}
	cases := []struct {
		name  string
		chat  string
		want  string
		found bool
	}{
		{"primary tenant", "111", "health", true},
		{"second tenant", "222", "health_mariia", true},
		{"unknown chat", "999", "", false},
		{"empty chat", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := schemaForChatID(chatIDs, tc.chat)
			if found != tc.found || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, found, tc.want, tc.found)
			}
		})
	}
}
```

- [ ] **Step 2: Run — verify fails**

```bash
go test ./internal/tenants/ -run TestSchemaForChatID -v
```

Expected: FAIL — `undefined: schemaForChatID`.

- [ ] **Step 3: Implement the lookup**

Create `internal/tenants/manager_telegram.go`:

```go
package tenants

import (
	"context"

	"health-receiver/internal/storage"
)

// schemaForChatID is the pure-map lookup, isolated so it can be
// tested without spinning up real DB pools. Returns the matched
// schema and whether a match was found.
func schemaForChatID(chatIDs map[string]string, chat string) (string, bool) {
	if chat == "" {
		return "", false
	}
	schema, ok := chatIDs[chat]
	return schema, ok
}

// DBForTelegramChatID walks every registered tenant's notification
// config (resolved against env defaults) and returns the first whose
// chat_id matches. Used by the Telegram webhook to route an inbound
// callback to the right tenant's DB pool.
//
// Returns (nil, "", false) when no tenant matches; the caller should
// reject the update without touching any pool.
func (m *Manager) DBForTelegramChatID(ctx context.Context, defaults storage.NotifyConfig, chat string) (*storage.DB, string, bool) {
	if chat == "" {
		return nil, "", false
	}
	chatIDs := make(map[string]string, len(m.dbs))
	for schema, db := range m.dbs {
		cfg := db.GetNotifyConfig(defaults)
		if cfg.ChatID != "" {
			chatIDs[cfg.ChatID] = schema
		}
	}
	schema, ok := schemaForChatID(chatIDs, chat)
	if !ok {
		return nil, "", false
	}
	return m.dbs[schema], schema, true
}
```

NOTE: this assumes `Manager` already has a `dbs map[string]*storage.DB` field and that `AllDBs()` reflects it. Verify by reading `internal/tenants/manager.go` before writing this file — if the field name differs, adapt. (Read it once and use the actual identifier.)

- [ ] **Step 4: Run — verify passes**

```bash
go test ./internal/tenants/ -run TestSchemaForChatID -v
```

Expected: PASS.

- [ ] **Step 5: Build**

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/tenants/manager_telegram.go internal/tenants/manager_telegram_test.go
git commit -m "feat(tenants): DBForTelegramChatID — route webhook to right tenant"
```

---

### Task 4: Telegram inline-keyboard send helper

**Files:**
- Modify: `internal/notify/telegram.go`
- Test: `internal/notify/telegram_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/notify/telegram_test.go`:

```go
package notify

import (
	"encoding/json"
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
```

- [ ] **Step 2: Run — verify fails**

```bash
go test ./internal/notify/ -run TestBuildInlineKeyboardPayload -v
```

Expected: FAIL — `undefined: InlineButton` / `buildInlineKeyboardPayload`.

- [ ] **Step 3: Implement payload builder + send method**

Modify `internal/notify/telegram.go`. Replace the entire file with:

```go
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

// InlineButton is a single Telegram inline-keyboard button.
// CallbackData lands in the webhook update verbatim — keep it short
// (Telegram caps at 64 bytes) and self-contained (parseable without
// session state).
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// buildInlineKeyboardPayload returns the JSON body for
// /sendMessage with an inline_keyboard reply markup. Exposed
// package-private so unit tests can verify the shape before any HTTP.
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
// button. `text` is shown as a toast (or alert when `alert=true`).
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
```

- [ ] **Step 4: Run — verify passes**

```bash
go test ./internal/notify/ -run TestBuildInlineKeyboardPayload -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/telegram.go internal/notify/telegram_test.go
git commit -m "feat(notify): inline-keyboard send + answerCallbackQuery"
```

---

### Task 5: Checkin prompt builder (i18n strings + button layout)

**Files:**
- Create: `internal/notify/checkin_prompt.go`, `internal/notify/checkin_prompt_test.go`
- Modify: `internal/health/i18n_{en,ru,sr}.go`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/checkin_prompt_test.go`:

```go
package notify

import "testing"

func TestCheckinPromptButtons(t *testing.T) {
	rows, _ := buildCheckinPromptButtons("ru", "2026-05-18")
	if len(rows) != 2 || len(rows[0]) != 2 || len(rows[1]) != 2 {
		t.Fatalf("expected 2x2 keyboard, got %d rows", len(rows))
	}
	want := []string{
		"checkin:great:2026-05-18",
		"checkin:ok:2026-05-18",
		"checkin:meh:2026-05-18",
		"checkin:sick:2026-05-18",
	}
	got := []string{
		rows[0][0].CallbackData,
		rows[0][1].CallbackData,
		rows[1][0].CallbackData,
		rows[1][1].CallbackData,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("button %d callback: got %q want %q", i, got[i], want[i])
		}
	}
	// At least the first button text should differ between en and ru
	rowsEN, _ := buildCheckinPromptButtons("en", "2026-05-18")
	if rowsEN[0][0].Text == rows[0][0].Text {
		t.Errorf("en and ru labels collided: %q", rowsEN[0][0].Text)
	}
}

func TestParseCheckinCallback(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		answer string
		date   string
		ok     bool
	}{
		{"valid great", "checkin:great:2026-05-18", "great", "2026-05-18", true},
		{"valid sick", "checkin:sick:2026-01-01", "sick", "2026-01-01", true},
		{"wrong prefix", "ping:great:2026-05-18", "", "", false},
		{"bad answer", "checkin:wonderful:2026-05-18", "", "", false},
		{"missing date", "checkin:great", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, date, ok := parseCheckinCallback(tc.input)
			if ok != tc.ok || answer != tc.answer || date != tc.date {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", answer, date, ok, tc.answer, tc.date, tc.ok)
			}
		})
	}
}
```

- [ ] **Step 2: Run — verify fails**

```bash
go test ./internal/notify/ -run "TestCheckinPromptButtons|TestParseCheckinCallback" -v
```

Expected: FAIL — undefined builders.

- [ ] **Step 3: Implement builders + parser**

Create `internal/notify/checkin_prompt.go`:

```go
package notify

import (
	"fmt"
	"strings"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// CheckinCallbackPrefix marks an inline-keyboard callback as a
// subjective-checkin answer. Telegram caps callback_data at 64
// bytes; "checkin:<answer>:<YYYY-MM-DD>" = 26 bytes worst case.
const CheckinCallbackPrefix = "checkin"

// buildCheckinPromptButtons returns the 2x2 inline keyboard for the
// morning prompt, localised via internal/health/i18n_*.go (the
// Telegram-side string set; the dashboard uses internal/ui/i18n_*.go
// separately).
//
// `date` is embedded in each callback_data so the webhook can validate
// the user is answering today's prompt, not a stale one from a chat
// scrollback.
func buildCheckinPromptButtons(lang, date string) ([][]InlineButton, string) {
	t := health.GetStrings(lang)
	row1 := []InlineButton{
		{Text: t["checkin_btn_great"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerGreat, date)},
		{Text: t["checkin_btn_ok"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerOK, date)},
	}
	row2 := []InlineButton{
		{Text: t["checkin_btn_meh"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerMeh, date)},
		{Text: t["checkin_btn_sick"], CallbackData: fmt.Sprintf("%s:%s:%s", CheckinCallbackPrefix, storage.CheckinAnswerSick, date)},
	}
	return [][]InlineButton{row1, row2}, t["checkin_prompt_text"]
}

// parseCheckinCallback returns (answer, date, ok) for a callback
// payload of the form `checkin:<answer>:<YYYY-MM-DD>`. ok=false on
// any deviation: unknown prefix, unknown answer, missing date.
func parseCheckinCallback(payload string) (string, string, bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 3 || parts[0] != CheckinCallbackPrefix {
		return "", "", false
	}
	if err := storage.ValidateCheckinAnswer(parts[1]); err != nil {
		return "", "", false
	}
	return parts[1], parts[2], true
}
```

- [ ] **Step 4: Add i18n strings to all three language files**

Append to `internal/health/i18n_en.go` (inside the map, alongside existing stress flag strings):

```go
	"checkin_prompt_text":      "How are you feeling this morning?",
	"checkin_btn_great":        "Great",
	"checkin_btn_ok":           "OK",
	"checkin_btn_meh":          "Meh",
	"checkin_btn_sick":         "Sick",
	"checkin_ack_great":        "Logged: Great. Have a good one.",
	"checkin_ack_ok":           "Logged: OK.",
	"checkin_ack_meh":          "Logged: Meh. Take it easy today.",
	"checkin_ack_sick":         "Logged: Sick. Rest up.",
	"checkin_ack_late":         "Logged after the morning report — saved for analytics.",
	"checkin_expired_note":     "<i>Want the report to reflect your state better? Answer the one-tap morning question tomorrow.</i>",
```

Append to `internal/health/i18n_ru.go` (mirroring keys):

```go
	"checkin_prompt_text":      "Как вы себя чувствуете этим утром?",
	"checkin_btn_great":        "Отлично",
	"checkin_btn_ok":           "Нормально",
	"checkin_btn_meh":          "Не очень",
	"checkin_btn_sick":         "Болен(а)",
	"checkin_ack_great":        "Записал: Отлично. Хорошего дня.",
	"checkin_ack_ok":           "Записал: Нормально.",
	"checkin_ack_meh":          "Записал: Не очень. Поберегите себя.",
	"checkin_ack_sick":         "Записал: Болен. Отдыхайте.",
	"checkin_ack_late":         "Записал после отчёта — пойдёт в аналитику.",
	"checkin_expired_note":     "<i>Хотите чтобы отчёт точнее отражал ваше состояние? Ответьте одним нажатием на утренний вопрос завтра.</i>",
```

Append to `internal/health/i18n_sr.go`:

```go
	"checkin_prompt_text":      "Kako se osećate jutros?",
	"checkin_btn_great":        "Odlično",
	"checkin_btn_ok":           "Normalno",
	"checkin_btn_meh":          "Onako",
	"checkin_btn_sick":         "Bolestan(a)",
	"checkin_ack_great":        "Zabeleženo: Odlično. Lep dan.",
	"checkin_ack_ok":           "Zabeleženo: Normalno.",
	"checkin_ack_meh":          "Zabeleženo: Onako. Štedite se danas.",
	"checkin_ack_sick":         "Zabeleženo: Bolestan. Odmarajte.",
	"checkin_ack_late":         "Zabeleženo posle izveštaja — ide u analitiku.",
	"checkin_expired_note":     "<i>Želite da izveštaj bolje odražava vaše stanje? Odgovorite jednim dodirom sutra.</i>",
```

- [ ] **Step 5: Run — verify passes**

```bash
go test ./internal/notify/ -run "TestCheckinPromptButtons|TestParseCheckinCallback" -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/notify/checkin_prompt.go internal/notify/checkin_prompt_test.go internal/health/i18n_en.go internal/health/i18n_ru.go internal/health/i18n_sr.go
git commit -m "feat(notify): morning check-in prompt builder + parser + i18n"
```

---

### Task 6: SendCheckinPrompt — full Telegram send with persistence

**Files:**
- Modify: `internal/notify/checkin_prompt.go`
- Test: `internal/notify/checkin_prompt_test.go` (extend)

- [ ] **Step 1: Write the failing test for the orchestration helper**

The helper composes the prompt and persists the `prompted` row. We can't unit-test the live Telegram POST, but we can test the surrounding logic by mocking the bot's `SendInlineKeyboard` call. Append to `internal/notify/checkin_prompt_test.go`:

```go
import (
	"errors"
	"testing"
	"time"

	"health-receiver/internal/storage"
)

type fakeBot struct {
	lastText  string
	lastRows  [][]InlineButton
	msgID     int64
	sendErr   error
	answerErr error
}

func (f *fakeBot) SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error) {
	f.lastText = text
	f.lastRows = rows
	return f.msgID, f.sendErr
}
func (f *fakeBot) AnswerCallbackQuery(qid, text string) error {
	return f.answerErr
}

type fakeCheckinStore struct {
	saved    bool
	lastDate string
	lastSrc  string
	lastMsg  int64
	lastExp  time.Time
}

func (s *fakeCheckinStore) SaveCheckinPrompted(date, source string, msgID int64, promptedAt, expiresAt time.Time) error {
	s.saved = true
	s.lastDate = date
	s.lastSrc = source
	s.lastMsg = msgID
	s.lastExp = expiresAt
	return nil
}

func TestSendCheckinPrompt_HappyPath(t *testing.T) {
	bot := &fakeBot{msgID: 42}
	store := &fakeCheckinStore{}
	now := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	cap := time.Date(2026, 5, 18, 11, 0, 0, 0, time.UTC)
	if err := SendCheckinPrompt(bot, store, "ru", "2026-05-18", now, cap); err != nil {
		t.Fatalf("send: %v", err)
	}
	if bot.lastText == "" || len(bot.lastRows) != 2 {
		t.Errorf("bot not called correctly: text=%q rows=%v", bot.lastText, bot.lastRows)
	}
	if !store.saved {
		t.Fatalf("store not invoked")
	}
	if store.lastMsg != 42 || !store.lastExp.Equal(cap) || store.lastSrc != storage.CheckinSourceTelegram {
		t.Errorf("store payload off: %+v", store)
	}
}

func TestSendCheckinPrompt_TelegramErrSkipsStore(t *testing.T) {
	bot := &fakeBot{sendErr: errors.New("boom")}
	store := &fakeCheckinStore{}
	now := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	err := SendCheckinPrompt(bot, store, "ru", "2026-05-18", now, now.Add(3*time.Hour))
	if err == nil {
		t.Fatal("expected error")
	}
	if store.saved {
		t.Fatal("store must not be written when Telegram send fails")
	}
}
```

- [ ] **Step 2: Run — verify fails**

```bash
go test ./internal/notify/ -run "TestSendCheckinPrompt" -v
```

Expected: FAIL — `undefined: SendCheckinPrompt`, `CheckinSourceTelegram`.

- [ ] **Step 3: Add the source constant + interfaces + helper**

Append to `internal/storage/subjective_checkin.go`:

```go
// Source enum (column subjective_checkins.source). MVP only emits
// telegram; dashboard answers (planned post-MVP) will use a separate
// source so coverage analytics can tell them apart.
const CheckinSourceTelegram = "telegram"
```

Append to `internal/notify/checkin_prompt.go`:

```go
import (
	"time"

	"health-receiver/internal/storage"
)

// CheckinBot abstracts the subset of *Bot that SendCheckinPrompt
// needs, so the orchestration can be tested with a fake.
type CheckinBot interface {
	SendInlineKeyboard(text string, rows [][]InlineButton) (int64, error)
}

// CheckinStore abstracts the subset of *storage.DB the prompt path
// needs. Same rationale as CheckinBot — keeps the test boundary thin.
type CheckinStore interface {
	SaveCheckinPrompted(date, source string, msgID int64, promptedAt, expiresAt time.Time) error
}

// SendCheckinPrompt builds the 2x2 inline keyboard, POSTs it to
// Telegram, and persists the resulting message_id in a `prompted`
// row. expiresAt is the morning-cap time — once the wall clock
// passes it, the row gets transitioned to `expired` by the
// scheduler and the morning report sends with a soft note.
//
// Store write is skipped when the Telegram send fails so we don't
// claim "we prompted them" when no message ever arrived.
func SendCheckinPrompt(bot CheckinBot, store CheckinStore, lang, date string, now, expiresAt time.Time) error {
	rows, text := buildCheckinPromptButtons(lang, date)
	msgID, err := bot.SendInlineKeyboard(text, rows)
	if err != nil {
		return err
	}
	return store.SaveCheckinPrompted(date, storage.CheckinSourceTelegram, msgID, now, expiresAt)
}
```

- [ ] **Step 4: Run — verify passes**

```bash
go test ./internal/notify/ -run "TestSendCheckinPrompt" -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/checkin_prompt.go internal/notify/checkin_prompt_test.go internal/storage/subjective_checkin.go
git commit -m "feat(notify): SendCheckinPrompt — orchestrates Telegram send + persistence"
```

---

### Task 7: Webhook handler — secret + chat_id validation + dispatch

**Files:**
- Create: `internal/notify/checkin_callback.go`, `internal/notify/checkin_callback_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/checkin_callback_test.go`:

```go
package notify

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeRouter struct {
	saveStatus string
	saveErr    error
	saveCalls  int
	lastDate   string
	lastSource string
	lastAnswer string
}

func (f *fakeRouter) SaveAnswer(date, source, answer string, _ time.Time) (string, error) {
	f.saveCalls++
	f.lastDate, f.lastSource, f.lastAnswer = date, source, answer
	return f.saveStatus, f.saveErr
}
func (f *fakeRouter) AnswerCallbackQuery(qid, text string) error { return nil }
func (f *fakeRouter) TriggerReport(schema string)                {}

func buildUpdateBody(t *testing.T, chatID, callbackData string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"update_id": 1,
		"callback_query": map[string]any{
			"id": "qid-1",
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
	h := NewWebhookHandler(WebhookConfig{Secret: "good", TokenHeader: "expected-token"})
	req := httptest.NewRequest("POST", "/api/telegram/webhook/good", bytes.NewReader(buildUpdateBody(t, "111", "checkin:great:2026-05-18")))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-token")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
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

func TestWebhook_HappyPath(t *testing.T) {
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
		t.Fatalf("router.SaveAnswer not called exactly once: %d", router.saveCalls)
	}
	if router.lastAnswer != "great" || router.lastDate != "2026-05-18" {
		t.Fatalf("router got wrong payload: %+v", router)
	}
}

func TestWebhook_RejectsMalformedCallback(t *testing.T) {
	router := &fakeRouter{}
	h := NewWebhookHandler(WebhookConfig{
		Secret: "good",
		TenantFinder: func(chat string) (CheckinTenant, bool) {
			return CheckinTenant{Router: router}, true
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
	if !strings.Contains(rec.Body.String(), "ignored") {
		// soft signal in body for log readers
		t.Logf("body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run — verify fails**

```bash
go test ./internal/notify/ -run "TestWebhook" -v
```

Expected: FAIL — `undefined: NewWebhookHandler`, `WebhookConfig`, `CheckinTenant`, `CheckinTenant.Router`.

- [ ] **Step 3: Implement the handler**

Create `internal/notify/checkin_callback.go`:

```go
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
)

// CheckinAnswerRouter is what the webhook calls after secret + tenant
// validation succeed. Split from the bot abstraction so the same
// interface can be backed by *storage.DB in production and a fake
// in tests.
type CheckinAnswerRouter interface {
	// SaveAnswer persists the answer and returns the resulting status
	// ("answered" | "late_answered"). Implementation routes to the
	// right tenant's DB.
	SaveAnswer(date, source, answer string, answeredAt time.Time) (string, error)
	// AnswerCallbackQuery acks the Telegram callback (kills the
	// loading spinner on the button).
	AnswerCallbackQuery(callbackQueryID, text string) error
	// TriggerReport runs the morning-report send async after a
	// successful answer. No-op when the report has already been sent
	// for today (idempotent on the tenant side).
	TriggerReport(schema string)
}

// CheckinTenant carries the per-tenant routing context the webhook
// needs after a chat_id lookup. Schema is for logging + report
// trigger; Lang drives the ack text; Router does the actual save.
type CheckinTenant struct {
	Schema string
	Lang   string
	Router CheckinAnswerRouter
}

// WebhookConfig configures NewWebhookHandler.
//
// Secret is the URL-path segment (validated constant-time).
// TokenHeader is the optional Telegram `setWebhook?secret_token=...`
// value (sent back on every update as `X-Telegram-Bot-Api-Secret-Token`).
// Leave TokenHeader empty to disable the header check.
// TenantFinder maps the inbound chat_id to a CheckinTenant; returns
// found=false to silently reject (200 OK to prevent Telegram retries).
type WebhookConfig struct {
	Secret       string
	TokenHeader  string
	TenantFinder func(chat string) (CheckinTenant, bool)
}

// NewWebhookHandler returns an http.HandlerFunc for the Telegram
// callback. Path shape: `/api/telegram/webhook/<secret>`. The handler
// returns fast (single store write + one outbound ack) so the
// Telegram side never times out; the report trigger runs async in
// the router.
func NewWebhookHandler(cfg WebhookConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// URL-path secret. Constant-time compare so a timing attack
		// can't enumerate the secret one byte at a time.
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
						ID json.RawMessage `json:"id"` // Telegram sends int; coerce to string
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
		status, err := tenant.Router.SaveAnswer(date, "telegram", answer, time.Now())
		if err != nil {
			log.Printf("telegram webhook: save %s tenant=%s err=%v", answer, tenant.Schema, err)
			// 200 to suppress retry loop; the error is logged.
			fmt.Fprintln(w, "ignored: save error")
			return
		}
		ack := ackText(tenant.Lang, answer, status)
		if err := tenant.Router.AnswerCallbackQuery(upd.CallbackQuery.ID, ack); err != nil {
			log.Printf("telegram webhook: ack: %v", err)
		}
		// Trigger report ONLY when answered in time (not on late answers — the
		// report already went out, no point in firing again).
		if status == "answered" {
			tenant.Router.TriggerReport(tenant.Schema)
		}
		fmt.Fprintln(w, "ok")
	}
}

// ackText picks the localised toast string for the Telegram callback
// acknowledgement based on the answer and whether the save was in
// time or post-hoc.
func ackText(lang, answer, status string) string {
	// import health i18n here to keep the helper colocated; cross-package
	// import via the existing package alias used elsewhere in this file.
	strs := healthStrings(lang)
	if status == "late_answered" {
		return strs["checkin_ack_late"]
	}
	switch answer {
	case "great":
		return strs["checkin_ack_great"]
	case "ok":
		return strs["checkin_ack_ok"]
	case "meh":
		return strs["checkin_ack_meh"]
	case "sick":
		return strs["checkin_ack_sick"]
	}
	return ""
}

// healthStrings is a thin wrapper so checkin_callback.go can stay
// import-clean while still reaching the localisation map. Pulled
// out so the test file doesn't transitively need the health package.
func healthStrings(lang string) map[string]string {
	// internal/health/i18n_*.go exposes GetStrings.
	return _healthStrings(lang)
}
```

Then add the alias near the top of `internal/notify/checkin_prompt.go` so both files share it:

```go
// _healthStrings is a function-typed pointer so it can be swapped in
// tests if needed. Defaults to health.GetStrings.
var _healthStrings = func(lang string) map[string]string {
	// Use health.GetStrings, imported above.
	return health.GetStrings(lang)
}
```

(That import already exists in `checkin_prompt.go`. The variable lives in the package so both files can read it.)

- [ ] **Step 4: Run — verify passes**

```bash
go test ./internal/notify/ -run "TestWebhook" -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/checkin_callback.go internal/notify/checkin_callback_test.go internal/notify/checkin_prompt.go
git commit -m "feat(notify): Telegram webhook handler for check-in callbacks"
```

---

### Task 8: Wire webhook into the server + concrete router implementation

**Files:**
- Create: `internal/notify/checkin_router.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Implement the production router (DB-backed CheckinAnswerRouter)**

Create `internal/notify/checkin_router.go`:

```go
package notify

import (
	"log"
	"time"

	"health-receiver/internal/storage"
)

// MultiTenantRouter is the production CheckinAnswerRouter. It owns a
// lookup from chat_id to *storage.DB and the per-tenant Telegram bot
// (for the ack), and a callback that re-runs the morning trigger
// async after an answer lands.
type MultiTenantRouter struct {
	LookupByChat  func(chat string) (db *storage.DB, schema, lang, token, chatID string, ok bool)
	TriggerReportFn func(schema string)

	// answerBots is built lazily per chat — one Bot per tenant, cached.
	bots map[string]*Bot
}

// SaveAnswer dispatches the save to the right tenant DB. Source is
// forced to telegram; the webhook is the only writer that uses this
// router.
func (m *MultiTenantRouter) SaveAnswer(date, source, answer string, answeredAt time.Time) (string, error) {
	// The webhook hands us a chat_id via the TenantFinder closure that
	// composes this router; SaveAnswer receives (date, source, answer)
	// without the chat. We resolve the DB through a per-call closure
	// captured by the WebhookConfig.TenantFinder factory in main.go.
	//
	// This wrapper exists only so the webhook can speak through a
	// stable interface; the actual save lives one indirection deeper.
	return m.saveImpl(date, source, answer, answeredAt)
}

// saveImpl is set per-tenant by the TenantFinder factory. Replaced
// per inbound update.
func (m *MultiTenantRouter) saveImpl(date, source, answer string, answeredAt time.Time) (string, error) {
	// Should be replaced by per-update closure. If we hit this, the
	// composition was wrong.
	return "", fmt.Errorf("checkin router: saveImpl not bound")
}

// AnswerCallbackQuery answers via the per-tenant bot. The handler
// passes the chat_id implicitly through the closure-bound bot.
func (m *MultiTenantRouter) AnswerCallbackQuery(qid, text string) error {
	return m.ackImpl(qid, text)
}
func (m *MultiTenantRouter) ackImpl(qid, text string) error {
	return fmt.Errorf("checkin router: ackImpl not bound")
}

// TriggerReport calls the per-tenant morning-report retrigger.
func (m *MultiTenantRouter) TriggerReport(schema string) {
	if m.TriggerReportFn != nil {
		m.TriggerReportFn(schema)
	}
}
```

Note: this is intentionally an over-engineered scaffold — the simpler design is to bind the `Router` value per-update inside `TenantFinder` in main. The next step does exactly that and the file above can be deleted or kept as a placeholder for future extension. **Prefer the simpler design below — delete `checkin_router.go` if you find the indirection awkward.**

- [ ] **Step 2: Wire the webhook into the server (simpler design)**

Modify `cmd/server/main.go`. After the existing route registrations (look for the block that calls `handler.New(...).Register(mux)` and `uiHandler.Register(mux)`), add:

```go
	// Telegram check-in webhook. Path: /api/telegram/webhook/<secret>.
	// Secret is read from env TELEGRAM_WEBHOOK_SECRET. When unset, the
	// webhook is not registered — no-Telegram tenants are unaffected.
	if secret := os.Getenv("TELEGRAM_WEBHOOK_SECRET"); secret != "" {
		tokenHeader := os.Getenv("TELEGRAM_WEBHOOK_TOKEN_HEADER")
		notifyDefaults := envNotifyDefaults
		mux.HandleFunc("/api/telegram/webhook/", notify.NewWebhookHandler(notify.WebhookConfig{
			Secret:      secret,
			TokenHeader: tokenHeader,
			TenantFinder: func(chat string) (notify.CheckinTenant, bool) {
				db, schema, ok := mgr.DBForTelegramChatID(context.Background(), notifyDefaults, chat)
				if !ok {
					return notify.CheckinTenant{}, false
				}
				cfg := db.GetNotifyConfig(notifyDefaults)
				bot := notify.NewBot(cfg.Token, cfg.ChatID)
				return notify.CheckinTenant{
					Schema: schema,
					Lang:   cfg.Lang,
					Router: &liveRouter{db: db, bot: bot, triggerReport: makeReportTrigger(mgr, schema, notifyDefaults)},
				}, true
			},
		}))
		log.Printf("Telegram webhook registered at /api/telegram/webhook/<secret> (token header: %v)", tokenHeader != "")
	}
```

Then in the same file, add the `liveRouter` near the end of the file (or in a new helper section):

```go
// liveRouter implements notify.CheckinAnswerRouter against a
// *storage.DB + *notify.Bot for one tenant. Created per inbound
// update by the webhook's TenantFinder.
type liveRouter struct {
	db            *storage.DB
	bot           *notify.Bot
	triggerReport func()
}

func (r *liveRouter) SaveAnswer(date, source, answer string, answeredAt time.Time) (string, error) {
	return r.db.SaveCheckinAnswer(date, source, answer, answeredAt)
}
func (r *liveRouter) AnswerCallbackQuery(qid, text string) error {
	return r.bot.AnswerCallbackQuery(qid, text)
}
func (r *liveRouter) TriggerReport(schema string) {
	if r.triggerReport != nil {
		go r.triggerReport()
	}
}

// makeReportTrigger captures the dependencies needed to (re)run the
// morning trigger for a tenant. Used by the webhook to fire the
// report async after a successful answer.
func makeReportTrigger(mgr *tenants.Manager, schema string, defaults storage.NotifyConfig) func() {
	return func() {
		db, _ := mgr.GetOrCreate(context.Background(), schema)
		if db == nil {
			return
		}
		cfg := db.GetNotifyConfig(defaults)
		if !cfg.Enabled() {
			return
		}
		today := time.Now().In(tenantTZOrUTC(db, defaults, schema)).Format("2006-01-02")
		if db.HasSentMorningReport(today) {
			return
		}
		ncfg := buildNotifyCfg(db, cfg)
		bot := notify.NewBot(ncfg.Token, ncfg.ChatID)
		sent, reason, err := notify.SendMorningSmart(bot, db, ncfg, false)
		if err != nil {
			log.Printf("checkin-trigger: send: %v", err)
			return
		}
		if sent {
			if perr := db.MarkMorningReportSent(today); perr != nil {
				log.Printf("checkin-trigger: mark sent: %v", perr)
			}
			log.Printf("checkin-trigger: sent (reason=%s) for %s", reason, today)
		}
	}
}
```

Also at the top of main.go add `"os"` to the import block if not already present.

If `checkin_router.go` was created in Step 1, delete it now:

```bash
rm internal/notify/checkin_router.go
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: clean. Any "imported and not used" issues — fix the import set (notably `os` and the `notify` alias).

- [ ] **Step 4: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/checkin_router.go cmd/server/main.go
git commit -m "feat(server): register Telegram check-in webhook + per-tenant router"
```

(If you deleted checkin_router.go, the add will be a no-op for that file and the commit will only contain main.go — that's fine.)

---

### Task 9: Scheduler gate — prompt before report, expire at cap

**Files:**
- Modify: `cmd/server/main.go:runMorningSmartRetry`, `internal/notify/report.go`

- [ ] **Step 1: Identify the modification scope**

`runMorningSmartRetry` (cmd/server/main.go around line 623) currently:
1. Computes cap.
2. Calls `MaybeFireAll`.
3. Loops every 15min: if past cap force-send, else call `SendMorningSmart`.

New behavior:
1. Computes cap.
2. If Telegram is configured AND `SleepSettled(today)` AND no prompt yet, send check-in prompt.
3. Each tick: if past cap and prompt unanswered → `ExpireCheckin`, append expired note to report, force-send.
4. Each tick: if prompt unanswered and not past cap → defer (don't send report yet).
5. The webhook's own report trigger handles the answered case.

- [ ] **Step 2: Write the failing test for the gate decision helper**

Create `cmd/server/morning_gate_test.go` (or use an existing test file in `internal/notify` — the helper itself is pure):

```go
package notify

import (
	"testing"
	"time"
)

func TestMorningGate(t *testing.T) {
	t0 := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	cap := time.Date(2026, 5, 18, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name           string
		now            time.Time
		sleepSettled   bool
		hasCheckin     bool
		checkinStatus  string
		hasReportSent  bool
		want           MorningAction
	}{
		{"sleep not settled before cap", t0, false, false, "", false, MorningActionWait},
		{"settled, no prompt yet", t0, true, false, "", false, MorningActionPrompt},
		{"prompt sent, answered → send report", t0, true, true, "answered", false, MorningActionSendReport},
		{"prompt sent, not answered, before cap → wait", t0, true, true, "prompted", false, MorningActionWait},
		{"past cap, prompt unanswered → expire+force", t0.Add(3 * time.Hour), true, true, "prompted", false, MorningActionExpireAndForce},
		{"past cap, no prompt ever → force without expire", t0.Add(3 * time.Hour), true, false, "", false, MorningActionForce},
		{"already sent → no-op", t0, true, true, "answered", true, MorningActionNoop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideMorningAction(MorningGateInputs{
				Now:              tc.now,
				Cap:              cap,
				SleepSettled:     tc.sleepSettled,
				HasCheckin:       tc.hasCheckin,
				CheckinStatus:    tc.checkinStatus,
				ReportAlreadySent: tc.hasReportSent,
			})
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run — verify fails**

```bash
go test ./internal/notify/ -run TestMorningGate -v
```

Expected: FAIL — undefined `MorningAction`, `DecideMorningAction`, `MorningGateInputs`.

- [ ] **Step 4: Implement the gate decision (pure)**

Create `internal/notify/morning_gate.go`:

```go
package notify

import "time"

// MorningAction enumerates the scheduler decisions for one tick.
type MorningAction string

const (
	MorningActionNoop          MorningAction = "noop"           // report already sent today
	MorningActionWait          MorningAction = "wait"           // try again next tick
	MorningActionPrompt        MorningAction = "prompt"         // send check-in prompt (first time)
	MorningActionSendReport    MorningAction = "send_report"    // user answered, send report now
	MorningActionExpireAndForce MorningAction = "expire_and_force" // cap reached, mark expired, force-send
	MorningActionForce         MorningAction = "force"          // cap reached, no check-in row, force-send
)

// MorningGateInputs carries the per-tick state DecideMorningAction
// needs. Kept as a struct so adding a future signal doesn't churn
// every call site.
type MorningGateInputs struct {
	Now               time.Time
	Cap               time.Time
	SleepSettled      bool
	HasCheckin        bool
	CheckinStatus     string
	ReportAlreadySent bool
}

// DecideMorningAction is the pure decision table. Lives separately
// from the scheduler loop so the policy stays auditable and tests
// don't need a fake scheduler.
func DecideMorningAction(in MorningGateInputs) MorningAction {
	if in.ReportAlreadySent {
		return MorningActionNoop
	}
	past := !in.Now.Before(in.Cap)

	if past {
		// At/after cap. We always send the report; the only question is
		// whether a still-prompted check-in needs expiring first.
		if in.HasCheckin && in.CheckinStatus == "prompted" {
			return MorningActionExpireAndForce
		}
		return MorningActionForce
	}

	// Before cap.
	if !in.SleepSettled {
		return MorningActionWait
	}
	if !in.HasCheckin {
		return MorningActionPrompt
	}
	if in.CheckinStatus == "answered" || in.CheckinStatus == "late_answered" {
		return MorningActionSendReport
	}
	// prompted, before cap → keep waiting.
	return MorningActionWait
}
```

- [ ] **Step 5: Run — verify passes**

```bash
go test ./internal/notify/ -run TestMorningGate -v
```

Expected: PASS.

- [ ] **Step 6: Plug into runMorningSmartRetry**

Modify `cmd/server/main.go`. Replace the `for { ... }` body of `runMorningSmartRetry` (around lines 649-682) with a version that consults the new helper. Show the complete new loop body — engineers reading this plan out of order need the whole replacement:

```go
	for {
		today := time.Now().In(loc).Format("2006-01-02")
		if db.HasSentMorningReport(today) {
			log.Println("morning smart-retry: already sent, exiting loop")
			return
		}

		// Re-resolve fresh per tick: cap may shift if settings change,
		// sleep_settled flips as data arrives, checkin status changes via
		// webhook.
		now := time.Now().In(loc)
		settled := db.SleepSettled(today)
		row, _ := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
		inputs := notify.MorningGateInputs{
			Now:               now,
			Cap:               cap,
			SleepSettled:      settled,
			HasCheckin:        row != nil,
			ReportAlreadySent: false, // already checked above
		}
		if row != nil {
			inputs.CheckinStatus = row.Status
		}
		action := notify.DecideMorningAction(inputs)
		log.Printf("morning smart-retry: action=%s settled=%v checkin=%+v", action, settled, row)

		switch action {
		case notify.MorningActionWait:
			time.Sleep(tick)
			continue

		case notify.MorningActionPrompt:
			ncfg := buildNotifyCfg(db, db.GetNotifyConfig(envNotifyDefaults))
			promptBot := notify.NewBot(ncfg.Token, ncfg.ChatID)
			if err := notify.SendCheckinPrompt(promptBot, db, ncfg.Lang, today, now, cap); err != nil {
				log.Printf("morning smart-retry: prompt: %v", err)
			} else {
				log.Printf("morning smart-retry: check-in prompt sent")
			}
			time.Sleep(tick)
			continue

		case notify.MorningActionExpireAndForce:
			if _, err := db.ExpireCheckin(today, storage.CheckinSourceTelegram, now); err != nil {
				log.Printf("morning smart-retry: expire checkin: %v", err)
			}
			// fallthrough into force-send.
			fallthrough
		case notify.MorningActionForce, notify.MorningActionSendReport:
			ensureTodayAIInsight(db, mgr.AIDefaultsFor(context.Background(), schema), ncfg.Lang)
			past := action == notify.MorningActionForce || action == notify.MorningActionExpireAndForce
			sent, reason, err := notify.SendMorningSmart(bot, db, ncfg, past)
			if err != nil {
				log.Printf("morning smart-retry: send: %v", err)
			}
			if sent {
				if perr := db.MarkMorningReportSent(today); perr != nil {
					log.Printf("morning smart-retry: mark sent: %v", perr)
				}
				log.Printf("morning smart-retry: sent (reason=%s, forced=%v)", reason, past)
				return
			}
			if past {
				log.Printf("morning smart-retry: past cap but not sent (reason=%s), giving up", reason)
				return
			}
			time.Sleep(tick)

		case notify.MorningActionNoop:
			return
		}
	}
```

At the top of `main.go`, ensure `health-receiver/internal/storage` is imported and aliased as `storage` (it already is).

- [ ] **Step 7: Build + test**

```bash
go build ./... && go test ./...
```

Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add internal/notify/morning_gate.go internal/notify/morning_gate_test.go cmd/server/main.go
git commit -m "feat(scheduler): gate morning report on check-in answer, prompt on settle"
```

---

### Task 10: Dashboard confirmation line

**Files:**
- Modify: `internal/storage/briefing.go`, `internal/health/types.go`, `internal/ui/page_data.go` (or wherever dashboard data lives), `internal/ui/templates/pages/dashboard.html`, `internal/ui/i18n_{en,ru,sr}.go`

- [ ] **Step 1: Add the SubjectiveCheckin field to the dashboard payload type**

Add to `internal/health/types.go` (near the existing fields rendered into the dashboard hero):

```go
// SubjectiveCheckinSummary is the read-shape rendered into the
// dashboard hero. nil when no check-in row exists for today.
type SubjectiveCheckinSummary struct {
	Status string `json:"status"`           // prompted | answered | expired | late_answered
	Answer string `json:"answer,omitempty"` // great | ok | meh | sick (empty when status=prompted/expired)
}
```

Then add `SubjectiveCheckin *SubjectiveCheckinSummary \`json:"subjective_checkin,omitempty"\`` to the struct that GetHealthBriefing returns to the UI. (Inspect `internal/health/types.go` for the briefing/dashboard struct — likely `HealthBriefing`. Insert near `EnergyBank *EnergyBank`.)

- [ ] **Step 2: Populate it in briefing.go**

Modify `internal/storage/briefing.go`. After the `SaveEnergyBankSnapshot` async block (around line 533), add:

```go
	if row, err := s.GetTodayCheckin(*lastDate, CheckinSourceTelegram); err == nil && row != nil {
		resp.SubjectiveCheckin = &health.SubjectiveCheckinSummary{
			Status: row.Status,
			Answer: row.Answer,
		}
	}
```

- [ ] **Step 3: Add i18n strings (UI layer)**

Append to `internal/ui/i18n_en.go` (sleep stress chip block region):

```go
	"checkin_today_label":   "Your morning answer:",
	"checkin_answer_great":  "Great",
	"checkin_answer_ok":     "OK",
	"checkin_answer_meh":    "Meh",
	"checkin_answer_sick":   "Sick",
```

Append to `internal/ui/i18n_ru.go`:

```go
	"checkin_today_label":   "Ваш утренний ответ:",
	"checkin_answer_great":  "Отлично",
	"checkin_answer_ok":     "Нормально",
	"checkin_answer_meh":    "Не очень",
	"checkin_answer_sick":   "Болен(а)",
```

Append to `internal/ui/i18n_sr.go`:

```go
	"checkin_today_label":   "Vaš jutarnji odgovor:",
	"checkin_answer_great":  "Odlično",
	"checkin_answer_ok":     "Normalno",
	"checkin_answer_meh":    "Onako",
	"checkin_answer_sick":   "Bolestan(a)",
```

- [ ] **Step 4: Render the confirmation line**

Modify `internal/ui/templates/pages/dashboard.html`. After the `</div>` that closes `#hero-section` (around line 104) and before `{{if .Cards}}`, insert:

```html
{{if .SubjectiveCheckin}}{{if .SubjectiveCheckin.Answer}}
<div class="subjective-checkin-line">
  <span class="subjective-checkin-label">{{T .Lang "checkin_today_label"}}</span>
  <span class="subjective-checkin-answer">{{T .Lang (printf "checkin_answer_%s" .SubjectiveCheckin.Answer)}}</span>
</div>
{{end}}{{end}}
```

If `.SubjectiveCheckin` isn't on the page-data struct passed to this template, plumb it: find the page-data struct in `internal/ui/page_data.go` or wherever the dashboard render-call constructs the template data, and add `SubjectiveCheckin *health.SubjectiveCheckinSummary` to it. Populate from the briefing payload.

- [ ] **Step 5: Style it minimally**

Append to `internal/ui/style.go`:

```go
.subjective-checkin-line {
  margin: 8px 0;
  font-size: 13px;
  color: var(--text-secondary);
}
.subjective-checkin-label {
  margin-right: 6px;
}
.subjective-checkin-answer {
  font-weight: 600;
  color: var(--text);
}
```

- [ ] **Step 6: Build + test**

```bash
go build ./...
go test ./...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/health/types.go internal/storage/briefing.go internal/ui/page_data.go internal/ui/templates/pages/dashboard.html internal/ui/style.go internal/ui/i18n_en.go internal/ui/i18n_ru.go internal/ui/i18n_sr.go
git commit -m "feat(ui): dashboard one-line confirmation of today's morning check-in"
```

---

### Task 11: PR + smoke test + setWebhook on prod

**Files:** none (operational)

- [ ] **Step 1: Push the branch**

```bash
git push -u origin feat/subjective-checkin-mvp
```

- [ ] **Step 2: Open PR**

```bash
gh pr create --title "feat: subjective daily check-in MVP (Telegram-first)" --body "$(cat <<'EOF'
## Summary
- Adds a one-tap morning check-in via Telegram inline keyboard (great / ok / meh / sick).
- Scheduler now sends the check-in prompt on the same tick that would have sent the morning report, once SleepSettled returns true.
- The morning report is gated on the answer until MorningCapHour; past cap, the row expires and the report force-sends with a soft note.
- Webhook at /api/telegram/webhook/<secret>, secured by URL secret + optional X-Telegram-Bot-Api-Secret-Token header + chat_id lookup against tenant config.
- Late answers (after cap) are accepted as late_answered for analytics, separated from primary-validation answered.
- Tenants without Telegram configured are skipped entirely; morning flow proceeds as before.

## Out of scope
- No score is read or modified by this PR (readiness, EnergyBank, stress verdict unchanged).
- No admin/debug coverage table — tracked as PR 2.
- No correlation/validation queries — tracked as PR 3.

## Test plan
- [x] go build ./...
- [x] go test ./...
- [ ] Set TELEGRAM_WEBHOOK_SECRET in server .env, restart container.
- [ ] Call Telegram setWebhook with the URL + secret_token.
- [ ] Wait for next morning settle; verify prompt arrives, tap a button, verify ack toast, verify report follows.
- [ ] Inspect subjective_checkins row: status=answered, answer=<picked>.
- [ ] Verify dashboard renders the "Ваш утренний ответ" line.
- [ ] Force-test the expiry path by setting MorningCapHour earlier for one cycle.
EOF
)"
```

- [ ] **Step 3: After merge — set env vars on prod**

SSH into the VPS, edit `/root/health/.env` to add:

```
TELEGRAM_WEBHOOK_SECRET=<32 random bytes hex>
TELEGRAM_WEBHOOK_TOKEN_HEADER=<32 random bytes hex>
```

Restart: `docker compose up -d`.

- [ ] **Step 4: Register the webhook with Telegram**

```bash
curl -X POST "https://api.telegram.org/bot<BOT_TOKEN>/setWebhook" \
  -H 'Content-Type: application/json' \
  -d "{
    \"url\": \"https://health.dzarlax.dev/api/telegram/webhook/<TELEGRAM_WEBHOOK_SECRET>\",
    \"secret_token\": \"<TELEGRAM_WEBHOOK_TOKEN_HEADER>\",
    \"allowed_updates\": [\"callback_query\"]
  }"
```

Verify: `curl https://api.telegram.org/bot<BOT_TOKEN>/getWebhookInfo`.

- [ ] **Step 5: End-to-end smoke**

Wait for the next morning's `SleepSettled=true` tick (or simulate by force-restarting the container after sleep is settled). Observe in order:

1. Prompt arrives in Telegram chat with 4 buttons.
2. Tap "Нормально".
3. Toast shows "Записал: Нормально."
4. Within ~5s, the morning report arrives in the same chat.
5. Open dashboard, verify "Ваш утренний ответ: Нормально" line appears.
6. Query `subjective_checkins` via psql to confirm one row with status=answered, answer=ok, answered_at within the last minute.

---

## Self-Review

**Spec coverage check:**

- ✅ "User can answer with one Telegram tap" → Tasks 5 + 6 + 7.
- ✅ "Answer stored idempotently for tenant/date" → Task 2 (UPSERT on PK + transactional read-modify-write).
- ✅ "Morning report waits for answer until cap-time, then falls back safely" → Task 9.
- ✅ "Dashboard confirms today's answer" → Task 10.
- ✅ "No existing report path breaks when Telegram webhook is not configured or user ignores prompt" → Task 8 (webhook only registers when env secret set) + Task 9 gate falls into ActionForce when no checkin row exists.
- ✅ "Multi-tenant model remains one bot/chat per tenant" → Task 3 (DBForTelegramChatID walks per-tenant pool map).
- ✅ Storage schema with PK (date, source) and lifecycle states → Task 1 + Task 2.
- ✅ Telegram webhook with secret + token header + chat_id check → Task 7.
- ✅ Late-answer policy (`late_answered`, no report retrigger) → Task 2 (transition table) + Task 7 (only fires TriggerReport when status=answered).
- ✅ i18n EN/RU/SR for buttons, prompt, ack, expired note, dashboard line → Tasks 5 + 10.
- ✅ Tests pin transition logic, payload shape, webhook validation, gate decision → Tasks 1, 2, 4, 5, 7, 9.

**Placeholder scan:** No `TODO`, no `tbd`, no "implement appropriate". Each step has the code it adds. The one soft hand-off is Task 8 Step 1 (the scaffold `checkin_router.go`), explicitly marked as "delete in favour of the simpler design in Step 2" — that's a deliberate choice between two options, not a placeholder.

**Type consistency:**
- `CheckinSourceTelegram = "telegram"` is defined in Task 6 Step 3 and used in Tasks 7 (`source = "telegram"` literal in fakeRouter dispatch), 9 (`storage.CheckinSourceTelegram` in `GetTodayCheckin` calls), 10 (briefing fetch). Consistent.
- `CheckinAnswer*` constants defined Task 2 used in Tasks 5 (parser), 7 (callback validation).
- `MorningAction*` constants defined Task 9 used only in Task 9 — fine.
- `CheckinTenant.Router` is `CheckinAnswerRouter` interface; implemented by `liveRouter` in Task 8 (cmd/server/main.go) and `fakeRouter` in tests. Method signatures match: `SaveAnswer(date, source, answer string, answeredAt time.Time) (string, error)`, `AnswerCallbackQuery(qid, text string) error`, `TriggerReport(schema string)`. ✓

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-18-subjective-checkin-mvp.md`. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, two-stage review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints for review.

Which approach?
