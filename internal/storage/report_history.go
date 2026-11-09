package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReportHistory preserves the prepared bodies and the evidence identifying a send.
// Its delivery key is nil for previews, which never occupy a scheduled send key.
type ReportHistory struct {
	ID             uuid.UUID       `json:"id"`
	DeliveryKey    *string         `json:"delivery_key,omitempty"`
	Revision       int             `json:"revision"`
	FormatRevision string          `json:"format_revision"`
	Kind           string          `json:"kind"`
	LocalDate      string          `json:"local_date"`
	Timezone       string          `json:"timezone"`
	Language       string          `json:"language"`
	PreparedAt     time.Time       `json:"prepared_at"`
	ScheduledAt    *time.Time      `json:"scheduled_at,omitempty"`
	IsPreview      bool            `json:"is_preview"`
	PlainBody      string          `json:"plain_body"`
	RichBody       string          `json:"rich_body"`
	DecisionID     string          `json:"decision_id,omitempty"`
	Context        json.RawMessage `json:"context"`
	AIMetadata     json.RawMessage `json:"ai_metadata"`
	FallbackReason string          `json:"fallback_reason,omitempty"`
}

type ReportDeliveryAttempt struct {
	ID          uuid.UUID  `json:"id"`
	ReportID    uuid.UUID  `json:"report_id"`
	Format      string     `json:"format"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	ReservedAt  time.Time  `json:"reserved_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ErrorCode   string     `json:"error_code,omitempty"`
	MessageID   *int64     `json:"message_id,omitempty"`
}

const reportHistoryDDL = `
CREATE TABLE IF NOT EXISTS report_history (
 id UUID PRIMARY KEY, delivery_key TEXT, revision INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('morning','evening','proactive')), format_revision TEXT NOT NULL,
 local_date TEXT NOT NULL, timezone TEXT NOT NULL, language TEXT NOT NULL,
 prepared_at TIMESTAMPTZ NOT NULL, scheduled_at TIMESTAMPTZ, is_preview BOOLEAN NOT NULL,
 plain_body TEXT NOT NULL, rich_body TEXT NOT NULL, decision_id TEXT NOT NULL,
 context JSONB NOT NULL, ai_metadata JSONB NOT NULL, fallback_reason TEXT NOT NULL,
 UNIQUE (delivery_key, revision), CHECK ((is_preview AND delivery_key IS NULL) OR (NOT is_preview AND delivery_key IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS report_delivery_attempts (
 id UUID PRIMARY KEY, report_id UUID NOT NULL REFERENCES report_history(id), format TEXT NOT NULL CHECK (format IN ('html','rich')),
 body TEXT NOT NULL, status TEXT NOT NULL CHECK (status IN ('reserved','sent','failed','ambiguous')),
 reserved_at TIMESTAMPTZ NOT NULL, completed_at TIMESTAMPTZ, error_code TEXT NOT NULL, message_id BIGINT
);
CREATE INDEX IF NOT EXISTS idx_report_history_prepared ON report_history (prepared_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_report_attempts_report ON report_delivery_attempts (report_id, reserved_at, id);
`

func (s *DB) EnsureReportHistoryTablesContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, reportHistoryDDL)
	return err
}

func (h *ReportHistory) ensureFormatRevision() {
	if h.FormatRevision != "" {
		return
	}
	switch h.Kind {
	case "morning":
		h.FormatRevision = "morning-domains-v2"
	case "evening":
		h.FormatRevision = "evening-v1"
	case "proactive":
		h.FormatRevision = "proactive-v1"
	}
}

