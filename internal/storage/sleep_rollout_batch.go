package storage

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const sleepCandidateProcessingKind = "sleep_candidates"

type IngestSyncSession struct {
	ID    string
	Total int
}

type SleepRolloutLegacyRecord struct {
	PendingHealthRecord
	Session IngestSyncSession
}

// AcceptSleepRolloutBatch serializes receipt lookup and durable acceptance.
// Receipts already committed can be acknowledged in off without accepting
// new candidates or scheduling a second copy of their legacy metrics.
func (s *DB) AcceptSleepRolloutBatch(ctx context.Context, batchID uuid.UUID, record Record, allowNew bool, session IngestSyncSession, validate func() ([]SleepRolloutInput, error)) (id int64, err error) {
	digest := sha256.Sum256([]byte(record.Payload))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	if err := lockSleepRollout(ctx, tx); err != nil {
		return 0, err
	}
	var matches bool
	err = tx.QueryRow(ctx, `SELECT health_record_id, payload_hash=$2 FROM sleep_rollout_batches WHERE batch_id=$1`,
		batchID, digest[:]).Scan(&id, &matches)
	if err == nil {
		if !matches {
			return 0, ErrSleepRolloutConflict
		}
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if !allowNew {
		return 0, ErrSleepRolloutDisabled
	}
	if _, err := sleepRolloutProfile(ctx, tx); err != nil {
		return 0, err
	}
	candidates, err := validate()
	if err != nil {
		return 0, err
	}
	// Revision-bound work is staged in prepare. A previous publication is
	// fenced so new coverage cannot silently switch the live classifier.
	var serving bool
	if err := tx.QueryRow(ctx, `SELECT value='serve' FROM settings WHERE key=$1`, SleepRolloutModeSetting).Scan(&serving); err != nil {
		return 0, err
	}
	if serving {
		publication, err := readSleepRolloutPublication(ctx, tx)
		if err != nil {
			return 0, err
		}
		if publication == nil || publication.Projection == nil {
			for _, candidate := range candidates {
				if candidate.Candidate.Coverage != nil {
					return 0, ErrSleepRolloutConflict
				}
			}
		}
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	record.PendingProcessing = true
	record.ProcessingKind = sleepCandidateProcessingKind
	id, err = unit.insertRawContext(ctx, record)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sleep_rollout_batches (batch_id, payload_hash, health_record_id, sync_session_id, sync_session_total)
		VALUES ($1,$2,$3,$4,$5)`, batchID, digest[:], id, session.ID, session.Total); err != nil {
		return 0, err
	}
	for _, candidate := range candidates {
		if _, err := insertSleepRolloutCandidate(ctx, tx, candidate, &id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

// SleepRolloutPreparing reports the mode without treating failed reads as off.
func (s *DB) SleepRolloutPreparing(ctx context.Context) (bool, error) {
	var mode string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, SleepRolloutModeSetting).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return mode == "prepare" || mode == "serve", err
}

// ProcessSleepRolloutLegacyNext processes one mixed batch. Its ordinary points,
// cache writes and completed status become visible together. The prepare
// worker can only use the candidate after this transaction commits.
func (s *DB) ProcessSleepRolloutLegacyNext(ctx context.Context, process func(*DB, SleepRolloutLegacyRecord) error) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var claimed bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(
		hashtextextended('health/sleep-rollout-legacy/' || current_schema(),0))`).Scan(&claimed); err != nil || !claimed {
		return false, err
	}
	var record SleepRolloutLegacyRecord
	err = tx.QueryRow(ctx, `SELECT h.id,h.payload,h.processing_kind,b.sync_session_id,b.sync_session_total
		FROM sleep_rollout_batches b JOIN health_records h ON h.id=b.health_record_id
		WHERE h.processing_status='pending'
		ORDER BY h.id LIMIT 1`).Scan(&record.ID, &record.Payload, &record.ProcessingKind, &record.Session.ID, &record.Session.Total)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := s.BeginDashboardRefresh()
	defer finish()
	if err := s.cacheMu.LockBackground(ctx); err != nil {
		return false, err
	}
	defer s.cacheMu.Unlock()
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}, preserveNewerIngest: true}
	if err := process(unit, record); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	// The coordinator runs on the serving DB, not the transactional unit.
	if len(unit.cacheAppliedGenerations) != 0 {
		if s.cacheAppliedGenerations == nil {
			s.cacheAppliedGenerations = make(map[string]uint64)
		}
		for date, generation := range unit.cacheAppliedGenerations {
			s.cacheAppliedGenerations[date] = generation
		}
	}
	return true, nil
}

// AppliedIngestCacheDates returns the dates that still have the exact input
// generation already aggregated by this process.
func (s *DB) AppliedIngestCacheDates(dates []string) (map[string]bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	dirty, err := s.CaptureCacheDirty(ctx, dates)
	if err != nil {
		return nil, err
	}
	applied := make(map[string]bool, len(dates))
	for _, date := range dates {
		generation, exists := s.cacheAppliedGenerations[date]
		applied[date] = exists && generation == dirty[date]
	}
	return applied, nil
}
