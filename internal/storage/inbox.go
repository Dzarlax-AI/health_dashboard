package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	InboxKindMorningReport = "morning_report"
	InboxKindPromptCheckin = "prompt_checkin"

	SettingInAppInboxEnabled = "in_app_inbox_enabled"

	CheckinSourceInbox = "inbox"

	CheckinChannelIOSApp   = "ios_app"
	CheckinChannelIOSPush  = "ios_push"
	CheckinChannelWeb      = "web"
	CheckinChannelTelegram = "telegram"

	CheckinAnswerStatusSaved           = "saved"
	CheckinAnswerStatusAlreadyAnswered = "already_answered"
	CheckinAnswerStatusExpired         = "expired"

	CheckinTimingOnTime = "on_time"
	CheckinTimingLate   = "late"
)

// ErrCheckinPromptExpired indicates the prompt's local date does not match the
// tenant's current local date. Callers must reject the mutation without altering
// the stored check-in row (409 {"status":"expired"} on HTTP, silent ack on Telegram).
var ErrCheckinPromptExpired = errors.New("check-in prompt expired past local date")

// ErrCheckinPromptNotFound indicates the requested prompt ID or date does not exist.
var ErrCheckinPromptNotFound = errors.New("check-in prompt not found")

const inboxItemsTableDDL = `
CREATE TABLE IF NOT EXISTS inbox_items (
	id             UUID PRIMARY KEY,
	logical_key    TEXT NOT NULL UNIQUE,
	kind           TEXT NOT NULL CHECK (kind IN ('morning_report','prompt_checkin')),
	local_date     TEXT NOT NULL,
	created_at     TIMESTAMPTZ NOT NULL,
	report_id      UUID REFERENCES report_history(id),
	checkin_date   TEXT,
	checkin_source TEXT,
	read_at        TIMESTAMPTZ,
	CHECK (
		(kind = 'morning_report' AND report_id IS NOT NULL AND checkin_date IS NULL AND checkin_source IS NULL) OR
		(kind = 'prompt_checkin' AND report_id IS NULL AND checkin_date IS NOT NULL AND checkin_source IS NOT NULL)
	),
	FOREIGN KEY (checkin_date, checkin_source) REFERENCES subjective_checkins(date, source)
);
CREATE INDEX IF NOT EXISTS idx_inbox_items_feed ON inbox_items (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_inbox_items_kind_date ON inbox_items (kind, local_date DESC);
`

func (s *DB) EnsureInboxItemsTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, inboxItemsTableDDL)
	return err
}

// IsInAppInboxEnabled reports whether in-app Inbox cards and unified prompt
// endpoints are enabled for this tenant. Defaults to false for phased
// per-profile rollout; enabled only when explicitly opted in via the
// per-tenant setting (`in_app_inbox_enabled`) or process env
// (`IN_APP_INBOX_ENABLED`).
func IsInAppInboxEnabled(s *DB) bool {
	defaultEnabled := false
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("IN_APP_INBOX_ENABLED"))); raw != "" {
		defaultEnabled = raw == "true" || raw == "1" || raw == "on" || raw == "yes"
	}
	if s == nil {
		return defaultEnabled
	}
	return getSettingBool(s, SettingInAppInboxEnabled, defaultEnabled)
}

// ResolveReportLocation resolves an IANA timezone name using the same
// time.Local fallback across scheduler, Telegram callbacks, and Inbox API.
func ResolveReportLocation(tz string) *time.Location {
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}

// TenantLocalDate returns YYYY-MM-DD in the tenant's effective report timezone,
// falling back to time.Local identically across scheduler, Telegram, and API.
func (s *DB) TenantLocalDate(defaults NotifyConfig, now time.Time) string {
	tz := defaults.Timezone
	if s != nil {
		tz = s.GetNotifyConfig(defaults).Timezone
	}
	return now.In(ResolveReportLocation(tz)).Format("2006-01-02")
}

