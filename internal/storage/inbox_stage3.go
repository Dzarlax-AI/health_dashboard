package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	eveningReportPushTTL  = 6 * time.Hour
	daytimeUpdatePushTTL  = 8 * time.Hour
	digestNudgePushTTL    = 24 * time.Hour
	wakeFeedbackPushTTL   = 12 * time.Hour
)

func EveningReportInboxLogicalKey(localDate string) string {
	return InboxKindEveningReport + ":" + localDate
}

func WakeFeedbackInboxLogicalKey(localDate string) string {
	return InboxKindPromptWakeFeedback + ":" + localDate
}

func ContextPromptInboxLogicalKey(promptID string) string {
	return InboxKindPromptContext + ":" + promptID
}

// HasSettledEveningReport reports whether today's evening report has already been
// settled via Telegram (sent/ambiguous) or published to Inbox.
func (s *DB) HasSettledEveningReport(localDate string) bool {
	ctx, cancel := queryCtx()
	defer cancel()
	var settled bool
	_ = s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM inbox_items WHERE logical_key = $1)
		    OR EXISTS(
		        SELECT 1 FROM notification_deliveries
		         WHERE delivery_key = $2
		           AND status IN ('sent','ambiguous')
		    )`, EveningReportInboxLogicalKey(localDate), "report:evening:"+localDate).Scan(&settled)
	return settled
}

// HasPublishedEveningInboxReport reports whether today's evening Inbox card has
// already been committed for localDate.
func (s *DB) HasPublishedEveningInboxReport(localDate string) bool {
	ctx, cancel := queryCtx()
	defer cancel()
	var exists bool
	_ = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items WHERE logical_key = $1)`, EveningReportInboxLogicalKey(localDate)).Scan(&exists)
	return exists
}

