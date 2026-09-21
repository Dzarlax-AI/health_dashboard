package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

// DailyInsightNarrativeSlot is one durable B1 text overlay. Runtime currently
// uses only `overall`; the row hash is intentionally separate from the B0
// snapshot hash so a material update makes stale prose unreadable at once.
type DailyInsightNarrativeSlot struct {
	Date                string
	Lang                string
	Slot                string
	MaterialInputHash   string
	ProviderFingerprint string
	NarrativeInputHash  string
	Narrative           json.RawMessage
	GenerationState     string
	LeaseHash           string
	LeaseUntil          *time.Time
	FailureCount        int
	RetryAfter          *time.Time
	UpdatedAt           time.Time
}

func (s *DB) EnsureDailyInsightNarrativeSlotsTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS daily_insight_narrative_slots (
			date                 TEXT NOT NULL,
			lang                 TEXT NOT NULL,
			slot                 TEXT NOT NULL,
			material_input_hash  TEXT NOT NULL,
			provider_fingerprint TEXT NOT NULL,
			narrative_input_hash TEXT,
			narrative            JSONB,
			generation_state     TEXT NOT NULL,
			lease_hash           TEXT,
			lease_until          TIMESTAMPTZ,
			failure_count        INTEGER NOT NULL DEFAULT 0,
			retry_after          TIMESTAMPTZ,
			updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (date, lang, slot)
		)
	`)
	return err
}

func (s *DB) GetDailyInsightNarrativeSlots(date, lang string) (map[string]DailyInsightNarrativeSlot, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT slot, material_input_hash, provider_fingerprint, COALESCE(narrative_input_hash, ''),
		       narrative, generation_state, COALESCE(lease_hash, ''), lease_until,
		       failure_count, retry_after, updated_at
		  FROM daily_insight_narrative_slots
		 WHERE date=$1 AND lang=$2`, date, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]DailyInsightNarrativeSlot)
	for rows.Next() {
		var entry DailyInsightNarrativeSlot
		if err := rows.Scan(&entry.Slot, &entry.MaterialInputHash, &entry.ProviderFingerprint, &entry.NarrativeInputHash,
			&entry.Narrative, &entry.GenerationState, &entry.LeaseHash, &entry.LeaseUntil,
			&entry.FailureCount, &entry.RetryAfter, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		if !healthDailyInsightNarrativeSlot(entry.Slot) {
			return nil, errors.New("invalid stored daily insight narrative slot")
		}
		if len(entry.Narrative) > 0 && !json.Valid(entry.Narrative) {
			entry.Narrative = nil
			if entry.GenerationState == DailyInsightStateReady {
				entry.GenerationState = DailyInsightStateFailed
			}
		}
		entry.Date, entry.Lang = date, lang
		result[entry.Slot] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func healthDailyInsightNarrativeSlot(slot string) bool {
	_, known := health.BuildDailyInsightNarrativeSlotInput(nil, "en", slot)
	return known
}

// UpsertDailyInsightNarrativeSlot records current closed input material. Old
// prose remains physically available for audit/debugging, but its old input
// hash makes it impossible to render after a material change.
func (s *DB) UpsertDailyInsightNarrativeSlot(ctx context.Context, entry DailyInsightNarrativeSlot) error {
	if entry.Date == "" || entry.Lang == "" || entry.MaterialInputHash == "" || entry.ProviderFingerprint == "" || !healthDailyInsightNarrativeSlot(entry.Slot) {
		return errors.New("invalid daily insight narrative slot")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO daily_insight_narrative_slots(
			date, lang, slot, material_input_hash, provider_fingerprint, generation_state, updated_at
		) VALUES($1,$2,$3,$4,$5,'cold',NOW())
		ON CONFLICT(date,lang,slot) DO UPDATE SET
			material_input_hash=excluded.material_input_hash,
			provider_fingerprint=excluded.provider_fingerprint,
			generation_state=CASE WHEN daily_insight_narrative_slots.material_input_hash=excluded.material_input_hash
				AND daily_insight_narrative_slots.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_narrative_slots.generation_state ELSE 'cold' END,
			lease_hash=CASE WHEN daily_insight_narrative_slots.material_input_hash=excluded.material_input_hash
				AND daily_insight_narrative_slots.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_narrative_slots.lease_hash ELSE NULL END,
			lease_until=CASE WHEN daily_insight_narrative_slots.material_input_hash=excluded.material_input_hash
				AND daily_insight_narrative_slots.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_narrative_slots.lease_until ELSE NULL END,
			failure_count=CASE WHEN daily_insight_narrative_slots.material_input_hash=excluded.material_input_hash
				AND daily_insight_narrative_slots.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_narrative_slots.failure_count ELSE 0 END,
			retry_after=CASE WHEN daily_insight_narrative_slots.material_input_hash=excluded.material_input_hash
				AND daily_insight_narrative_slots.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_narrative_slots.retry_after ELSE NULL END,
			updated_at=NOW()`, entry.Date, entry.Lang, entry.Slot, entry.MaterialInputHash, entry.ProviderFingerprint)
	return err
}

func (s *DB) ClaimDailyInsightNarrativeSlotGeneration(ctx context.Context, date, lang, slot, materialHash, providerFingerprint string, now time.Time) (string, error) {
	if date == "" || lang == "" || materialHash == "" || providerFingerprint == "" || !healthDailyInsightNarrativeSlot(slot) {
		return "", errors.New("invalid daily insight narrative slot claim")
	}
	leaseToken, err := newDailyInsightLeaseToken()
	if err != nil {
		return "", err
	}
	leaseUntil := now.Add(dailyInsightLeaseDuration)
	tag, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_narrative_slots
		   SET generation_state='generating', lease_hash=$5, lease_until=$6, updated_at=$7
		 WHERE date=$1 AND lang=$2 AND slot=$3 AND material_input_hash=$4
		   AND provider_fingerprint=$8
		   AND generation_state IN ('cold','failed','generating')
		   AND (lease_until IS NULL OR lease_until <= $7)
		   AND (retry_after IS NULL OR retry_after <= $7)`,
		date, lang, slot, materialHash, leaseToken, leaseUntil, now, providerFingerprint)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", nil
	}
	return leaseToken, nil
}

func (s *DB) SaveDailyInsightNarrativeSlot(ctx context.Context, date, lang, slot, materialHash, providerFingerprint, leaseToken string, narrative json.RawMessage) (bool, error) {
	if leaseToken == "" || !json.Valid(narrative) {
		return false, errors.New("invalid daily insight narrative slot save")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_narrative_slots
		   SET narrative=$7, narrative_input_hash=$4, generation_state='ready',
		       lease_hash=NULL, lease_until=NULL, failure_count=0, retry_after=NULL, updated_at=NOW()
		 WHERE date=$1 AND lang=$2 AND slot=$3 AND material_input_hash=$4
		   AND provider_fingerprint=$5 AND lease_hash=$6`,
		date, lang, slot, materialHash, providerFingerprint, leaseToken, json.RawMessage(narrative))
	return tag.RowsAffected() == 1, err
}

func (s *DB) FailDailyInsightNarrativeSlotGeneration(ctx context.Context, date, lang, slot, materialHash, providerFingerprint, leaseToken string, now time.Time) (bool, error) {
	if leaseToken == "" {
		return false, errors.New("missing daily insight narrative slot lease token")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_narrative_slots
		   SET generation_state='failed', lease_hash=NULL, lease_until=NULL,
		       failure_count=failure_count+1, retry_after=$6, updated_at=$7
		 WHERE date=$1 AND lang=$2 AND slot=$3 AND material_input_hash=$4
		   AND provider_fingerprint=$5 AND lease_hash=$8`,
		date, lang, slot, materialHash, providerFingerprint, now.Add(dailyInsightFailureBackoff), now, leaseToken)
	return tag.RowsAffected() == 1, err
}

func (s *DB) InvalidateDailyInsightNarrativeSlot(ctx context.Context, date, lang, slot, materialHash, providerFingerprint string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_narrative_slots
		   SET narrative=NULL, narrative_input_hash=NULL, generation_state='failed',
		       lease_hash=NULL, lease_until=NULL, retry_after=NULL, updated_at=NOW()
		 WHERE date=$1 AND lang=$2 AND slot=$3 AND material_input_hash=$4
		   AND provider_fingerprint=$5 AND generation_state='ready'`,
		date, lang, slot, materialHash, providerFingerprint)
	return err
}

// IsNoDailyInsightNarrativeSlot returns true when a query genuinely found no
// row. It keeps callers from confusing storage failures with a cold slot.
func IsNoDailyInsightNarrativeSlot(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