func MorningReportInboxLogicalKey(localDate string) string {
	return InboxKindMorningReport + ":" + localDate
}

func CheckinInboxLogicalKey(localDate string) string {
	return InboxKindPromptCheckin + ":" + localDate
}

func ValidateCheckinChannel(channel string) error {
	switch channel {
	case CheckinChannelIOSApp, CheckinChannelIOSPush, CheckinChannelWeb, CheckinChannelTelegram:
		return nil
	default:
		return fmt.Errorf("invalid check-in channel %q", channel)
	}
}

func ValidateClientCheckinChannel(channel string) error {
	switch channel {
	case CheckinChannelIOSApp, CheckinChannelIOSPush, CheckinChannelWeb:
		return nil
	default:
		return fmt.Errorf("invalid client check-in channel %q", channel)
	}
}

var (
	reHTMLBreak = regexp.MustCompile(`(?i)<br\s*/?>`)
	reHTMLTag   = regexp.MustCompile(`<[^>]+>`)
	reBlankRun  = regexp.MustCompile(`\n{3,}`)
)

// NormalizeReportPlainText converts stored Telegram-flavoured HTML plain_body
// into clean plain text so iOS and Web never execute or parse raw HTML.
func NormalizeReportPlainText(raw string) string {
	if raw == "" {
		return ""
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = reHTMLBreak.ReplaceAllString(normalized, "\n")
	normalized = reHTMLTag.ReplaceAllString(normalized, "")
	normalized = html.UnescapeString(normalized)
	normalized = reBlankRun.ReplaceAllString(normalized, "\n\n")
	return strings.TrimSpace(normalized)
}

// PublishMorningReportToInbox atomically archives a durable morning report in
// report_history and publishes/updates its logical inbox_items card in a single
// transaction. When allowNewRevision is false and a morning card already exists
// for local_date, the existing archived report is loaded into h and created=false
// is returned without inserting a duplicate report or card.
func (s *DB) PublishMorningReportToInbox(ctx context.Context, h *ReportHistory, allowNewRevision bool) (uuid.UUID, bool, error) {
	if h == nil || h.IsPreview || h.Kind != "morning" || h.LocalDate == "" || h.DeliveryKey == nil || *h.DeliveryKey == "" {
		return uuid.Nil, false, errors.New("invalid morning inbox report")
	}
	pushEnabled := apnsPushEnabled(ctx, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('morning_report:' || current_schema() || ':' || $1))`, h.LocalDate); err != nil {
		return uuid.Nil, false, err
	}

	logicalKey := MorningReportInboxLogicalKey(h.LocalDate)
	var existingInboxID uuid.UUID
	var existingReportID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id, report_id FROM inbox_items WHERE logical_key = $1 FOR UPDATE`, logicalKey).Scan(&existingInboxID, &existingReportID)
	if err == nil && !allowNewRevision {
		if err = scanReportHistoryRow(tx.QueryRow(ctx, `SELECT id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision FROM report_history WHERE id=$1`, existingReportID), h); err != nil {
			return uuid.Nil, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return uuid.Nil, false, err
		}
		return existingInboxID, false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}

	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM report_history WHERE delivery_key=$1`, *h.DeliveryKey).Scan(&h.Revision); err != nil {
		return uuid.Nil, false, err
	}
	h.ensureFormatRevision()
	h.ID = uuid.New()
	h.PreparedAt = time.Now().UTC()
	if len(h.Context) == 0 {
		h.Context = json.RawMessage(`{}`)
	}
	if len(h.AIMetadata) == 0 {
		h.AIMetadata = json.RawMessage(`{}`)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO report_history(id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		h.ID, h.DeliveryKey, h.Revision, h.Kind, h.LocalDate, h.Timezone, h.Language, h.PreparedAt, h.ScheduledAt, h.IsPreview, h.PlainBody, h.RichBody, h.DecisionID, h.Context, h.AIMetadata, h.FallbackReason, h.FormatRevision,
	); err != nil {
		return uuid.Nil, false, err
	}

	inboxID := existingInboxID
	if inboxID == uuid.Nil {
		inboxID = uuid.New()
		if _, err = tx.Exec(ctx, `
			INSERT INTO inbox_items(id,logical_key,kind,local_date,created_at,report_id,read_at)
			VALUES($1,$2,$3,$4,$5,$6,NULL)`,
			inboxID, logicalKey, InboxKindMorningReport, h.LocalDate, h.PreparedAt, h.ID,
		); err != nil {
			return uuid.Nil, false, err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE inbox_items SET report_id = $2 WHERE id = $1`, inboxID, h.ID); err != nil {
			return uuid.Nil, false, err
		}
	}
	if existingInboxID == uuid.Nil && pushEnabled {
		if err = enqueuePushDeliveriesTx(ctx, tx, inboxID, InboxKindMorningReport, h.PreparedAt.Add(morningReportPushTTL)); err != nil {
			return uuid.Nil, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return inboxID, true, nil
}

// HasPublishedMorningInboxReport reports whether today's morning Inbox card has
// already been committed for localDate.
func (s *DB) HasPublishedMorningInboxReport(localDate string) bool {
	ctx, cancel := queryCtx()
	defer cancel()
	var exists bool
	_ = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items WHERE logical_key = $1)`, MorningReportInboxLogicalKey(localDate)).Scan(&exists)
	return exists
}

// GetPublishedMorningInboxReport returns the currently linked morning ReportHistory
// and Inbox card ID for localDate, or (nil, uuid.Nil, nil) when none exists.
func (s *DB) GetPublishedMorningInboxReport(ctx context.Context, localDate string) (*ReportHistory, uuid.UUID, error) {
	var inboxID, reportID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id, report_id FROM inbox_items WHERE logical_key = $1`, MorningReportInboxLogicalKey(localDate)).Scan(&inboxID, &reportID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, nil
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	var h ReportHistory
	if err = scanReportHistoryRow(s.pool.QueryRow(ctx, `SELECT id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision FROM report_history WHERE id=$1`, reportID), &h); err != nil {
		return nil, uuid.Nil, err
	}
	return &h, inboxID, nil
}

