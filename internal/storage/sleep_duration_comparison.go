package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

const sleepComparisonSetting = "sleep_duration_comparison_enabled"

// GetSleepDurationComparison returns nil when the tenant's pilot is disabled.
// The canonical night and dbt reference are read in a single statement.
func (s *DB) GetSleepDurationComparison(ctx context.Context, wakeDate string) *health.SleepDurationComparison {
	date, err := health.ParseSleepComparisonDate(wakeDate)
	if err != nil {
		return &health.SleepDurationComparison{
			WakeDate: wakeDate, Status: health.SleepComparisonUnavailable, RuleVersion: health.SleepComparisonRuleVersion,
		}
	}
	fact := health.NewSleepDurationComparison(date)

	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var enabled bool
	if err := s.pool.QueryRow(readCtx, `select exists (
		select 1 from settings where key=$1 and value='true'
	)`, sleepComparisonSetting).Scan(&enabled); err != nil {
		return &fact
	}
	if !enabled {
		return nil
	}

	var n health.CompletedNightSleep
	var historyVersion string
	query := sleepComparisonQuery
	if s.profileGraphSchema != "" {
		query = `select c.status,c.duration_minutes,c.reference_minutes,c.delta_minutes,
			c.source,c.source_epoch,c.algorithm_version,c.input_hash,
			coalesce(n.diagnostics->>'preferred_sleep_source',''),coalesce(n.diagnostics->>'source_selection_reason',''),
			c.capture_completeness,c.duration_assessment,c.finalization_state,c.claim_eligibility,c.observed_at,
			c.window_start,c.window_end_exclusive,c.rule_version,c.eligible_nights,c.history_version
			from ` + s.profileGraphRelation("semantic_sleep_duration_comparison_v1") + ` c
			join completed_night_sleep n using (wake_date,source,source_epoch,algorithm_version,input_hash)
			where c.wake_date=$1`
	}
	err = s.pool.QueryRow(readCtx, query, wakeDate).Scan(
		&fact.Status, &fact.DurationMinutes, &fact.ReferenceMinutes, &fact.DeltaMinutes,
		&fact.Source, &n.SourceEpoch, &n.AlgorithmVersion, &n.InputHash,
		&fact.PreferredSleepSource, &fact.SourceSelectionReason,
		&fact.Capture, &fact.Assessment, &fact.Finalization, &n.ClaimEligibility, &n.ObservedAt,
		&fact.WindowStart, &fact.WindowEndExclusive, &fact.RuleVersion, &fact.EligibleNights, &historyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		fact.Status = health.SleepComparisonNoNight
	} else if err != nil {
		unavailable := health.NewSleepDurationComparison(date)
		return &unavailable
	} else {
		observed := n.ObservedAt.UTC()
		fact.ObservedAt = &observed
	}
	payload, _ := json.Marshal([]any{fact, n.SourceEpoch, n.AlgorithmVersion, n.InputHash, n.ClaimEligibility, historyVersion})
	hash := sha256.Sum256(payload)
	fact.FactVersion = hex.EncodeToString(hash[:])
	return &fact
}

var sleepComparisonQuery = `
	select status,duration_minutes,reference_minutes,delta_minutes,
	       n.source,n.source_epoch,n.algorithm_version,n.input_hash,
	       coalesce(n.diagnostics->>'preferred_sleep_source',''),coalesce(n.diagnostics->>'source_selection_reason',''),
	       n.capture_completeness,n.duration_assessment,n.finalization_state,n.claim_eligibility,n.observed_at,
	       r.window_start,r.window_end_exclusive,r.rule_version,r.eligible_nights,r.history_version
	  from completed_night_sleep n
	  join sleep_duration_reference_v1 r using (wake_date,source,source_epoch,algorithm_version)
	` + dbtmetrics.SleepDurationComparisonFieldsSQL() + `
	 where n.wake_date=$1`

func (s *DB) ensureSleepDurationReference(ctx context.Context) error {
	// The provisioning/migration connection already has the tenant search path.
	_, err := s.pool.Exec(ctx, `CREATE OR REPLACE VIEW sleep_duration_reference_v1 WITH (security_invoker=true) AS `+
		dbtmetrics.SleepDurationReferenceSQL("completed_night_sleep"))
	return err
}

// A catalog fingerprint verifies PostgreSQL's parsed definition without DDL
// at runtime. The creating schema must own both relations and no other role
// may hold an explicit grant on the view.
func (s *DB) verifySleepDurationReference(ctx context.Context) error {
	var definition string
	var isolated bool
	err := s.pool.QueryRow(ctx, `
		select pg_get_viewdef(v.oid, false),
		       v.relowner=t.relowner and v.relowner=n.nspowner
		       and coalesce(v.reloptions = array['security_invoker=true'],false)
		       and not exists (
		         select from aclexplode(coalesce(v.relacl,acldefault('r',v.relowner))) a
		          where a.grantee <> v.relowner
		       )
		  from pg_namespace n
		  join pg_class v on v.relnamespace=n.oid and v.relname='sleep_duration_reference_v1' and v.relkind='v'
		  join pg_class t on t.relnamespace=n.oid and t.relname='completed_night_sleep'
		 where n.nspname=current_schema()`).Scan(&definition, &isolated)
	if err != nil {
		return fmt.Errorf("verify sleep duration reference: %w", err)
	}
	hash := sha256.Sum256([]byte(definition))
	if !isolated || hex.EncodeToString(hash[:]) != sleepReferenceCatalogFingerprint {
		return &SchemaContractMismatchError{Reason: "sleep duration reference definition, owner or grants differ"}
	}
	return nil
}

// PostgreSQL 16 and 17 produce the same fingerprint for the checked model.
const sleepReferenceCatalogFingerprint = "037e3dc02f3d5a6efb348e1a7efefb973c8da21522fc80ca5702235b62cebfbc"
