package storage

import "testing"

func TestPendingColumnMigrationsSkipsExistingColumns(t *testing.T) {
	migrations := []columnMigration{
		{table: "metric_points", column: "quality", ddl: "alter quality"},
		{table: "daily_scores", column: "stress_flags", ddl: "alter stress flags"},
		{table: "daily_scores", column: "baseline_hr_overnight", ddl: "alter baseline"},
	}
	existing := map[columnRef]bool{
		{table: "metric_points", column: "quality"}:              true,
		{table: "daily_scores", column: "baseline_hr_overnight"}: true,
	}

	pending := pendingColumnMigrations(migrations, existing)

	if len(pending) != 1 {
		t.Fatalf("len(pending) = %d, want 1", len(pending))
	}
	if pending[0].table != "daily_scores" || pending[0].column != "stress_flags" {
		t.Fatalf("pending[0] = %s.%s, want daily_scores.stress_flags", pending[0].table, pending[0].column)
	}
}

func TestPendingIndexMigrationsSkipsExistingIndexes(t *testing.T) {
	indexes := []indexMigration{
		{name: "idx_points_quality_metric", ddl: "create quality index"},
		{name: "idx_hourly_date", ddl: "create hourly date index"},
		{name: "idx_points_metric_date", ddl: "create points metric date index"},
	}
	existing := map[string]bool{
		"idx_points_quality_metric": true,
		"idx_points_metric_date":    true,
	}

	pending := pendingIndexMigrations(indexes, existing)

	if len(pending) != 1 {
		t.Fatalf("len(pending) = %d, want 1", len(pending))
	}
	if pending[0].name != "idx_hourly_date" {
		t.Fatalf("pending[0].name = %q, want idx_hourly_date", pending[0].name)
	}
}

func TestEnsureIndexesIntegration_CreatesAndSkipsTenantDDL(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	requiredColumns := []columnRef{
		{table: "metric_points", column: "quality"},
		{table: "daily_scores", column: "energy_capacity"},
		{table: "daily_scores", column: "energy_eod_current"},
		{table: "daily_scores", column: "energy_drain"},
		{table: "daily_scores", column: "energy_verdict"},
		{table: "daily_scores", column: "baseline_hr_overnight"},
		{table: "daily_scores", column: "sustained_hr_load"},
		{table: "daily_scores", column: "stress_flags"},
		{table: "daily_scores", column: "sleep_unspecified"},
	}
	requiredIndexes := []indexMigration{
		{name: "idx_points_quality_metric"},
		{name: "idx_hourly_date"},
		{name: "idx_hourly_metric_date"},
		{name: "idx_points_date"},
		{name: "idx_points_metric_date"},
	}

	// First run: should create any missing startup DDL for this tenant schema.
	db.EnsureIndexes()

	columns, err := db.existingColumns(columnMigrationsFromRefs(requiredColumns))
	if err != nil {
		t.Fatalf("existingColumns: %v", err)
	}
	for _, ref := range requiredColumns {
		if !columns[ref] {
			t.Fatalf("required column missing after EnsureIndexes: %s.%s", ref.table, ref.column)
		}
	}

	indexes, err := db.existingIndexes(requiredIndexes)
	if err != nil {
		t.Fatalf("existingIndexes: %v", err)
	}
	for _, index := range requiredIndexes {
		if !indexes[index.name] {
			t.Fatalf("required index missing after EnsureIndexes: %s", index.name)
		}
	}

	// Run a second time against the already-current tenant schema. The
	// catalog precheck should skip all idempotent DDL and still return cleanly.
	db.EnsureIndexes()
}

func columnMigrationsFromRefs(refs []columnRef) []columnMigration {
	migrations := make([]columnMigration, 0, len(refs))
	for _, ref := range refs {
		migrations = append(migrations, columnMigration{table: ref.table, column: ref.column})
	}
	return migrations
}
