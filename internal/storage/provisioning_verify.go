package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VerifyTenantIsolation performs narrow denial probes used when validating a
// newly opened restricted tenant pool. It exposes no general query facility.
func (s *DB) VerifyTenantIsolation(ctx context.Context, forbiddenSchemas ...string) error {
	probes := []string{`SELECT count(*) FROM health_registry.users`}
	for _, schema := range forbiddenSchemas {
		probes = append(probes, "SELECT count(*) FROM "+pgx.Identifier{schema, "isolation_probe"}.Sanitize())
	}
	for _, probe := range probes {
		_, err := s.pool.Exec(ctx, probe)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			return fmt.Errorf("tenant isolation denial probe did not fail with SQLSTATE 42501: %v", err)
		}
	}
	return nil
}

// VerifyProvisionedSchema is the fail-closed catalog contract used before a
// tenant is activated. Startup's compatibility wrappers may log DDL errors;
// provisioning must observe the resulting incompleteness explicitly.
func (s *DB) VerifyProvisionedSchema() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tables := []string{"health_records", "metric_points", "import_runs", "import_run_coverage", "import_stage_points", "import_stage_workouts", "minute_metrics", "hourly_metrics", "daily_scores", "settings", "notification_deliveries", "workouts", "ai_briefings", "ai_briefing_blocks", "energy_snapshots", "source_epochs", "target_snapshots", "feature_snapshots", "naive_baselines", "chip_calibrations", "subjective_checkins", "context_prompt_interactions", "auth_sessions"}
	indexes := []string{"idx_auth_sessions_expires", "idx_chip_calibrations_sub_kind", "idx_context_prompt_one_sent_per_day", "idx_context_prompt_status_expires", "idx_energy_snapshots_date", "idx_energy_snapshots_flags", "idx_energy_snapshots_ts", "idx_feature_snapshots_sub_date", "idx_hourly_date", "idx_hourly_metric_date", "idx_import_stage_points_coverage", "idx_import_stage_points_dedup", "idx_import_stage_workouts_dedup", "idx_import_stage_workouts_synthetic", "idx_naive_baselines_sub_kind_base_date", "idx_points_date", "idx_points_metric_date", "idx_points_quality_metric", "idx_source_epochs_active", "idx_target_snapshots_source_epoch", "idx_target_snapshots_sub_kind_date", "idx_workouts_name", "idx_workouts_start_time", "uq_source_epochs_kind_start"}
	var missing []string
	for _, name := range tables {
		var ok bool
		if err := s.pool.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.'||$1) IS NOT NULL`, name).Scan(&ok); err != nil {
			return fmt.Errorf("verify table %s: %w", name, err)
		}
		if !ok {
			missing = append(missing, "table:"+name)
		}
	}
	for _, name := range indexes {
		var ok bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1)`, name).Scan(&ok); err != nil {
			return fmt.Errorf("verify index %s: %w", name, err)
		}
		if !ok {
			missing = append(missing, "index:"+name)
		}
	}
	columns := map[string][]string{"health_records": {"processing_status", "processing_kind", "processing_error", "processed_at"}, "metric_points": {"quality", "origin", "import_run_id", "source_snapshot_at"}, "workouts": {"origin", "import_run_id", "source_snapshot_at"}, "daily_scores": {"energy_capacity", "energy_eod_current", "energy_drain", "energy_verdict", "baseline_hr_overnight", "sustained_hr_load", "stress_flags", "sleep_unspecified"}}
	for table, names := range columns {
		for _, name := range names {
			var ok bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2)`, table, name).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				missing = append(missing, "column:"+table+"."+name)
			}
		}
	}
	if err := s.VerifyReadinessRedesignSchema(); err != nil {
		return err
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("incomplete provisioned tenant schema: %v", missing)
	}
	return nil
}