// ReserveReportDeliveryForExisting acquires a durable delivery lease for an
// already-archived report_history row without creating a new revision or Inbox
// card. Retries are allowed on `failed` deliveries and on stale zero-attempt
// reservations (>=30s with zero report_delivery_attempts for reportID).
func (s *DB) ReserveReportDeliveryForExisting(ctx context.Context, reportID uuid.UUID) (uuid.UUID, bool, error) {
	if reportID == uuid.Nil {
		return uuid.Nil, false, errors.New("report id is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)

	var deliveryKey *string
	var preview bool
	if err = tx.QueryRow(ctx, `SELECT delivery_key, is_preview FROM report_history WHERE id = $1`, reportID).Scan(&deliveryKey, &preview); err != nil {
		return uuid.Nil, false, err
	}
	if preview || deliveryKey == nil || *deliveryKey == "" {
		return uuid.Nil, false, errors.New("report has no durable delivery key")
	}

	token := uuid.New()
	var got uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO notification_deliveries(delivery_key,status,lease_token,reserved_at)
		VALUES($1,'reserved',$2,clock_timestamp())
		ON CONFLICT DO NOTHING
		RETURNING lease_token`, *deliveryKey, token).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		var state string
		var reservedAt time.Time
		var existingLease uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT status, reserved_at, lease_token FROM notification_deliveries WHERE delivery_key = $1 FOR UPDATE`, *deliveryKey).Scan(&state, &reservedAt, &existingLease); err != nil {
			return uuid.Nil, false, err
		}
		retryable := state == "failed"
		if state == "reserved" && time.Since(reservedAt) >= 30*time.Second {
			var noAttempt bool
			err = tx.QueryRow(ctx, `
				SELECT NOT EXISTS(
					SELECT 1
					  FROM report_delivery_attempts a
					  JOIN report_history h ON h.id = a.report_id
					 WHERE h.delivery_key = $1
					   AND (a.lease_token = $2 OR (a.lease_token IS NULL AND a.reserved_at >= $3))
				)`, *deliveryKey, existingLease, reservedAt).Scan(&noAttempt)
			if err != nil {
				return uuid.Nil, false, err
			}
			retryable = noAttempt
		}
		if !retryable {
			return uuid.Nil, false, nil
		}
		if _, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='reserved', lease_token=$2, reserved_at=clock_timestamp(), completed_at=NULL, error_code=NULL WHERE delivery_key=$1`, *deliveryKey, token); err != nil {
			return uuid.Nil, false, err
		}
	} else if err != nil {
		return uuid.Nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return token, true, nil
}

// EnsureCanonicalCheckinPrompt atomically creates (or reuses) the single
// canonical subjective_checkins row for date and its corresponding inbox_items
// card. If a legacy/mid-day source='telegram' row already exists for date, it
// remains canonical and no second row is created.
func (s *DB) EnsureCanonicalCheckinPrompt(ctx context.Context, date string, promptedAt, expiresAt time.Time) (uuid.UUID, CheckinRow, bool, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return uuid.Nil, CheckinRow{}, false, fmt.Errorf("date must be YYYY-MM-DD: %w", err)
	}
	pushEnabled := apnsPushEnabled(ctx, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, CheckinRow{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('checkin:' || current_schema() || ':' || $1))`, date); err != nil {
		return uuid.Nil, CheckinRow{}, false, err
	}

	row, found, err := selectCanonicalCheckinForUpdateTx(ctx, tx, date)
	if err != nil {
		return uuid.Nil, CheckinRow{}, false, err
	}
	created := false
	if !found {
		if _, err = tx.Exec(ctx, `
			INSERT INTO subjective_checkins (date, source, status, prompted_at, expires_at)
			VALUES ($1, $2, $3, $4, $5)`,
			date, CheckinSourceInbox, CheckinStatusPrompted, promptedAt, expiresAt,
		); err != nil {
			return uuid.Nil, CheckinRow{}, false, err
		}
		row = CheckinRow{
			Date:       date,
			Source:     CheckinSourceInbox,
			Status:     CheckinStatusPrompted,
			PromptedAt: promptedAt,
			ExpiresAt:  expiresAt,
		}
		created = true
	}

	logicalKey := CheckinInboxLogicalKey(date)
	var promptID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM inbox_items WHERE logical_key = $1 FOR UPDATE`, logicalKey).Scan(&promptID)
	if errors.Is(err, pgx.ErrNoRows) {
		promptID = uuid.New()
		if _, err = tx.Exec(ctx, `
			INSERT INTO inbox_items (id, logical_key, kind, local_date, created_at, checkin_date, checkin_source, read_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)`,
			promptID, logicalKey, InboxKindPromptCheckin, date, row.PromptedAt, row.Date, row.Source,
		); err != nil {
			return uuid.Nil, CheckinRow{}, false, err
		}
	} else if err != nil {
		return uuid.Nil, CheckinRow{}, false, err
	}
	if created && pushEnabled {
		if err = enqueuePushDeliveriesTx(ctx, tx, promptID, InboxKindPromptCheckin, expiresAt); err != nil {
			return uuid.Nil, CheckinRow{}, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, CheckinRow{}, false, err
	}
	return promptID, row, created, nil
}

// AttachCheckinTelegramMessageID stores the Telegram prompt_message_id on the
// canonical subjective_checkins row for date without altering status or answer.
func (s *DB) AttachCheckinTelegramMessageID(ctx context.Context, date string, msgID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('checkin:' || current_schema() || ':' || $1))`, date); err != nil {
		return err
	}
	row, found, err := selectCanonicalCheckinForUpdateTx(ctx, tx, date)
	if err != nil || !found {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE subjective_checkins SET prompt_message_id = $3 WHERE date = $1 AND source = $2`, row.Date, row.Source, msgID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CheckinAnswerResult is the outcome of a unified first-write-wins answer attempt.
type CheckinAnswerResult struct {
	PromptID        uuid.UUID
	Date            string
	Status          string // "saved" | "already_answered" | "expired"
	EffectiveStatus string // "answered" | "late_answered"
	Answer          string
	AnsweredAt      time.Time
	Timing          string // "on_time" | "late"
	AnsweredChannel string
}

// AnswerCheckinByPromptID resolves an inbox prompt UUID and applies the shared
// first-write-wins, cross-day-guarded check-in transaction.
func (s *DB) AnswerCheckinByPromptID(ctx context.Context, promptID uuid.UUID, todayInTZ, channel, answer string, answeredAt time.Time) (*CheckinAnswerResult, error) {
	if err := ValidateCheckinAnswer(answer); err != nil {
		return nil, err
	}
	if err := ValidateCheckinChannel(channel); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var checkinDate, checkinSource string
	err = tx.QueryRow(ctx, `
		SELECT checkin_date, checkin_source
		  FROM inbox_items
		 WHERE id = $1 AND kind = $2`, promptID, InboxKindPromptCheckin).Scan(&checkinDate, &checkinSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCheckinPromptNotFound
	}
	if err != nil {
		return nil, err
	}

	res, err := answerCanonicalCheckinTx(ctx, tx, promptID, checkinDate, checkinSource, todayInTZ, channel, answer, answeredAt)
	if err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// AnswerCheckinByDate resolves the canonical check-in row for date (used by
// Telegram callbacks) and applies the exact same transaction rules as
// AnswerCheckinByPromptID.
func (s *DB) AnswerCheckinByDate(ctx context.Context, date, todayInTZ, channel, answer string, answeredAt time.Time) (*CheckinAnswerResult, error) {
	if err := ValidateCheckinAnswer(answer); err != nil {
		return nil, err
	}
	if err := ValidateCheckinChannel(channel); err != nil {
		return nil, err
	}
	if todayInTZ != "" && date != todayInTZ {
		return &CheckinAnswerResult{Date: date, Status: CheckinAnswerStatusExpired}, ErrCheckinPromptExpired
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('checkin:' || current_schema() || ':' || $1))`, date); err != nil {
		return nil, err
	}
	row, found, err := selectCanonicalCheckinForUpdateTx(ctx, tx, date)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrCheckinPromptNotFound
	}
	var promptID uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id FROM inbox_items WHERE logical_key = $1`, CheckinInboxLogicalKey(date)).Scan(&promptID)

	res, err := answerCanonicalCheckinTx(ctx, tx, promptID, row.Date, row.Source, todayInTZ, channel, answer, answeredAt)
	if err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func answerCanonicalCheckinTx(
	ctx context.Context,
	tx pgx.Tx,
	promptID uuid.UUID,
	date, source, todayInTZ, channel, answer string,
	answeredAt time.Time,
) (*CheckinAnswerResult, error) {
	// Cross-day guard runs BEFORE already_answered checks so stale/previous-day
	// taps always receive 409 expired and never mutate historical rows.
	if todayInTZ != "" && date != todayInTZ {
		return &CheckinAnswerResult{PromptID: promptID, Date: date, Status: CheckinAnswerStatusExpired}, ErrCheckinPromptExpired
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('checkin:' || current_schema() || ':' || $1))`, date); err != nil {
		return nil, err
	}

	var status string
	var existingAnswer *string
	var existingAnsweredAt *time.Time
	var expiresAt time.Time
	var rawMeta []byte
	err := tx.QueryRow(ctx, `
		SELECT status, answer, answered_at, expires_at, metadata
		  FROM subjective_checkins
		 WHERE date = $1 AND source = $2
		 FOR UPDATE`, date, source).Scan(&status, &existingAnswer, &existingAnsweredAt, &expiresAt, &rawMeta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCheckinPromptNotFound
	}
	if err != nil {
		return nil, err
	}

	if status == CheckinStatusAnswered || status == CheckinStatusLateAnswered {
		savedAnswer := ""
		if existingAnswer != nil {
			savedAnswer = *existingAnswer
		}
		var savedAt time.Time
		if existingAnsweredAt != nil {
			savedAt = *existingAnsweredAt
		}
		timing := CheckinTimingOnTime
		if status == CheckinStatusLateAnswered {
			timing = CheckinTimingLate
		}
		return &CheckinAnswerResult{
			PromptID:        promptID,
			Date:            date,
			Status:          CheckinAnswerStatusAlreadyAnswered,
			EffectiveStatus: status,
			Answer:          savedAnswer,
			AnsweredAt:      savedAt,
			Timing:          timing,
			AnsweredChannel: extractAnsweredChannel(rawMeta, source),
		}, nil
	}

	nextStatus, err := nextCheckinStatus(status, "tap", expiresAt, answeredAt)
	if err != nil {
		return nil, err
	}
	var savedAt time.Time
	if err = tx.QueryRow(ctx, `
		UPDATE subjective_checkins
		   SET status = $3,
		       answer = $4,
		       answered_at = $5,
		       metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{answered_channel}', to_jsonb($6::text), true)
		 WHERE date = $1 AND source = $2
		 RETURNING answered_at`,
		date, source, nextStatus, answer, answeredAt, channel,
	).Scan(&savedAt); err != nil {
		return nil, err
	}
	timing := CheckinTimingOnTime
	if nextStatus == CheckinStatusLateAnswered {
		timing = CheckinTimingLate
	}
	return &CheckinAnswerResult{
		PromptID:        promptID,
		Date:            date,
		Status:          CheckinAnswerStatusSaved,
		EffectiveStatus: nextStatus,
		Answer:          answer,
		AnsweredAt:      savedAt,
		Timing:          timing,
		AnsweredChannel: channel,
	}, nil
}

