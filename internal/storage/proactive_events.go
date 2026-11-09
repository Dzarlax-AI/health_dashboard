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

const proactiveEventsDDL = `CREATE TABLE IF NOT EXISTS proactive_events (
 id UUID PRIMARY KEY, rule TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('active','resolved')),
 revision INTEGER NOT NULL, factual_identity TEXT NOT NULL, previous_conclusion TEXT NOT NULL,
 current_conclusion TEXT NOT NULL, source_report_id UUID, report_id UUID,
 opened_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_proactive_events_active_rule ON proactive_events(rule) WHERE state='active';`

func (s *DB) EnsureProactiveEventsTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, proactiveEventsDDL)
	return err
}

type ProactiveEventCandidate struct {
	Rule, FactualIdentity, PreviousConclusion, CurrentConclusion string
	SourceReportID                                               uuid.UUID
	ExpectedEpisodeID                                            uuid.UUID
	ExpectedRevision                                             int
}

// ResolveProactiveEvent ends an episode only after current evaluation finds no
// candidate. The budget lock serializes this transition with reservations.
func (s *DB) ResolveProactiveEvent(ctx context.Context, rule string, expected ActiveProactiveEpisode, now time.Time) (bool, error) {
	if expected.ID == uuid.Nil || expected.Revision < 1 || expected.FactualIdentity == "" {
		return false, errors.New("invalid expected episode")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema() || ':proactive-budget',0))`); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE proactive_events SET state='resolved',resolved_at=$2,updated_at=$2 WHERE rule=$1 AND state='active' AND id=$3 AND revision=$4 AND factual_identity=$5`, rule, now, expected.ID, expected.Revision, expected.FactualIdentity)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReserveProactive atomically applies the tenant-wide daily limit and cooldown,
