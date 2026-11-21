package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SleepRolloutAudit struct {
	projection              *sleepGoProjection
	projectionBoundary      int64
	VerifiedProjectionDates int                    `json:"verified_projection_dates,omitempty"`
	ProjectionReady         bool                   `json:"projection_ready,omitempty"`
	Evidence                string                 `json:"evidence"`
	From                    string                 `json:"from"`
	Through                 string                 `json:"through"`
	ExpectedDates           int                    `json:"expected_dates"`
	MatchedDates            int                    `json:"matched_dates"`
	UnownedDates            int                    `json:"unowned_dates"`
	MissingResults          int                    `json:"missing_results"`
	MismatchedResults       int                    `json:"mismatched_results"`
	PendingMixedBatches     int                    `json:"pending_mixed_batches"`
	FailedMixedBatches      int                    `json:"failed_mixed_batches"`
	PendingLegacyDates      int                    `json:"pending_legacy_dates"`
	PendingCandidates       int                    `json:"pending_candidates"`
	Dates                   []SleepRolloutAuditDay `json:"dates"`
}

type SleepRolloutAuditDay struct {
	Date         string          `json:"date"`
	Sleep        string          `json:"sleep"`
	Result       string          `json:"result"`
	Generation   int64           `json:"generation,omitempty"`
	ReadinessOK  bool            `json:"readiness_ok"`
	EnergyBankOK bool            `json:"energy_bank_ok"`
	Selection    *sleepSelection `json:"episode_selection,omitempty"`
}

// AuditSleepRolloutPreparation compares each prepared date against the current
// Go formulas and tenant inputs from a single read-only database snapshot.
// Revision-bound windows check the reader's eligibility and generation fence
// instead of comparing the legacy preparation result on another projection.
func (s *DB) AuditSleepRolloutPreparation(ctx context.Context, tenantID uuid.UUID, from, through string) (SleepRolloutAudit, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SleepRolloutAudit{}, err
	}
	defer tx.Rollback(context.Background())
	return auditSleepRolloutInTransaction(ctx, tx, tenantID, from, through)
}

func auditSleepRolloutInTransaction(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, from, through string) (SleepRolloutAudit, error) {
	report := SleepRolloutAudit{From: from, Through: through, Dates: []SleepRolloutAuditDay{}}
	start, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return report, err
	}
	end, err := time.Parse(time.DateOnly, through)
	if err != nil {
		return report, err
	}
	if end.Before(start) || tenantID == uuid.Nil {
		return report, errors.New("audit requires a tenant identity and an increasing date range")
	}
	if err := verifySleepRolloutTenant(ctx, tx, tenantID); err != nil {
		return report, err
	}
	projection, err := transitionSleepProjection(ctx, tx)
	if err != nil {
		return report, err
	}
	if projection != nil {
		return auditRevisionBoundSleepRollout(ctx, tx, report, tenantID, projection)
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	report.Evidence, err = sleepRolloutEvidence(ctx, tx, tenantID, from, through)
	if err != nil {
		return report, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM sleep_rollout_batches b
		JOIN health_records h ON h.id=b.health_record_id WHERE h.processing_status='pending'`).
		Scan(&report.PendingMixedBatches); err != nil {
		return report, err
	}
	report.FailedMixedBatches, err = unresolvedSleepRolloutBatches(ctx, tx)
	if err != nil {
		return report, err
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM sleep_rollout_packets WHERE applied_at IS NULL`).
		Scan(&report.PendingCandidates); err != nil {
		return report, err
	}

	report.PendingLegacyDates, err = pendingSleepRolloutLegacyDates(ctx, tx)
	if err != nil {
		return report, err
	}
	cfg := unit.GetEnergyConfig()
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		report.ExpectedDates++
		result, err := unit.auditSleepRolloutDay(ctx, date, cfg, sleepRolloutWindow{From: from, Through: through})
		if err != nil {
			return report, fmt.Errorf("audit sleep date %s: %w", date, err)
		}
		if result.Sleep == "unowned" {
			report.UnownedDates++
		}
		switch result.Result {
		case "match":
			report.MatchedDates++
		case "missing":
			report.MissingResults++
		case "mismatch":
			report.MismatchedResults++
		}
		report.Dates = append(report.Dates, result)
	}
	return report, nil
}

