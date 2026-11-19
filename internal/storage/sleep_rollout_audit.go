package storage

import (
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
	Evidence            string                 `json:"evidence"`
	From                string                 `json:"from"`
	Through             string                 `json:"through"`
	ExpectedDates       int                    `json:"expected_dates"`
	MatchedDates        int                    `json:"matched_dates"`
	UnownedDates        int                    `json:"unowned_dates"`
	MissingResults      int                    `json:"missing_results"`
	MismatchedResults   int                    `json:"mismatched_results"`
	PendingMixedBatches int                    `json:"pending_mixed_batches"`
	FailedMixedBatches  int                    `json:"failed_mixed_batches"`
	PendingLegacyDates  int                    `json:"pending_legacy_dates"`
	PendingCandidates   int                    `json:"pending_candidates"`
	Dates               []SleepRolloutAuditDay `json:"dates"`
}

type SleepRolloutAuditDay struct {
	Date         string `json:"date"`
	Sleep        string `json:"sleep"`
	Result       string `json:"result"`
	Generation   int64  `json:"generation,omitempty"`
	ReadinessOK  bool   `json:"readiness_ok"`
	EnergyBankOK bool   `json:"energy_bank_ok"`
}

// AuditSleepRolloutPreparation compares each prepared date against the current
// Go formulas and tenant inputs from a single read-only database snapshot.
// The report is evidence for rollout review, not an activation token.
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
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	report.Evidence, err = sleepRolloutEvidence(ctx, tx, tenantID, from, through)
	if err != nil {
		return report, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE h.processing_status='pending'),
		       COUNT(*) FILTER (WHERE h.processing_status='failed')
		FROM sleep_rollout_batches b JOIN health_records h ON h.id=b.health_record_id`).
		Scan(&report.PendingMixedBatches, &report.FailedMixedBatches); err != nil {
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
		result, err := unit.auditSleepRolloutDay(ctx, date, cfg)
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

func (s *DB) auditSleepRolloutDay(ctx context.Context, date string, cfg EnergyConfig) (SleepRolloutAuditDay, error) {
	out := SleepRolloutAuditDay{Date: date, Sleep: "unowned", Result: "missing"}
	var (
		owned    bool
		hasNight bool
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sleep_rollout_daily WHERE wake_date=$1::text::date),
		       EXISTS(SELECT 1 FROM sleep_rollout_daily WHERE wake_date=$1::text::date AND candidate IS NOT NULL)`, date).
		Scan(&owned, &hasNight); err != nil {
		return out, err
	}
	if hasNight {
		out.Sleep = "candidate"
	} else if owned {
		out.Sleep = "confirmed_absence"
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
	currentReadiness, err := s.readRolloutReadiness(ctx, date)
	if err != nil {
		return out, err
	}
	currentEnergy, err := s.readRolloutEnergy(ctx, date, cfg)
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