// episode deduplication, delivery lease, and immutable report insert.
// Legacy sends pass a nil candidate and retain their existing delivery key.
func (s *DB) ReserveProactive(ctx context.Context, h *ReportHistory, key string, event *ProactiveEventCandidate, now time.Time, loc *time.Location) (uuid.UUID, bool, error) {
	if h == nil || h.Kind != "proactive" || key == "" || loc == nil || h.IsPreview {
		return uuid.Nil, false, errors.New("invalid proactive reservation")
	}
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
		key = fmt.Sprintf("proactive:daytime:%s:%d", episode, revision)
	}
	// Fence zero-attempt recovery against ReserveReportAttempt using the
	// existing delivery row lock. No attempted or ambiguous send is retried.
	var state, code string
	var reservedAt time.Time
	retryable := false
	err = tx.QueryRow(ctx, `SELECT status,reserved_at,COALESCE(error_code,'') FROM notification_deliveries WHERE delivery_key=$1 FOR UPDATE`, key).Scan(&state, &reservedAt, &code)
	if err == nil {
		if now.Sub(reservedAt) >= 30*time.Second {
			switch {
			case state == "reserved" || (state == "failed" && code == "archive_unavailable"):
				// Only a latest revision with no attempts proves no network call.
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM report_history h WHERE h.delivery_key=$1 AND h.revision=(SELECT max(revision) FROM report_history WHERE delivery_key=$1) AND NOT EXISTS(SELECT 1 FROM report_delivery_attempts a WHERE a.report_id=h.id))`, key).Scan(&retryable)
			case state == "failed" && code == "provider_rejected":
				var rejections int
				err = tx.QueryRow(ctx, `SELECT count(*) FROM report_delivery_attempts a JOIN report_history h ON h.id=a.report_id WHERE h.delivery_key=$1 AND a.status='failed' AND a.error_code='provider_rejected'`, key).Scan(&rejections)
				retryable = rejections > 0 && rejections < 2
			}
			if err != nil {
				return uuid.Nil, false, err
			}
		}
		if !retryable {
			return uuid.Nil, false, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}

	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	var count int
	var latest *time.Time
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE reserved_at >= $1 AND reserved_at < $2),max(reserved_at) FROM notification_deliveries WHERE delivery_key LIKE 'proactive:%' AND status IN ('reserved','sent','ambiguous') AND delivery_key <> $3`, start, end, key).Scan(&count, &latest)
	if err != nil {
		return uuid.Nil, false, err
	}
	if count >= 2 || (latest != nil && now.Sub(*latest) < time.Hour) {
		return uuid.Nil, false, nil
	}
	token := uuid.New()
	var got uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO notification_deliveries(delivery_key,status,lease_token,reserved_at) VALUES($1,'reserved',$2,$3) ON CONFLICT(delivery_key) DO UPDATE SET status='reserved',lease_token=excluded.lease_token,reserved_at=excluded.reserved_at,completed_at=NULL,error_code=NULL WHERE $4 RETURNING lease_token`, key, token, now, retryable).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	h.ensureFormatRevision()
	h.DeliveryKey = &key
	h.ID = uuid.New()
	h.PreparedAt = now.UTC()
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM report_history WHERE delivery_key=$1`, key).Scan(&h.Revision); err != nil {
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
		fields["proactive"] = map[string]any{"rule": event.Rule, "episode_id": episode.String(), "revision": revision, "category": "guidance_change", "factual_identity": event.FactualIdentity, "source_report_id": event.SourceReportID.String(), "previous_conclusion": event.PreviousConclusion, "current_conclusion": event.CurrentConclusion}
		if event.Rule == "sleep_analysis_update" {
			fields["proactive"].(map[string]any)["category"] = "sleep_analysis_update"
		}
		h.Context, err = json.Marshal(fields)
		if err != nil {
			return uuid.Nil, false, err
		}
	}
	if len(h.AIMetadata) == 0 {
		h.AIMetadata = json.RawMessage(`{}`)
	}
	_, err = tx.Exec(ctx, `INSERT INTO report_history(id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,false,$10,$11,$12,$13,$14,$15,$16)`, h.ID, key, h.Revision, h.Kind, h.LocalDate, h.Timezone, h.Language, h.PreparedAt, h.ScheduledAt, h.PlainBody, h.RichBody, h.DecisionID, h.Context, h.AIMetadata, h.FallbackReason, h.FormatRevision)
	if err != nil {
		return uuid.Nil, false, err
	}
	if event != nil {
		_, err = tx.Exec(ctx, `INSERT INTO proactive_events(id,rule,state,revision,factual_identity,previous_conclusion,current_conclusion,source_report_id,report_id,opened_at,updated_at) VALUES($1,$2,'active',$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,factual_identity=excluded.factual_identity,previous_conclusion=excluded.previous_conclusion,current_conclusion=excluded.current_conclusion,source_report_id=excluded.source_report_id,report_id=excluded.report_id,updated_at=excluded.updated_at`, episode, event.Rule, revision, event.FactualIdentity, event.PreviousConclusion, event.CurrentConclusion, event.SourceReportID, h.ID, now)
		if err != nil {
			return uuid.Nil, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return token, true, nil
}

type ArchivedGuidance struct {
	ReportID   uuid.UUID
	PreparedAt time.Time
	DecisionID string
	Context    json.RawMessage
}

// LastDeliveredGuidance reads only confirmed reports. It never initiates generation.
func (s *DB) LastDeliveredGuidance(ctx context.Context, localDate, kind string) (*ArchivedGuidance, error) {
	var g ArchivedGuidance
	err := s.pool.QueryRow(ctx, `SELECT h.id,h.prepared_at,h.decision_id,h.context FROM report_history h WHERE h.local_date=$1 AND h.kind=$2 AND NOT h.is_preview AND EXISTS (SELECT 1 FROM report_delivery_attempts a WHERE a.report_id=h.id AND a.status='sent') ORDER BY h.prepared_at DESC,h.id DESC LIMIT 1`, localDate, kind).Scan(&g.ReportID, &g.PreparedAt, &g.DecisionID, &g.Context)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *DB) LastDeliveredProactiveAnalysis(ctx context.Context, localDate, rule string) (*ArchivedGuidance, error) {
	var g ArchivedGuidance
	err := s.pool.QueryRow(ctx, `SELECT h.id,h.prepared_at,h.decision_id,h.context FROM report_history h WHERE h.local_date=$1 AND h.kind='proactive' AND h.context->'proactive'->>'rule'=$2 AND NOT h.is_preview AND EXISTS (SELECT 1 FROM report_delivery_attempts a WHERE a.report_id=h.id AND a.status='sent') ORDER BY h.prepared_at DESC,h.id DESC LIMIT 1`, localDate, rule).Scan(&g.ReportID, &g.PreparedAt, &g.DecisionID, &g.Context)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ActiveProactiveEpisode supports rendering against a fenced prior conclusion.
type ActiveProactiveEpisode struct {
	ID              uuid.UUID
	Revision        int
	Conclusion      string
	FactualIdentity string
}

func (s *DB) ActiveProactiveEpisode(ctx context.Context, rule string) (*ActiveProactiveEpisode, error) {
	var e ActiveProactiveEpisode
	err := s.pool.QueryRow(ctx, `SELECT id,revision,current_conclusion,factual_identity FROM proactive_events WHERE rule=$1 AND state='active'`, rule).Scan(&e.ID, &e.Revision, &e.Conclusion, &e.FactualIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}