// A failed receipt stays unresolved unless one completed replacement owns
// every original candidate and preserves every valid ordinary input. Keep
// both immutable receipts so the decision can be recomputed during audit.
func unresolvedSleepRolloutBatches(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT original.id, (original.payload::jsonb->'data')::text, (replacement.payload::jsonb->'data')::text
		FROM sleep_rollout_batches b
		JOIN health_records original ON original.id=b.health_record_id AND original.processing_status='failed'
		LEFT JOIN LATERAL (
			SELECT h.payload
			FROM sleep_rollout_batches retry
			JOIN health_records h ON h.id=retry.health_record_id AND h.processing_status='complete'
			WHERE retry.health_record_id>original.id
			AND jsonb_typeof(original.payload::jsonb #> '{data,sleep_candidates_v1,candidates}')='array'
			AND (h.payload::jsonb #> '{data,sleep_candidates_v1,candidates}')
				 @> (original.payload::jsonb #> '{data,sleep_candidates_v1,candidates}')
			AND NOT EXISTS (SELECT 1 FROM sleep_rollout_packets p
				WHERE p.health_record_id=original.id AND p.applied_at IS NULL)
		) replacement ON true`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	// Multiple valid retries can exist. A full replacement resolves the
	// original once, even if another retry omitted some ordinary inputs.
	resolved := make(map[int64]bool)
	for rows.Next() {
		var id int64
		var original, replacement *string
		if err := rows.Scan(&id, &original, &replacement); err != nil {
			return 0, err
		}
		resolved[id] = resolved[id] || (original != nil && replacement != nil && replacementCoversMixedMetrics(*original, *replacement))
	}
	var count int
	for _, repaired := range resolved {
		if !repaired {
			count++
		}
	}
	return count, rows.Err()
}

func replacementCoversMixedMetrics(original, replacement string) bool {
	type metric struct {
		Name  string
		Units string
		Data  []json.RawMessage
	}
	parse := func(raw string) (map[string]json.RawMessage, []metric, error) {
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return nil, nil, err
		}
		var metrics []metric
		if err := json.Unmarshal(metadata["metrics"], &metrics); err != nil {
			return nil, nil, err
		}
		delete(metadata, "metrics")
		delete(metadata, "sleep_candidates_v1")
		return metadata, metrics, nil
	}
	oldMetadata, oldMetrics, oldErr := parse(original)
	newMetadata, newMetrics, newErr := parse(replacement)
	if oldErr != nil || newErr != nil {
		return false
	}
	for key, value := range oldMetadata {
		if !bytes.Equal(value, newMetadata[key]) {
			return false
		}
	}
	type sampleKey struct{ name, units, raw string }
	present := make(map[sampleKey]bool)
	for _, metric := range newMetrics {
		for _, raw := range metric.Data {
			present[sampleKey{metric.Name, metric.Units, string(raw)}] = true
		}
	}
	for _, metric := range oldMetrics {
		for _, raw := range metric.Data {
			var point struct{ Date string }
			if json.Unmarshal(raw, &point) == nil && ValidateIngestMetricTime(point.Date) == nil &&
				!present[sampleKey{metric.Name, metric.Units, string(raw)}] {
				return false
			}
		}
	}
	return true
}

func (s *DB) auditSleepRolloutDay(ctx context.Context, date string, cfg EnergyConfig, window sleepRolloutWindow) (SleepRolloutAuditDay, error) {
	out := SleepRolloutAuditDay{Date: date, Sleep: "unowned", Result: "missing"}
	var (
		owned     bool
		hasNight  bool
		selection []byte
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sleep_rollout_daily WHERE wake_date=$1::text::date),
		       EXISTS(SELECT 1 FROM sleep_rollout_daily WHERE wake_date=$1::text::date AND candidate IS NOT NULL),
		       (SELECT episode_selection FROM sleep_rollout_daily WHERE wake_date=$1::text::date)`, date).
		Scan(&owned, &hasNight, &selection); err != nil {
		return out, err
	}
	if hasNight {
		out.Sleep = "candidate"
	} else if owned {
		out.Sleep = "confirmed_absence"
	}

	if selection != nil {
		if err := json.Unmarshal(selection, &out.Selection); err != nil {
			return out, err
		}
		out.Sleep = out.Selection.State
	}

	var readinessJSON, energyJSON []byte
	err := s.pool.QueryRow(ctx, `SELECT generation,readiness,energy FROM sleep_rollout_results WHERE day=$1::text::date`, date).
		Scan(&out.Generation, &readinessJSON, &energyJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var savedReadiness LocalShadowReadiness
	var savedEnergy BankResult
	if err := json.Unmarshal(readinessJSON, &savedReadiness); err != nil {
		return out, err
	}
	if err := json.Unmarshal(energyJSON, &savedEnergy); err != nil {
		return out, err
	}
	currentReadiness, currentEnergy, err := s.calculateSleepRolloutResult(ctx, date, cfg, window)
	if err != nil {
		return out, err
	}
	out.ReadinessOK = reflect.DeepEqual(&savedReadiness, currentReadiness)
	out.EnergyBankOK = reflect.DeepEqual(savedEnergy, currentEnergy)
	out.Result = "mismatch"
	if out.ReadinessOK && out.EnergyBankOK {
		out.Result = "match"
	}
	return out, nil
}