// ReserveArchivedReport commits the ordinary delivery reservation and immutable
// report in one transaction; a failed transaction cannot start a network send.
func (s *DB) ReserveArchivedReport(ctx context.Context, h *ReportHistory) (uuid.UUID, bool, error) {
	if h == nil || (!h.IsPreview && (h.DeliveryKey == nil || *h.DeliveryKey == "")) || (h.IsPreview && h.DeliveryKey != nil) {
		return uuid.Nil, false, errors.New("invalid report reservation")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)
	token := uuid.New()
	if !h.IsPreview {
		var got uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO notification_deliveries(delivery_key,status,lease_token) VALUES($1,'reserved',$2) ON CONFLICT DO NOTHING RETURNING lease_token`, *h.DeliveryKey, token).Scan(&got)
		if errors.Is(err, pgx.ErrNoRows) {
			var state string
			var reservedAt time.Time
			if err = tx.QueryRow(ctx, `SELECT status,reserved_at FROM notification_deliveries WHERE delivery_key=$1 FOR UPDATE`, *h.DeliveryKey).Scan(&state, &reservedAt); err != nil {
				return uuid.Nil, false, err
			}
			retryable := state == "failed"
			if state == "reserved" && time.Since(reservedAt) >= 30*time.Second {
				// Holding the delivery row lock fences a previous preparer.
				// Only an archived latest revision with zero transport attempts
				// can be recovered. Legacy reservations remain ambiguous.
				var noAttempt bool
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM report_history h WHERE h.delivery_key=$1 AND h.revision=(SELECT max(revision) FROM report_history WHERE delivery_key=$1) AND NOT EXISTS(SELECT 1 FROM report_delivery_attempts a WHERE a.report_id=h.id))`, *h.DeliveryKey).Scan(&noAttempt)
				if err != nil {
					return uuid.Nil, false, err
				}
				retryable = noAttempt
			}
			if !retryable {
				return uuid.Nil, false, nil
			}
			_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='reserved',lease_token=$2,reserved_at=now(),completed_at=NULL,error_code=NULL WHERE delivery_key=$1`, *h.DeliveryKey, token)
		}
		if err != nil {
			return uuid.Nil, false, err
		}
		if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM report_history WHERE delivery_key=$1`, *h.DeliveryKey).Scan(&h.Revision); err != nil {
			return uuid.Nil, false, err
		}
	} else {
		h.Revision = 1
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
	_, err = tx.Exec(ctx, `INSERT INTO report_history(id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, h.ID, h.DeliveryKey, h.Revision, h.Kind, h.LocalDate, h.Timezone, h.Language, h.PreparedAt, h.ScheduledAt, h.IsPreview, h.PlainBody, h.RichBody, h.DecisionID, h.Context, h.AIMetadata, h.FallbackReason, h.FormatRevision)
	if err != nil {
		return uuid.Nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return token, true, nil
}

func (s *DB) ReserveReportAttempt(ctx context.Context, reportID uuid.UUID, format, body string, token uuid.UUID) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var deliveryKey *string
	var preview bool
	if err = tx.QueryRow(ctx, `SELECT delivery_key,is_preview FROM report_history WHERE id=$1`, reportID).Scan(&deliveryKey, &preview); err != nil {
		return uuid.Nil, err
	}
	if !preview {
		var owned bool
		// This row lock serializes the first possible send with recovery of a
		// zero-attempt reservation. A stale preparer can never start transport.
		err = tx.QueryRow(ctx, `SELECT true FROM notification_deliveries WHERE delivery_key=$1 AND lease_token=$2 AND status='reserved' FOR UPDATE`, deliveryKey, token).Scan(&owned)
		if err != nil {
			return uuid.Nil, err
		}
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO report_delivery_attempts(id,report_id,format,body,status,reserved_at,error_code) VALUES($1,$2,$3,$4,'reserved',NOW(),'')`, id, reportID, format, body)
	if err != nil {
		return uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// CompleteReportAttempt persists only safe error codes, never provider descriptions.
func (s *DB) CompleteReportAttempt(ctx context.Context, id uuid.UUID, status, code string, messageID *int64) error {
	if status != "sent" && status != "failed" && status != "ambiguous" {
		return errors.New("invalid attempt status")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE report_delivery_attempts SET status=$2,completed_at=NOW(),error_code=$3,message_id=$4 WHERE id=$1 AND status='reserved'`, id, status, code, messageID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("attempt is no longer reserved")
	}
	return nil
}

// ReportHistoryItem includes metadata-only rows for deliveries predating this archive.
type ReportHistoryItem struct {
	ID               uuid.UUID `json:"id"`
	Kind             string    `json:"kind"`
	LocalDate        string    `json:"local_date"`
	PreparedAt       time.Time `json:"prepared_at"`
	IsPreview        bool      `json:"is_preview"`
	ContentAvailable bool      `json:"content_available"`
	Status           string    `json:"status"`
}

type ReportHistoryQuery struct {
	From, To, Kind string
	Limit          int
	BeforeAt       *time.Time
	BeforeID       uuid.UUID
}

const historyRowsSQL = `WITH all_reports AS (
 SELECT h.id,h.kind,h.local_date,h.prepared_at,h.is_preview,true AS content_available,
 COALESCE((SELECT a.status FROM report_delivery_attempts a WHERE a.report_id=h.id ORDER BY a.reserved_at DESC,a.id DESC LIMIT 1), 'reserved') AS status
 FROM report_history h
 UNION ALL
 SELECT md5('legacy:' || current_schema() || ':' || n.delivery_key)::uuid,
 CASE WHEN n.delivery_key LIKE 'report:morning:%' THEN 'morning' WHEN n.delivery_key LIKE 'report:evening:%' THEN 'evening' ELSE 'proactive' END,
 right(n.delivery_key,10),n.reserved_at,false,false,n.status
 FROM notification_deliveries n
 WHERE (n.delivery_key LIKE 'report:morning:%' OR n.delivery_key LIKE 'report:evening:%' OR n.delivery_key LIKE 'proactive:%')
 AND NOT EXISTS (SELECT 1 FROM report_history h WHERE h.delivery_key=n.delivery_key)
)`

func (s *DB) ListReportHistory(ctx context.Context, q ReportHistoryQuery) ([]ReportHistoryItem, error) {
	rows, err := s.pool.Query(ctx, historyRowsSQL+` SELECT id,kind,local_date,prepared_at,is_preview,content_available,status FROM all_reports WHERE local_date >= $1 AND local_date <= $2 AND ($3='' OR kind=$3) AND ($4::timestamptz IS NULL OR (prepared_at,id)<($4,$5)) ORDER BY prepared_at DESC,id DESC LIMIT $6`, q.From, q.To, q.Kind, q.BeforeAt, q.BeforeID, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReportHistoryItem{}
	for rows.Next() {
		var x ReportHistoryItem
		if err = rows.Scan(&x.ID, &x.Kind, &x.LocalDate, &x.PreparedAt, &x.IsPreview, &x.ContentAvailable, &x.Status); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

type ReportHistoryDetail struct {
	ReportHistory
	ContentAvailable bool                    `json:"content_available"`
	Attempts         []ReportDeliveryAttempt `json:"attempts"`
}

func (s *DB) GetReportHistory(ctx context.Context, id uuid.UUID) (*ReportHistoryDetail, error) {
	var d ReportHistoryDetail
	err := s.pool.QueryRow(ctx, `SELECT id,delivery_key,revision,kind,local_date,timezone,language,prepared_at,scheduled_at,is_preview,plain_body,rich_body,decision_id,context,ai_metadata,fallback_reason,format_revision FROM report_history WHERE id=$1`, id).Scan(&d.ID, &d.DeliveryKey, &d.Revision, &d.Kind, &d.LocalDate, &d.Timezone, &d.Language, &d.PreparedAt, &d.ScheduledAt, &d.IsPreview, &d.PlainBody, &d.RichBody, &d.DecisionID, &d.Context, &d.AIMetadata, &d.FallbackReason, &d.FormatRevision)
	if err != nil {
		return nil, err
	}
	d.ContentAvailable = true
	d.Attempts = []ReportDeliveryAttempt{}
	rows, err := s.pool.Query(ctx, `SELECT id,report_id,format,body,status,reserved_at,completed_at,error_code,message_id FROM report_delivery_attempts WHERE report_id=$1 ORDER BY reserved_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a ReportDeliveryAttempt
		if err = rows.Scan(&a.ID, &a.ReportID, &a.Format, &a.Body, &a.Status, &a.ReservedAt, &a.CompletedAt, &a.ErrorCode, &a.MessageID); err != nil {
			return nil, err
		}
		d.Attempts = append(d.Attempts, a)
	}
	return &d, rows.Err()
}

func (s *DB) GetLegacyReportHistory(ctx context.Context, id uuid.UUID) (*ReportHistoryItem, error) {
	var x ReportHistoryItem
	err := s.pool.QueryRow(ctx, historyRowsSQL+` SELECT id,kind,local_date,prepared_at,is_preview,content_available,status FROM all_reports WHERE id=$1 AND NOT content_available`, id).Scan(&x.ID, &x.Kind, &x.LocalDate, &x.PreparedAt, &x.IsPreview, &x.ContentAvailable, &x.Status)
	if err != nil {
		return nil, err
	}
	return &x, nil
}