// selectCanonicalCheckinForUpdateTx implements the single canonical-row
// selection rule shared by scheduler, inbox publishing, and Telegram callbacks.
// If a pre-existing telegram row exists on date (mid-day transition), it stays
// canonical; if both somehow existed, an answered row wins, then inbox.
func selectCanonicalCheckinForUpdateTx(ctx context.Context, tx pgx.Tx, date string) (CheckinRow, bool, error) {
	var row CheckinRow
	var answer *string
	var msgID *int64
	var answeredAt *time.Time
	var rawMeta []byte
	err := tx.QueryRow(ctx, `
		SELECT date, source, status, answer, prompt_message_id, prompted_at, answered_at, expires_at, metadata
		  FROM subjective_checkins
		 WHERE date = $1 AND source IN ($2, $3)
		 ORDER BY
		   CASE WHEN status IN ('answered','late_answered') THEN 0 ELSE 1 END,
		   CASE WHEN source = $3 THEN 0 ELSE 1 END
		 LIMIT 1
		 FOR UPDATE`, date, CheckinSourceInbox, CheckinSourceTelegram).
		Scan(&row.Date, &row.Source, &row.Status, &answer, &msgID, &row.PromptedAt, &answeredAt, &row.ExpiresAt, &rawMeta)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckinRow{}, false, nil
	}
	if err != nil {
		return CheckinRow{}, false, err
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
	row.AnsweredChannel = extractAnsweredChannel(rawMeta, row.Source)
	return row, true, nil
}