// GetPublishedEveningInboxReport loads the published evening ReportHistory and
// Inbox card ID for localDate, or (nil, uuid.Nil, nil) if none exists.
func (s *DB) GetPublishedEveningInboxReport(ctx context.Context, localDate string) (*ReportHistory, uuid.UUID, error) {
	var inboxID, reportID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id, report_id FROM inbox_items WHERE logical_key = $1`, EveningReportInboxLogicalKey(localDate)).Scan(&inboxID, &reportID)
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

// ListRetryableInboxTelegramReports returns today's already-published Inbox
// reports whose Telegram delivery has not yet been reserved (e.g. crash between
// Inbox commit and first reservation), previously failed with a retryable error,
// or stalled in `reserved` for >=30s with zero transport attempts. It bypasses
// publication cadence, snapshot, and proactive episode/budget gates while
// strictly excluding sent, ambiguous, and telegram_disabled deliveries.
func (s *DB) ListRetryableInboxTelegramReports(ctx context.Context, localDate string, kinds []string, maxProviderRejections, limit int) ([]ReportHistory, error) {
	if s == nil || localDate == "" {
		return nil, nil
	}
	if len(kinds) == 0 {
		kinds = []string{
			InboxKindEveningReport,
			InboxKindSleepUpdate,
			InboxKindGuidanceChange,
			InboxKindWeeklyDigest,
			InboxKindNudge,
		}
	}
	if maxProviderRejections <= 0 {
		maxProviderRejections = MaxReportProviderRejectionCycles
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.delivery_key, h.revision, h.kind, h.local_date, h.timezone, h.language,
		       h.prepared_at, h.scheduled_at, h.is_preview, h.plain_body, h.rich_body,
		       h.decision_id, h.context, h.ai_metadata, h.fallback_reason, h.format_revision
		  FROM inbox_items i
		  JOIN report_history h ON h.id = i.report_id
		  LEFT JOIN notification_deliveries d ON d.delivery_key = h.delivery_key
		 WHERE i.local_date = $1
		   AND i.kind = ANY($2)
		   AND i.report_id IS NOT NULL
		   AND h.is_preview = FALSE
		   AND h.delivery_key IS NOT NULL
		   AND (
		       d.delivery_key IS NULL
		       OR (
		           d.status = 'failed'
		           AND COALESCE(d.error_code, '') <> 'telegram_disabled'
		           AND (
		               COALESCE(d.error_code, '') <> 'provider_rejected'
		               OR (
		                   SELECT COUNT(DISTINCT COALESCE(a.lease_token, a.id))
		                     FROM report_delivery_attempts a
		                     JOIN report_history rh ON rh.id = a.report_id
		                    WHERE rh.delivery_key = h.delivery_key
		                      AND a.status = 'failed'
		                      AND a.error_code = 'provider_rejected'
		               ) < $3
		           )
		       )
		       OR (
		           d.status = 'reserved'
		           AND d.reserved_at <= clock_timestamp() - INTERVAL '30 seconds'
		           AND NOT EXISTS (
		               SELECT 1
		                 FROM report_delivery_attempts a
		                 JOIN report_history rh ON rh.id = a.report_id
		                WHERE rh.delivery_key = h.delivery_key
		                  AND (a.lease_token = d.lease_token OR (a.lease_token IS NULL AND a.reserved_at >= d.reserved_at))
		           )
		       )
		   )
		 ORDER BY i.created_at ASC, i.id ASC
		 LIMIT $4`, localDate, kinds, maxProviderRejections, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportHistory
	for rows.Next() {
		var h ReportHistory
		if err := scanReportHistoryRow(rows, &h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PublishEveningReportToInbox atomically archives a durable evening report in
// report_history, inserts its inbox_items card, enqueues APNs push deliveries
// on first creation only, and atomically records whether Telegram delivery is
// eligible for this publication. When telegramDeliveryAllowed is false (e.g.
// Telegram bot is unconfigured) or telegram_notifications_enabled is false, a
// terminal failed/telegram_disabled marker is committed in the same transaction
// so later enabling/configuring Telegram cannot retroactively send old cards.
func (s *DB) PublishEveningReportToInbox(ctx context.Context, h *ReportHistory, telegramDeliveryAllowed bool) (uuid.UUID, bool, error) {
	if h == nil || h.IsPreview || h.Kind != "evening" || h.LocalDate == "" || h.DeliveryKey == nil || *h.DeliveryKey == "" {
		return uuid.Nil, false, errors.New("invalid evening inbox report")
	}
	pushEnabled := apnsPushEnabled(ctx, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('evening_report:' || current_schema() || ':' || $1))`, h.LocalDate); err != nil {
		return uuid.Nil, false, err
	}

	logicalKey := EveningReportInboxLogicalKey(h.LocalDate)
	var existingInboxID, existingReportID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id, report_id FROM inbox_items WHERE logical_key = $1 FOR UPDATE`, logicalKey).Scan(&existingInboxID, &existingReportID)
	if err == nil {
		if err = scanReportHistoryRow(tx.QueryRow(ctx, `SELECT id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision FROM report_history WHERE id=$1`, existingReportID), h); err != nil {
			return uuid.Nil, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return uuid.Nil, false, err
		}
		return existingInboxID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}

	// Anti-retroactivity guard: if Telegram already settled today's evening
	// report before Inbox/APNs was enabled, do not create a duplicate card or push.
	var alreadySettled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE delivery_key = $1 AND status IN ('sent','ambiguous'))`, *h.DeliveryKey).Scan(&alreadySettled); err != nil {
		return uuid.Nil, false, err
	}
	if alreadySettled {
		return uuid.Nil, false, tx.Commit(ctx)
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

	inboxID := uuid.New()
	if _, err = tx.Exec(ctx, `
		INSERT INTO inbox_items(id,logical_key,kind,local_date,created_at,report_id,read_at)
		VALUES($1,$2,$3,$4,clock_timestamp(),$5,NULL)`,
		inboxID, logicalKey, InboxKindEveningReport, h.LocalDate, h.ID,
	); err != nil {
		return uuid.Nil, false, err
	}
	if pushEnabled {
		if err = enqueuePushDeliveriesTx(ctx, tx, inboxID, InboxKindEveningReport, h.PreparedAt.Add(eveningReportPushTTL)); err != nil {
			return uuid.Nil, false, err
		}
	}
	telegramEnabled, err := telegramNotificationsEnabledTx(ctx, tx)
	if err != nil {
		return uuid.Nil, false, err
	}
	if !telegramDeliveryAllowed || !telegramEnabled {
		if _, err = tx.Exec(ctx, `
			INSERT INTO notification_deliveries(delivery_key, status, lease_token, reserved_at, completed_at, error_code)
			VALUES($1, 'failed', $2, clock_timestamp(), clock_timestamp(), 'telegram_disabled')
			ON CONFLICT DO NOTHING`, *h.DeliveryKey, uuid.New()); err != nil {
			return uuid.Nil, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return inboxID, true, nil
}

// PublishProactiveToInbox decouples proactive publication (report_history,
// proactive_events, inbox_items, APNs push queue, and the unified daily budget /
// 1h cooldown) from Telegram's notification_deliveries table.
// Each publication is counted ONCE toward the proactive daily budget (<=2/day,
// 1h cooldown) across both Inbox publications and legacy Telegram-only deliveries.
func (s *DB) PublishProactiveToInbox(
	ctx context.Context,
	h *ReportHistory,
	logicalKey, deliveryKey string,
	event *ProactiveEventCandidate,
	now time.Time,
	loc *time.Location,
	inboxKind string,
	pushTTL time.Duration,
	telegramDeliveryAllowed bool,
) (uuid.UUID, bool, error) {
	if h == nil || h.Kind != "proactive" || deliveryKey == "" || loc == nil || h.IsPreview || !IsReportInboxKind(inboxKind) {
		return uuid.Nil, false, errors.New("invalid proactive inbox publication")
	}
	if pushTTL <= 0 {
		pushTTL = daytimeUpdatePushTTL
	}
	pushEnabled := apnsPushEnabled(ctx, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema() || ':proactive-budget',0))`); err != nil {
		return uuid.Nil, false, err
	}

	var episode uuid.UUID
	revision := 1
	if event != nil {
		if event.Rule == "" || event.FactualIdentity == "" || event.CurrentConclusion == "" {
			return uuid.Nil, false, errors.New("invalid proactive event")
		}
		var priorFact, priorConclusion string
		err = tx.QueryRow(ctx, `SELECT id,revision,factual_identity,current_conclusion FROM proactive_events WHERE rule=$1 AND state='active' FOR UPDATE`, event.Rule).Scan(&episode, &revision, &priorFact, &priorConclusion)
		if err == nil {
			if event.ExpectedEpisodeID != episode || event.ExpectedRevision != revision {
				return uuid.Nil, false, nil
			}
			if priorFact != event.FactualIdentity && priorConclusion != event.CurrentConclusion {
				if event.PreviousConclusion != priorConclusion {
					return uuid.Nil, false, nil
				}
				revision++
			} else if priorFact != event.FactualIdentity || priorConclusion != event.CurrentConclusion {
				return uuid.Nil, false, nil
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			if event.ExpectedEpisodeID != uuid.Nil || event.ExpectedRevision != 0 {
				return uuid.Nil, false, nil
			}
			episode = uuid.New()
			revision = 1
		} else {
			return uuid.Nil, false, err
		}
		deliveryKey = fmt.Sprintf("proactive:daytime:%s:%d", episode, revision)
		logicalKey = fmt.Sprintf("%s:%s:%d", inboxKind, episode, revision)
	}
	if logicalKey == "" {
		return uuid.Nil, false, errors.New("logicalKey is required")
	}

	// Idempotent deduplication by logical_key: return existing archived report
	// with created=false so Telegram may still retry if eligible without
	// duplicating the Inbox card or re-enqueueing APNs.
	var existingInboxID, existingReportID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id, report_id FROM inbox_items WHERE logical_key = $1 FOR UPDATE`, logicalKey).Scan(&existingInboxID, &existingReportID)
	if err == nil {
		if err = scanReportHistoryRow(tx.QueryRow(ctx, `SELECT id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision FROM report_history WHERE id=$1`, existingReportID), h); err != nil {
			return uuid.Nil, false, err
		}
		return existingInboxID, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}

	// Anti-retroactivity guard: if this exact proactive delivery_key was already
	// settled in Telegram before Inbox was enabled, do not re-publish.
	var alreadySettled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE delivery_key = $1 AND status IN ('sent','ambiguous'))`, deliveryKey).Scan(&alreadySettled); err != nil {
		return uuid.Nil, false, err
	}
	if alreadySettled {
		return uuid.Nil, false, tx.Commit(ctx)
	}

	// Unified proactive daily budget (<=2/day) and 1h cooldown, counting each
	// logical proactive publication ONCE across inbox_items and legacy
	// Telegram-only notification_deliveries (deduplicated by delivery_key).
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	var count int
	var latest *time.Time
	err = tx.QueryRow(ctx, `
		WITH unified AS (
			SELECT COALESCE(r.delivery_key, i.logical_key) AS dkey, i.created_at AS published_at
			  FROM inbox_items i
			  LEFT JOIN report_history r ON r.id = i.report_id
			 WHERE i.kind IN ('sleep_update','guidance_change','weekly_digest','nudge')
			UNION ALL
			SELECT nd.delivery_key AS dkey, nd.reserved_at AS published_at
			  FROM notification_deliveries nd
			 WHERE nd.delivery_key LIKE 'proactive:%'
			   AND nd.status IN ('reserved','sent','ambiguous')
		),
		dedup AS (
			SELECT dkey, MIN(published_at) AS published_at
			  FROM unified
			 WHERE dkey <> $3
			 GROUP BY dkey
		)
		SELECT count(*) FILTER (WHERE published_at >= $1 AND published_at < $2),
		       max(published_at)
		  FROM dedup`, start, end, deliveryKey).Scan(&count, &latest)
	if err != nil {
		return uuid.Nil, false, err
	}
	if count >= 2 || (latest != nil && now.Sub(*latest) < time.Hour) {
		return uuid.Nil, false, nil
	}

	h.ensureFormatRevision()
	h.DeliveryKey = &deliveryKey
	h.ID = uuid.New()
	h.PreparedAt = now.UTC()
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM report_history WHERE delivery_key=$1`, deliveryKey).Scan(&h.Revision); err != nil {
		return uuid.Nil, false, err
	}
	if len(h.Context) == 0 {
		h.Context = json.RawMessage(`{}`)
	}
	if event != nil {
		var fields map[string]any
		if err = json.Unmarshal(h.Context, &fields); err != nil || fields == nil {
			return uuid.Nil, false, errors.New("invalid proactive context")
		}
		category := "guidance_change"
		if event.Rule == "sleep_analysis_update" {
			category = "sleep_analysis_update"
		}
		fields["proactive"] = map[string]any{
			"rule":                event.Rule,
			"episode_id":          episode.String(),
			"revision":            revision,
			"category":            category,
			"factual_identity":    event.FactualIdentity,
			"source_report_id":    event.SourceReportID.String(),
			"previous_conclusion": event.PreviousConclusion,
			"current_conclusion":  event.CurrentConclusion,
		}
		h.Context, err = json.Marshal(fields)
		if err != nil {
			return uuid.Nil, false, err
		}
	}
	if len(h.AIMetadata) == 0 {
		h.AIMetadata = json.RawMessage(`{}`)
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO report_history(id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,false,$10,$11,$12,$13,$14,$15,$16)`,
		h.ID, deliveryKey, h.Revision, h.Kind, h.LocalDate, h.Timezone, h.Language, h.PreparedAt, h.ScheduledAt,
		h.PlainBody, h.RichBody, h.DecisionID, h.Context, h.AIMetadata, h.FallbackReason, h.FormatRevision,
	); err != nil {
		return uuid.Nil, false, err
	}
	if event != nil {
		if _, err = tx.Exec(ctx, `
			INSERT INTO proactive_events(id,rule,state,revision,factual_identity,previous_conclusion,current_conclusion,source_report_id,report_id,opened_at,updated_at)
			VALUES($1,$2,'active',$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT(id) DO UPDATE SET
				revision=excluded.revision,
				factual_identity=excluded.factual_identity,
				previous_conclusion=excluded.previous_conclusion,
				current_conclusion=excluded.current_conclusion,
				source_report_id=excluded.source_report_id,
				report_id=excluded.report_id,
				updated_at=excluded.updated_at`,
			episode, event.Rule, revision, event.FactualIdentity, event.PreviousConclusion, event.CurrentConclusion, event.SourceReportID, h.ID, now,
		); err != nil {
			return uuid.Nil, false, err
		}
	}

	inboxID := uuid.New()
	if _, err = tx.Exec(ctx, `
		INSERT INTO inbox_items(id,logical_key,kind,local_date,created_at,report_id,read_at)
		VALUES($1,$2,$3,$4,clock_timestamp(),$5,NULL)`,
		inboxID, logicalKey, inboxKind, h.LocalDate, h.ID,
	); err != nil {
		return uuid.Nil, false, err
	}
	if pushEnabled {
		if err = enqueuePushDeliveriesTx(ctx, tx, inboxID, inboxKind, now.UTC().Add(pushTTL)); err != nil {
			return uuid.Nil, false, err
		}
	}
	telegramEnabled, err := telegramNotificationsEnabledTx(ctx, tx)
	if err != nil {
		return uuid.Nil, false, err
	}
	if !telegramDeliveryAllowed || !telegramEnabled {
		if _, err = tx.Exec(ctx, `
			INSERT INTO notification_deliveries(delivery_key, status, lease_token, reserved_at, completed_at, error_code)
			VALUES($1, 'failed', $2, clock_timestamp(), clock_timestamp(), 'telegram_disabled')
			ON CONFLICT DO NOTHING`, deliveryKey, uuid.New()); err != nil {
			return uuid.Nil, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return inboxID, true, nil
}

// --- Canonical Wake Feedback Prompt & Unified First-Write-Wins Answer ---

type WakeFeedbackAnswerResult struct {
	PromptID        uuid.UUID
	Date            string
	Status          string // "saved" | "already_answered" | "expired"
	Response        string
	AnsweredAt      time.Time
	AnsweredChannel string
}

func selectCanonicalWakeFeedbackForUpdateTx(ctx context.Context, tx pgx.Tx, date string) (DerivedMetricFeedback, bool, error) {
	var fb DerivedMetricFeedback
	var proposed, corrected, metadata []byte
	var response *string
	err := tx.QueryRow(ctx, `
		SELECT metric_name, TO_CHAR(metric_date, 'YYYY-MM-DD'), channel,
		       proposed_value, response, corrected_value, prompt_message_id,
		       prompted_at, answered_at, metadata
		  FROM derived_metric_feedback
		 WHERE metric_name = $1
		   AND metric_date = $2::date
		   AND channel IN ($3, $4)
		 ORDER BY
		   CASE WHEN response IS NOT NULL THEN 0 ELSE 1 END,
		   CASE WHEN channel = $4 THEN 0 ELSE 1 END
		 LIMIT 1
		 FOR UPDATE`,
		DerivedMetricWakeTime, date, DerivedMetricFeedbackInbox, DerivedMetricFeedbackTelegram,
	).Scan(
		&fb.MetricName, &fb.MetricDate, &fb.Channel,
		&proposed, &response, &corrected, &fb.PromptMessageID,
		&fb.PromptedAt, &fb.AnsweredAt, &metadata,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DerivedMetricFeedback{}, false, nil
	}
	if err != nil {
		return DerivedMetricFeedback{}, false, err
	}
	fb.ProposedValue = json.RawMessage(proposed)
	fb.CorrectedValue = json.RawMessage(corrected)
	fb.Metadata = json.RawMessage(metadata)
	if response != nil {
		fb.Response = *response
	}
	return fb, true, nil
}

// EnsureCanonicalWakeFeedbackPrompt atomically creates (or reuses) the single
// canonical wake_time feedback row for date, its inbox_items card, and (on first
// creation only) APNs push deliveries.
func (s *DB) EnsureCanonicalWakeFeedbackPrompt(
	ctx context.Context,
	date string,
	proposedValue json.RawMessage,
	confidence string,
	promptedAt time.Time,
) (uuid.UUID, DerivedMetricFeedback, bool, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return uuid.Nil, DerivedMetricFeedback{}, false, fmt.Errorf("date must be YYYY-MM-DD: %w", err)
	}
	if len(proposedValue) == 0 || !json.Valid(proposedValue) {
		return uuid.Nil, DerivedMetricFeedback{}, false, errors.New("proposedValue must be valid JSON")
	}
	if promptedAt.IsZero() {
		promptedAt = time.Now().UTC()
	}
	pushEnabled := apnsPushEnabled(ctx, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, DerivedMetricFeedback{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('wake_feedback:' || current_schema() || ':' || $1))`, date); err != nil {
		return uuid.Nil, DerivedMetricFeedback{}, false, err
	}

	fb, found, err := selectCanonicalWakeFeedbackForUpdateTx(ctx, tx, date)
	if err != nil {
		return uuid.Nil, DerivedMetricFeedback{}, false, err
	}
	created := false
	if !found {
		meta := json.RawMessage(fmt.Sprintf(`{"confidence":%q}`, confidence))
		if _, err = tx.Exec(ctx, `
			INSERT INTO derived_metric_feedback (
				metric_name, metric_date, channel, proposed_value, prompted_at, metadata
			) VALUES ($1, $2::date, $3, $4, $5, $6)`,
			DerivedMetricWakeTime, date, DerivedMetricFeedbackInbox, json.RawMessage(proposedValue), promptedAt, meta,
		); err != nil {
			return uuid.Nil, DerivedMetricFeedback{}, false, err
		}
		fb = DerivedMetricFeedback{
			MetricName:    DerivedMetricWakeTime,
			MetricDate:    date,
			Channel:       DerivedMetricFeedbackInbox,
			ProposedValue: proposedValue,
			PromptedAt:    promptedAt,
			Metadata:      meta,
		}
		created = true
	}

	logicalKey := WakeFeedbackInboxLogicalKey(date)
	var inboxID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM inbox_items WHERE logical_key = $1 FOR UPDATE`, logicalKey).Scan(&inboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		inboxID = uuid.New()
		if _, err = tx.Exec(ctx, `
			INSERT INTO inbox_items (
				id, logical_key, kind, local_date, created_at,
				feedback_metric, feedback_date, feedback_channel, read_at
			) VALUES ($1, $2, $3, $4, clock_timestamp(), $5, $6::date, $7, NULL)`,
			inboxID, logicalKey, InboxKindPromptWakeFeedback, date, fb.MetricName, fb.MetricDate, fb.Channel,
		); err != nil {
			return uuid.Nil, DerivedMetricFeedback{}, false, err
		}
	} else if err != nil {
		return uuid.Nil, DerivedMetricFeedback{}, false, err
	}

	if created && pushEnabled {
		if err = enqueuePushDeliveriesTx(ctx, tx, inboxID, InboxKindPromptWakeFeedback, promptedAt.UTC().Add(wakeFeedbackPushTTL)); err != nil {
			return uuid.Nil, DerivedMetricFeedback{}, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, DerivedMetricFeedback{}, false, err
	}
	return inboxID, fb, created, nil
}

// AttachWakeFeedbackTelegramMessageID records the Telegram prompt_message_id on
// the canonical wake_time feedback row without creating a duplicate row.
func (s *DB) AttachWakeFeedbackTelegramMessageID(ctx context.Context, date string, msgID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('wake_feedback:' || current_schema() || ':' || $1))`, date); err != nil {
		return err
	}
	fb, found, err := selectCanonicalWakeFeedbackForUpdateTx(ctx, tx, date)
	if err != nil || !found {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE derived_metric_feedback
		   SET prompt_message_id = $4
		 WHERE metric_name = $1 AND metric_date = $2::date AND channel = $3`,
		fb.MetricName, fb.MetricDate, fb.Channel, msgID,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *DB) AnswerWakeFeedbackByPromptID(ctx context.Context, promptID uuid.UUID, todayInTZ, channel, response string, answeredAt time.Time) (*WakeFeedbackAnswerResult, error) {
	if err := ValidateWakeFeedbackResponse(response); err != nil {
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

	var localDate, fbChannel string
	err = tx.QueryRow(ctx, `
		SELECT local_date, feedback_channel
		  FROM inbox_items
		 WHERE id = $1 AND kind = $2`, promptID, InboxKindPromptWakeFeedback).Scan(&localDate, &fbChannel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCheckinPromptNotFound
	}
	if err != nil {
		return nil, err
	}

	res, err := answerCanonicalWakeFeedbackTx(ctx, tx, promptID, localDate, fbChannel, todayInTZ, channel, response, answeredAt)
	if err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// AnswerWakeFeedbackByDate resolves the canonical wake_time feedback row for
// date (used by Telegram callbacks) and runs the exact same transaction as API answers.
func (s *DB) AnswerWakeFeedbackByDate(ctx context.Context, date, todayInTZ, channel, response string, answeredAt time.Time) (*WakeFeedbackAnswerResult, error) {
	if err := ValidateWakeFeedbackResponse(response); err != nil {
		return nil, err
	}
	if err := ValidateCheckinChannel(channel); err != nil {
		return nil, err
	}
	if todayInTZ != "" && date != todayInTZ {
		return &WakeFeedbackAnswerResult{Date: date, Status: CheckinAnswerStatusExpired}, ErrWakeFeedbackExpired
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('wake_feedback:' || current_schema() || ':' || $1))`, date); err != nil {
		return nil, err
	}
	fb, found, err := selectCanonicalWakeFeedbackForUpdateTx(ctx, tx, date)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrCheckinPromptNotFound
	}
	var promptID uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id FROM inbox_items WHERE logical_key = $1`, WakeFeedbackInboxLogicalKey(date)).Scan(&promptID)

	res, err := answerCanonicalWakeFeedbackTx(ctx, tx, promptID, fb.MetricDate, fb.Channel, todayInTZ, channel, response, answeredAt)
	if err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func answerCanonicalWakeFeedbackTx(
	ctx context.Context,
	tx pgx.Tx,
	promptID uuid.UUID,
	date, fbChannel, todayInTZ, answerChannel, response string,
	answeredAt time.Time,
) (*WakeFeedbackAnswerResult, error) {
	if todayInTZ != "" && date != todayInTZ {
		return &WakeFeedbackAnswerResult{PromptID: promptID, Date: date, Status: CheckinAnswerStatusExpired}, ErrWakeFeedbackExpired
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('wake_feedback:' || current_schema() || ':' || $1))`, date); err != nil {
		return nil, err
	}
	if answeredAt.IsZero() {
		answeredAt = time.Now().UTC()
	}

	var existingResp *string
	var existingAnsweredAt *time.Time
	var rawMeta []byte
	err := tx.QueryRow(ctx, `
		SELECT response, answered_at, metadata
		  FROM derived_metric_feedback
		 WHERE metric_name = $1 AND metric_date = $2::date AND channel = $3
		 FOR UPDATE`, DerivedMetricWakeTime, date, fbChannel).Scan(&existingResp, &existingAnsweredAt, &rawMeta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCheckinPromptNotFound
	}
	if err != nil {
		return nil, err
	}

	if existingResp != nil && *existingResp != "" {
		if promptID != uuid.Nil {
			_, _ = tx.Exec(ctx, `UPDATE inbox_items SET read_at = COALESCE(read_at, clock_timestamp()) WHERE id = $1`, promptID)
		} else {
			_, _ = tx.Exec(ctx, `UPDATE inbox_items SET read_at = COALESCE(read_at, clock_timestamp()) WHERE logical_key = $1`, WakeFeedbackInboxLogicalKey(date))
		}
		var savedAt time.Time
		if existingAnsweredAt != nil {
			savedAt = *existingAnsweredAt
		}
		return &WakeFeedbackAnswerResult{
			PromptID:        promptID,
			Date:            date,
			Status:          CheckinAnswerStatusAlreadyAnswered,
			Response:        *existingResp,
			AnsweredAt:      savedAt,
			AnsweredChannel: extractAnsweredChannel(rawMeta, fbChannel),
		}, nil
	}

	var savedAt time.Time
	if err = tx.QueryRow(ctx, `
		UPDATE derived_metric_feedback
		   SET response = $4,
		       answered_at = $5,
		       metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{answered_channel}', to_jsonb($6::text), true)
		 WHERE metric_name = $1 AND metric_date = $2::date AND channel = $3
		 RETURNING answered_at`,
		DerivedMetricWakeTime, date, fbChannel, response, answeredAt, answerChannel,
	).Scan(&savedAt); err != nil {
		return nil, err
	}

	if promptID != uuid.Nil {
		if _, err = tx.Exec(ctx, `UPDATE inbox_items SET read_at = COALESCE(read_at, $2) WHERE id = $1`, promptID, savedAt); err != nil {
			return nil, err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE inbox_items SET read_at = COALESCE(read_at, $2) WHERE logical_key = $1`, WakeFeedbackInboxLogicalKey(date), savedAt); err != nil {
			return nil, err
		}
	}

	return &WakeFeedbackAnswerResult{
		PromptID:        promptID,
		Date:            date,
		Status:          CheckinAnswerStatusSaved,
		Response:        response,
		AnsweredAt:      savedAt,
		AnsweredChannel: answerChannel,
	}, nil
}

