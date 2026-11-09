package storage

import (
	"context"
	"log"
	"time"
)

func (s *DB) observeAIGenerationStatus(ctx context.Context, status AIGenerationStatus) {
	if err := s.saveAIGenerationStatus(ctx, status); err != nil {
		// Observability cannot block facts or the existing worker's eligibility.
		log.Print("AI insight diagnostics unavailable")
	}
}

// AIGenerationStatus observes the existing fenced workers. It is never a
// lease, authorization decision, or source of generation eligibility.
type AIGenerationStatus struct {
	Pipeline            string     `json:"pipeline"`
	Date                string     `json:"date"`
	Lang                string     `json:"lang"`
	Slot                string     `json:"slot"`
	MaterialHash        string     `json:"-"`
	ProviderFingerprint string     `json:"-"`
	State               string     `json:"state"`
	ReasonCode          string     `json:"reason_code"`
	Trigger             string     `json:"trigger"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	RetryAfter          *time.Time `json:"retry_after,omitempty"`
}

func (s *DB) EnsureAIGenerationStatusTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS ai_generation_status (
 pipeline TEXT NOT NULL, date TEXT NOT NULL, lang TEXT NOT NULL, slot TEXT NOT NULL,
 material_hash TEXT NOT NULL DEFAULT '', provider_fingerprint TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, reason_code TEXT NOT NULL, trigger TEXT NOT NULL DEFAULT '',
 last_attempt_at TIMESTAMPTZ, last_success_at TIMESTAMPTZ, retry_after TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(pipeline,date,lang,slot))`)
	return err
}

func (s *DB) saveAIGenerationStatus(ctx context.Context, status AIGenerationStatus) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO ai_generation_status
 (pipeline,date,lang,slot,material_hash,provider_fingerprint,state,reason_code,trigger,last_attempt_at,last_success_at,retry_after)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
 ON CONFLICT(pipeline,date,lang,slot) DO UPDATE SET
 material_hash=excluded.material_hash,provider_fingerprint=excluded.provider_fingerprint,
 state=excluded.state,reason_code=excluded.reason_code,trigger=excluded.trigger,
 last_attempt_at=COALESCE(excluded.last_attempt_at,ai_generation_status.last_attempt_at),
 last_success_at=COALESCE(excluded.last_success_at,ai_generation_status.last_success_at),
 retry_after=excluded.retry_after,updated_at=now()
 WHERE (ai_generation_status.material_hash,ai_generation_status.provider_fingerprint,ai_generation_status.state,ai_generation_status.reason_code,ai_generation_status.retry_after)
 IS DISTINCT FROM (excluded.material_hash,excluded.provider_fingerprint,excluded.state,excluded.reason_code,excluded.retry_after)
 OR excluded.last_attempt_at IS NOT NULL OR excluded.last_success_at IS NOT NULL`, status.Pipeline, status.Date, status.Lang, status.Slot, status.MaterialHash, status.ProviderFingerprint, status.State, status.ReasonCode, status.Trigger, status.LastAttemptAt, status.LastSuccessAt, status.RetryAfter)
	return err
}

func (s *DB) GetAIGenerationStatuses(ctx context.Context, date, lang string) ([]AIGenerationStatus, error) {
	rows, err := s.pool.Query(ctx, `SELECT pipeline,date,lang,slot,state,reason_code,trigger,last_attempt_at,last_success_at,retry_after FROM ai_generation_status g WHERE date=$1 AND lang=$2
 AND (pipeline <> 'today' OR material_hash='' OR EXISTS (
 SELECT 1 FROM daily_insight_narrative_slots n WHERE n.date=g.date AND n.lang=g.lang AND n.slot=g.slot AND n.material_input_hash=g.material_hash AND n.provider_fingerprint=g.provider_fingerprint))
 ORDER BY pipeline,slot`, date, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIGenerationStatus{}
	for rows.Next() {
		var status AIGenerationStatus
		if err := rows.Scan(&status.Pipeline, &status.Date, &status.Lang, &status.Slot, &status.State, &status.ReasonCode, &status.Trigger, &status.LastAttemptAt, &status.LastSuccessAt, &status.RetryAfter); err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, rows.Err()
}