func extractAnsweredChannel(rawMeta []byte, fallbackSource string) string {
	if len(rawMeta) > 0 {
		var m map[string]any
		if json.Unmarshal(rawMeta, &m) == nil {
			if ch, ok := m["answered_channel"].(string); ok && ch != "" {
				return ch
			}
		}
	}
	if fallbackSource == CheckinSourceTelegram {
		return CheckinChannelTelegram
	}
	return ""
}

func scanReportHistoryRow(row pgx.Row, h *ReportHistory) error {
	return row.Scan(
		&h.ID, &h.DeliveryKey, &h.Revision, &h.Kind, &h.LocalDate, &h.Timezone,
		&h.Language, &h.PreparedAt, &h.ScheduledAt, &h.IsPreview, &h.PlainBody,
		&h.RichBody, &h.DecisionID, &h.Context, &h.AIMetadata, &h.FallbackReason, &h.FormatRevision,
	)
}

// InboxQuery filters the paginated inbox feed.
type InboxQuery struct {
	Limit    int
	BeforeAt *time.Time
	BeforeID uuid.UUID
}

// InboxRow is a joined storage projection for both inbox feed and detail reads.
type InboxRow struct {
	ID              uuid.UUID
	LogicalKey      string
	Kind            string
	LocalDate       string
	CreatedAt       time.Time
	ReadAt          *time.Time
	ReportID        *uuid.UUID
	ReportLanguage  string
	ReportPlainBody string
	ReportPrepared  *time.Time
	CheckinStatus   string
	CheckinAnswer   string
	CheckinChannel  string
	CheckinPrompted *time.Time
	CheckinAnswered *time.Time
	CheckinExpires  *time.Time
}