// --- Canonical Context Prompt Publication & Unified First-Write-Wins Answer ---

type ContextPromptAnswerResult struct {
	InboxID         uuid.UUID
	PromptID        string
	Status          string // "saved" | "already_answered" | "expired"
	EffectiveStatus string // "answered" | "skipped" | "expired"
	Category        string
	AnsweredAt      time.Time
	AnsweredChannel string
}

// PublishLowSleepContextPromptToInbox detects eligibility and atomically commits
// context_prompt_interactions (status='prompted', source='inbox'), inbox_items
// (kind='prompt_context'), and APNs push deliveries on first creation only.
func (s *DB) PublishLowSleepContextPromptToInbox(
	ctx context.Context,
	signalDate, promptLocalDate string,
	now, expiresAt time.Time,
) (uuid.UUID, *ContextPromptInteraction, bool, error) {
	detection, err := s.DetectLowSleepContextPrompt(signalDate)
	if err != nil || !detection.Eligible {
		return uuid.Nil, nil, false, err
	}
	gate, err := s.EvaluateLowSleepPromptUsefulness(signalDate, detection)
	if err != nil || !gate.Eligible {
		return uuid.Nil, nil, false, err
	}
	if _, err := time.Parse("2006-01-02", promptLocalDate); err != nil {
		return uuid.Nil, nil, false, fmt.Errorf("promptLocalDate must be YYYY-MM-DD: %w", err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if expiresAt.IsZero() || !expiresAt.After(now) {
		expiresAt = now.Add(36 * time.Hour)
	}
	if err := s.ensureContextPromptCategoriesAllowed(DefaultContextPromptCategories); err != nil {
		return uuid.Nil, nil, false, err
	}

	promptID, err := newContextPromptID()
	if err != nil {
		return uuid.Nil, nil, false, err
	}
	allowedJSON, err := json.Marshal(DefaultContextPromptCategories)
	if err != nil {
		return uuid.Nil, nil, false, err
	}
	metadata := map[string]any{
		"sleep_hours":               round1(detection.SleepHours),
		"baseline_avg":              round1(detection.BaselineAvg),
		"z_score":                   round2(detection.ZScore),
		"baseline_days":             detection.BaselineDays,
		"prompt_gate_reason":        gate.Reason,
		"sleep_structure_disrupted": gate.SleepStructureDisrupted,
		"timing_deviation":          gate.TimingDeviation,
		"repeated_short_nights":     gate.RepeatedShortNights,
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return uuid.Nil, nil, false, err
	}

	pushEnabled := apnsPushEnabled(ctx, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, nil, false, err
	}
	defer tx.Rollback(ctx)

	row := &ContextPromptInteraction{}
	var allowedRaw, metadataRaw []byte
	err = tx.QueryRow(ctx, `
		INSERT INTO context_prompt_interactions
			(prompt_id, signal_date, prompt_local_date, detected_reason, detector_version,
			 status, source, prompted_at, expires_at, allowed_categories, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb, $8, $8)
		ON CONFLICT DO NOTHING
		RETURNING prompt_id, signal_date::text, prompt_local_date::text, detected_reason, detector_version, status,
		          COALESCE(category, ''), COALESCE(source, ''), COALESCE(prompt_message_id, 0),
		          COALESCE(prompted_at, '0001-01-01'::timestamptz), expires_at,
		          COALESCE(answered_at, '0001-01-01'::timestamptz), allowed_categories, metadata`,
		promptID, signalDate, promptLocalDate,
		ContextPromptDetectedReasonLowSleep, ContextPromptDetectorVersionLowSleepV2,
		ContextPromptStatusPrompted, ContextPromptSourceInbox, now, expiresAt,
		json.RawMessage(allowedJSON), json.RawMessage(metadataJSON),
	).Scan(
		&row.PromptID, &row.SignalDate, &row.PromptLocalDate, &row.DetectedReason, &row.DetectorVersion, &row.Status,
		&row.Category, &row.Source, &row.PromptMessageID, &row.PromptedAt, &row.ExpiresAt, &row.AnsweredAt,
		&allowedRaw, &metadataRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, false, tx.Commit(ctx)
	}
	if err != nil {
		return uuid.Nil, nil, false, err
	}
	_ = json.Unmarshal(allowedRaw, &row.AllowedCategories)
	_ = json.Unmarshal(metadataRaw, &row.Metadata)

	inboxID := uuid.New()
	if _, err = tx.Exec(ctx, `
		INSERT INTO inbox_items (
			id, logical_key, kind, local_date, created_at, context_prompt_id, read_at
		) VALUES ($1, $2, $3, $4, clock_timestamp(), $5, NULL)`,
		inboxID, ContextPromptInboxLogicalKey(row.PromptID), InboxKindPromptContext, promptLocalDate, row.PromptID,
	); err != nil {
		return uuid.Nil, nil, false, err
	}
	if pushEnabled {
		if err = enqueuePushDeliveriesTx(ctx, tx, inboxID, InboxKindPromptContext, expiresAt); err != nil {
			return uuid.Nil, nil, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, nil, false, err
	}
	return inboxID, row, true, nil
}

func (s *DB) ensureContextPromptCategoriesAllowed(categories []string) error {
	for _, c := range categories {
		if err := ValidateContextPromptCategory(c); err != nil {
			return err
		}
	}
	return nil
}

// AttachContextPromptTelegramMessageID attaches a Telegram prompt_message_id to
// an already-prompted Inbox context prompt without mutating status or answer.
func (s *DB) AttachContextPromptTelegramMessageID(ctx context.Context, promptID string, msgID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE context_prompt_interactions
		   SET prompt_message_id = $2, updated_at = clock_timestamp()
		 WHERE prompt_id = $1`, promptID, msgID)
	return err
}

func (s *DB) AnswerContextPromptByInboxID(ctx context.Context, inboxID uuid.UUID, channel, category string, answeredAt time.Time) (*ContextPromptAnswerResult, error) {
	if err := ValidateContextPromptCategory(category); err != nil {
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

	var contextPromptID string
	err = tx.QueryRow(ctx, `
		SELECT context_prompt_id
		  FROM inbox_items
		 WHERE id = $1 AND kind = $2`, inboxID, InboxKindPromptContext).Scan(&contextPromptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCheckinPromptNotFound
	}
	if err != nil {
		return nil, err
	}

	res, err := answerContextPromptTx(ctx, tx, inboxID, contextPromptID, channel, category, answeredAt)
	if err != nil {
		if errors.Is(err, ErrContextPromptExpired) {
			_ = tx.Commit(ctx)
		}
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *DB) AnswerContextPromptByPromptID(ctx context.Context, promptID, channel, category string, answeredAt time.Time) (*ContextPromptAnswerResult, error) {
	if err := ValidateContextPromptCategory(category); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var inboxID uuid.UUID
	var hasInbox bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.inbox_items') IS NOT NULL`).Scan(&hasInbox); err != nil {
		return nil, err
	}
	if hasInbox {
		_ = tx.QueryRow(ctx, `SELECT id FROM inbox_items WHERE context_prompt_id = $1`, promptID).Scan(&inboxID)
	}

	res, err := answerContextPromptTx(ctx, tx, inboxID, promptID, channel, category, answeredAt)
	if err != nil {
		if errors.Is(err, ErrContextPromptExpired) {
			_ = tx.Commit(ctx)
		}
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func answerContextPromptTx(
	ctx context.Context,
	tx pgx.Tx,
	inboxID uuid.UUID,
	promptID, channel, category string,
	answeredAt time.Time,
) (*ContextPromptAnswerResult, error) {
	if answeredAt.IsZero() {
		answeredAt = time.Now().UTC()
	}
	var status string
	var existingCat *string
	var existingSource *string
	var existingAnsweredAt *time.Time
	var expiresAt time.Time
	var allowedRaw, metaRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT status, category, source, answered_at, expires_at, allowed_categories, metadata
		  FROM context_prompt_interactions
		 WHERE prompt_id = $1
		 FOR UPDATE`, promptID).Scan(&status, &existingCat, &existingSource, &existingAnsweredAt, &expiresAt, &allowedRaw, &metaRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCheckinPromptNotFound
	}
	if err != nil {
		return nil, err
	}

	markInboxRead := func(at time.Time) error {
		if inboxID == uuid.Nil {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE inbox_items SET read_at = COALESCE(read_at, $2) WHERE id = $1`, inboxID, at)
		return err
	}

	if status == ContextPromptStatusAnswered || status == ContextPromptStatusSkipped {
		_ = markInboxRead(answeredAt)
		savedCat := ""
		if existingCat != nil {
			savedCat = *existingCat
		}
		var savedAt time.Time
		if existingAnsweredAt != nil {
			savedAt = *existingAnsweredAt
		}
		src := ""
		if existingSource != nil {
			src = *existingSource
		}
		return &ContextPromptAnswerResult{
			InboxID:         inboxID,
			PromptID:        promptID,
			Status:          CheckinAnswerStatusAlreadyAnswered,
			EffectiveStatus: status,
			Category:        savedCat,
			AnsweredAt:      savedAt,
			AnsweredChannel: extractAnsweredChannel(metaRaw, src),
		}, nil
	}
	if status == ContextPromptStatusExpired || !answeredAt.Before(expiresAt) {
		if status != ContextPromptStatusExpired && contextPromptCanAcceptAnswer(status) {
			if _, err := tx.Exec(ctx, `
				UPDATE context_prompt_interactions
				   SET status = $2, updated_at = $3
				 WHERE prompt_id = $1`,
				promptID, ContextPromptStatusExpired, answeredAt); err != nil {
				return nil, err
			}
		}
		return &ContextPromptAnswerResult{
			InboxID:         inboxID,
			PromptID:        promptID,
			Status:          CheckinAnswerStatusExpired,
			EffectiveStatus: ContextPromptStatusExpired,
		}, ErrContextPromptExpired
	}
	if !contextPromptCanAcceptAnswer(status) {
		return &ContextPromptAnswerResult{
			InboxID:         inboxID,
			PromptID:        promptID,
			Status:          CheckinAnswerStatusExpired,
			EffectiveStatus: status,
		}, ErrContextPromptExpired
	}

	var allowed []string
	if err := json.Unmarshal(allowedRaw, &allowed); err != nil {
		return nil, err
	}
	if !stringInSlice(category, allowed) {
		return nil, fmt.Errorf("category %q not allowed for prompt %s", category, promptID)
	}
	nextStatus := ContextPromptStatusAnswered
	if category == ContextPromptCategoryUnknown || category == ContextPromptCategorySkip {
		nextStatus = ContextPromptStatusSkipped
	}
	var savedAt time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE context_prompt_interactions
		   SET status = $2,
		       category = $3,
		       source = COALESCE(NULLIF(source, ''), $4),
		       answered_at = $5,
		       updated_at = $5,
		       metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{answered_channel}', to_jsonb($4::text), true)
		 WHERE prompt_id = $1
		 RETURNING answered_at`,
		promptID, nextStatus, category, channel, answeredAt).Scan(&savedAt); err != nil {
		return nil, err
	}
	if err := markInboxRead(savedAt); err != nil {
		return nil, err
	}
	return &ContextPromptAnswerResult{
		InboxID:         inboxID,
		PromptID:        promptID,
		Status:          CheckinAnswerStatusSaved,
		EffectiveStatus: nextStatus,
		Category:        category,
		AnsweredAt:      savedAt,
		AnsweredChannel: channel,
	}, nil
}

// ListPendingInboxPrompts returns currently answerable prompts for the tenant.
// Contract v1 (TestFlight 33) receives ONLY active prompt_checkin rows;
// Contract v2 receives prompt_checkin, prompt_wake_feedback, and prompt_context.
func (s *DB) ListPendingInboxPrompts(ctx context.Context, todayInTZ string, now time.Time, contractVersion int) ([]InboxRow, error) {
	var query string
	var args []any
	if contractVersion < InboxContractV2 {
		query = inboxSelectSQL + `
			WHERE i.kind = $1
			  AND i.local_date = $2
			  AND c.status = $3
			  AND c.expires_at > $4
			ORDER BY i.created_at DESC, i.id DESC`
		args = []any{InboxKindPromptCheckin, todayInTZ, CheckinStatusPrompted, now}
	} else {
		query = inboxSelectSQL + `
			WHERE (
				(i.kind = $1 AND i.local_date = $2 AND c.status = $3 AND c.expires_at > $4)
				OR (i.kind = $5 AND i.local_date = $2 AND df.response IS NULL)
				OR (i.kind = $6 AND cp.status = $7 AND cp.expires_at > $4)
			)
			ORDER BY
				CASE i.kind
					WHEN 'prompt_checkin' THEN 0
					WHEN 'prompt_wake_feedback' THEN 1
					ELSE 2
				END,
				i.created_at DESC, i.id DESC`
		args = []any{
			InboxKindPromptCheckin, todayInTZ, CheckinStatusPrompted, now,
			InboxKindPromptWakeFeedback,
			InboxKindPromptContext, ContextPromptStatusPrompted,
		}
	}
	rows, err := s.pool.Query(ctx, query, args...)
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