const inboxSelectSQL = `
SELECT i.id, i.logical_key, i.kind, i.local_date, i.created_at, i.read_at,
       i.report_id, COALESCE(r.language, ''), COALESCE(r.plain_body, ''), r.prepared_at,
       COALESCE(c.status, ''), COALESCE(c.answer, ''), c.source, c.metadata,
       c.prompted_at, c.answered_at, c.expires_at
  FROM inbox_items i
  LEFT JOIN report_history r ON r.id = i.report_id
  LEFT JOIN subjective_checkins c ON c.date = i.checkin_date AND c.source = i.checkin_source`

func scanInboxRow(scanner interface{ Scan(...any) error }) (InboxRow, error) {
	var row InboxRow
	var checkinSource *string
	var rawMeta []byte
	err := scanner.Scan(
		&row.ID, &row.LogicalKey, &row.Kind, &row.LocalDate, &row.CreatedAt, &row.ReadAt,
		&row.ReportID, &row.ReportLanguage, &row.ReportPlainBody, &row.ReportPrepared,
		&row.CheckinStatus, &row.CheckinAnswer, &checkinSource, &rawMeta,
		&row.CheckinPrompted, &row.CheckinAnswered, &row.CheckinExpires,
	)
	if err != nil {
		return InboxRow{}, err
	}
	if row.CheckinStatus == CheckinStatusAnswered || row.CheckinStatus == CheckinStatusLateAnswered {
		src := ""
		if checkinSource != nil {
			src = *checkinSource
		}
		row.CheckinChannel = extractAnsweredChannel(rawMeta, src)
	}
	return row, nil
}

func (s *DB) ListInboxItems(ctx context.Context, q InboxQuery) ([]InboxRow, error) {
	rows, err := s.pool.Query(ctx, inboxSelectSQL+`
		WHERE ($1::timestamptz IS NULL OR (i.created_at, i.id) < ($1, $2))
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $3`, q.BeforeAt, q.BeforeID, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxRow{}
	for rows.Next() {
		item, err := scanInboxRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *DB) GetInboxItem(ctx context.Context, id uuid.UUID) (*InboxRow, error) {
	row, err := scanInboxRow(s.pool.QueryRow(ctx, inboxSelectSQL+` WHERE i.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *DB) ListPendingInboxCheckins(ctx context.Context, todayInTZ string, now time.Time) ([]InboxRow, error) {
	rows, err := s.pool.Query(ctx, inboxSelectSQL+`
		WHERE i.kind = $1
		  AND i.local_date = $2
		  AND c.status = $3
		  AND c.expires_at > $4
		ORDER BY i.created_at DESC, i.id DESC`,
		InboxKindPromptCheckin, todayInTZ, CheckinStatusPrompted, now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxRow{}
	for rows.Next() {
		item, err := scanInboxRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
