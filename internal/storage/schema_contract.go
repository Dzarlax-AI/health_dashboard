package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SchemaContractVersion is bumped whenever the declared tenant schema
// contract changes. Existing tenants are not current until both the permanent
// marker and registry metadata carry this version and checksum.
const SchemaContractVersion = 6

// TenantIdentityTable is the permanent marker shared by clean provisioning
// and existing-tenant migrations. The provisioning marker is intentionally
// separate and transient.
const TenantIdentityTable = "__tenant_identity"

// ContractManifest is the catalog surface required before a tenant may be
// activated. Callers receive defensive copies from SchemaContractManifest.
type ContractManifest struct {
	Tables            []string
	Indexes           []string
	IndexDefinitions  []IndexDefinition
	Columns           map[string][]string
	ColumnDefinitions []ColumnDefinition
	Constraints       []ConstraintDefinition
	RequiredRows      []RequiredRow
}

// IndexDefinition is the semantic portion of CREATE INDEX that affects query
// behavior. Expressions and predicates use PostgreSQL's canonical catalog
// form after whitespace, identifier quoting, and text casts are normalized.
type IndexDefinition struct {
	Name         string
	Table        string
	Unique       bool
	AccessMethod string
	Keys         []string
	Predicate    string
}

type ColumnDefinition struct {
	Table          string
	Column         string
	DataType       string
	UDTName        string
	Nullable       bool
	Default        string
	RequireDefault bool
}

type ConstraintDefinition struct {
	Table   string
	Kind    string
	Columns []string
}

type RequiredRow struct {
	Table  string
	Values map[string]string
}

// TenantIdentity is the durable per-schema identity and applied contract.
type TenantIdentity struct {
	TenantID               uuid.UUID
	OperationID            uuid.UUID
	SchemaContractVersion  int
	SchemaContractChecksum string
}

type SchemaContractState struct {
	Version  *int
	Checksum *string
}

type SchemaContractMismatchError struct {
	Reason string
	Cause  error
}

func (e *SchemaContractMismatchError) Error() string {
	if e.Reason == "" {
		return "tenant schema contract mismatch"
	}
	return "tenant schema contract mismatch: " + e.Reason
}
func (e *SchemaContractMismatchError) Unwrap() error { return e.Cause }

// ContractCatalog is implemented by pgx pools and transactions. It lets the
// same permanent marker writer serve restricted clean installs and admin-led
// upgrades without duplicating its compatibility rules.
type ContractCatalog interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var schemaContract = ContractManifest{
	Tables:  []string{"health_records", "metric_points", "import_runs", "import_run_coverage", "import_stage_points", "import_stage_workouts", "minute_metrics", "hourly_metrics", "daily_scores", "settings", "notification_deliveries", "workouts", "ai_briefings", "ai_briefing_blocks", "energy_snapshots", "source_epochs", "target_snapshots", "feature_snapshots", "naive_baselines", "chip_calibrations", "subjective_checkins", "context_prompt_interactions", "derived_metrics", "derived_metric_feedback", "auth_sessions"},
	Indexes: []string{"idx_auth_sessions_expires", "idx_chip_calibrations_sub_kind", "idx_context_prompt_one_sent_per_day", "idx_context_prompt_status_expires", "idx_energy_snapshots_date", "idx_energy_snapshots_flags", "idx_energy_snapshots_ts", "idx_feature_snapshots_sub_date", "idx_health_records_completed_processed_at", "idx_hourly_date", "idx_hourly_metric_date", "idx_import_stage_points_coverage", "idx_import_stage_points_dedup", "idx_import_stage_workouts_dedup", "idx_import_stage_workouts_synthetic", "idx_naive_baselines_sub_kind_base_date", "idx_points_date", "idx_points_metric_date", "idx_points_quality_metric", "idx_source_epochs_active", "idx_target_snapshots_source_epoch", "idx_target_snapshots_sub_kind_date", "idx_workouts_name", "idx_workouts_start_time", "uq_source_epochs_kind_start"},
	IndexDefinitions: []IndexDefinition{
		{Name: "idx_auth_sessions_expires", Table: "auth_sessions", AccessMethod: "btree", Keys: []string{"expires_at"}},
		{Name: "idx_chip_calibrations_sub_kind", Table: "chip_calibrations", AccessMethod: "btree", Keys: []string{"sub_score", "target_kind", "computed_at desc"}},
		{Name: "idx_context_prompt_one_sent_per_day", Table: "context_prompt_interactions", Unique: true, AccessMethod: "btree", Keys: []string{"prompt_local_date"}, Predicate: "status = any (array['reserved','prompted','answered','skipped','expired','send_failed'])"},
		{Name: "idx_context_prompt_status_expires", Table: "context_prompt_interactions", AccessMethod: "btree", Keys: []string{"status", "expires_at"}},
		{Name: "idx_energy_snapshots_date", Table: "energy_snapshots", AccessMethod: "btree", Keys: []string{"date desc"}},
		{Name: "idx_energy_snapshots_flags", Table: "energy_snapshots", AccessMethod: "gin", Keys: []string{"flags"}},
		{Name: "idx_energy_snapshots_ts", Table: "energy_snapshots", AccessMethod: "btree", Keys: []string{"ts_bucket desc"}},
		{Name: "idx_feature_snapshots_sub_date", Table: "feature_snapshots", AccessMethod: "btree", Keys: []string{"sub_score", "date desc"}},
		{Name: "idx_health_records_completed_processed_at", Table: "health_records", AccessMethod: "btree", Keys: []string{"processed_at desc"}, Predicate: "processing_status = 'complete' and processed_at is not null"},
		{Name: "idx_hourly_date", Table: "hourly_metrics", AccessMethod: "btree", Keys: []string{"substring(hour,1,10)"}},
		{Name: "idx_hourly_metric_date", Table: "hourly_metrics", AccessMethod: "btree", Keys: []string{"metric_name", "substring(hour,1,10)"}},
		{Name: "idx_import_stage_points_coverage", Table: "import_stage_points", AccessMethod: "btree", Keys: []string{"import_run_id", "metric_name", "source", "local_date"}},
		{Name: "idx_import_stage_points_dedup", Table: "import_stage_points", AccessMethod: "btree", Keys: []string{"import_run_id", "metric_name", "date", "source", "staged_seq desc"}},
		{Name: "idx_import_stage_workouts_dedup", Table: "import_stage_workouts", AccessMethod: "btree", Keys: []string{"import_run_id", "external_id", "staged_seq desc"}},
		{Name: "idx_import_stage_workouts_synthetic", Table: "import_stage_workouts", AccessMethod: "btree", Keys: []string{"import_run_id", "name", "start_time", "end_time"}},
		{Name: "idx_naive_baselines_sub_kind_base_date", Table: "naive_baselines", AccessMethod: "btree", Keys: []string{"sub_score", "target_kind", "baseline_kind", "date desc"}},
		{Name: "idx_points_date", Table: "metric_points", AccessMethod: "btree", Keys: []string{"substring(date,1,10)"}},
		{Name: "idx_points_metric_date", Table: "metric_points", AccessMethod: "btree", Keys: []string{"metric_name", "substring(date,1,10)"}},
		{Name: "idx_points_quality_metric", Table: "metric_points", AccessMethod: "btree", Keys: []string{"metric_name", "substring(date,1,10)"}, Predicate: "quality = 'ok'"},
		{Name: "idx_source_epochs_active", Table: "source_epochs", AccessMethod: "btree", Keys: []string{"kind", "start_date desc"}, Predicate: "end_date is null"},
		{Name: "idx_target_snapshots_source_epoch", Table: "target_snapshots", AccessMethod: "btree", Keys: []string{"source_epoch"}},
		{Name: "idx_target_snapshots_sub_kind_date", Table: "target_snapshots", AccessMethod: "btree", Keys: []string{"sub_score", "target_kind", "date desc"}},
		{Name: "idx_workouts_name", Table: "workouts", AccessMethod: "btree", Keys: []string{"name"}},
		{Name: "idx_workouts_start_time", Table: "workouts", AccessMethod: "btree", Keys: []string{"start_time desc"}},
		{Name: "uq_source_epochs_kind_start", Table: "source_epochs", Unique: true, AccessMethod: "btree", Keys: []string{"kind", "start_date"}},
	},
	Columns: map[string][]string{
		"health_records":    {"processing_status", "processing_kind", "processing_error", "processed_at"},
		"import_runs":       {"heartbeat_at", "lease_token"},
		"metric_points":     {"quality", "origin", "import_run_id", "source_snapshot_at"},
		"workouts":          {"origin", "import_run_id", "source_snapshot_at"},
		"daily_scores":      {"energy_capacity", "energy_eod_current", "energy_drain", "energy_verdict", "baseline_hr_overnight", "sustained_hr_load", "stress_flags", "sleep_unspecified"},
		"naive_baselines":   {"reason"},
		"chip_calibrations": {"cutoff", "p80", "base_rate", "status", "method"},
	},
	ColumnDefinitions: runtimeContractColumns(),
	Constraints: []ConstraintDefinition{
		{Table: "health_records", Kind: "p", Columns: []string{"id"}},
		{Table: "metric_points", Kind: "p", Columns: []string{"id"}},
		{Table: "metric_points", Kind: "u", Columns: []string{"metric_name", "date", "source"}},
		{Table: "import_runs", Kind: "p", Columns: []string{"id"}},
		{Table: "import_run_coverage", Kind: "p", Columns: []string{"import_run_id", "metric_name", "source", "local_date"}},
		{Table: "import_stage_points", Kind: "p", Columns: []string{"staged_seq"}},
		{Table: "import_stage_workouts", Kind: "p", Columns: []string{"staged_seq"}},
		{Table: "minute_metrics", Kind: "p", Columns: []string{"metric_name", "minute", "source"}},
		{Table: "hourly_metrics", Kind: "p", Columns: []string{"metric_name", "hour", "source"}},
		{Table: "daily_scores", Kind: "p", Columns: []string{"date"}},
		{Table: "settings", Kind: "p", Columns: []string{"key"}},
		{Table: "notification_deliveries", Kind: "p", Columns: []string{"delivery_key"}},
		{Table: "workouts", Kind: "p", Columns: []string{"id"}},
		{Table: "workouts", Kind: "u", Columns: []string{"external_id"}},
		{Table: "ai_briefings", Kind: "p", Columns: []string{"date"}},
		{Table: "ai_briefing_blocks", Kind: "p", Columns: []string{"date", "lang", "block"}},
		{Table: "energy_snapshots", Kind: "p", Columns: []string{"ts_bucket"}},
		{Table: "source_epochs", Kind: "p", Columns: []string{"epoch_id"}},
		{Table: "target_snapshots", Kind: "p", Columns: []string{"date", "sub_score", "target_kind"}},
		{Table: "feature_snapshots", Kind: "p", Columns: []string{"date", "sub_score"}},
		{Table: "naive_baselines", Kind: "p", Columns: []string{"date", "sub_score", "target_kind", "baseline_kind"}},
		{Table: "chip_calibrations", Kind: "p", Columns: []string{"sub_score", "target_kind", "source_epoch"}},
		{Table: "subjective_checkins", Kind: "p", Columns: []string{"date", "source"}},
		{Table: "context_prompt_interactions", Kind: "p", Columns: []string{"prompt_id"}},
		{Table: "context_prompt_interactions", Kind: "u", Columns: []string{"signal_date", "detected_reason"}},
		{Table: "derived_metrics", Kind: "p", Columns: []string{"metric_name", "metric_date"}},
		{Table: "derived_metric_feedback", Kind: "p", Columns: []string{"metric_name", "metric_date", "channel"}},
		{Table: "auth_sessions", Kind: "p", Columns: []string{"id_hash"}},
	},
	RequiredRows: []RequiredRow{{Table: "source_epochs", Values: map[string]string{
		"epoch_id": InitialSourceEpoch, "start_date": "2014-01-01",
		"kind": SourceEpochKindIngest, "confirmed": "true",
	}}},
}

// SchemaContractManifest returns the exact application objects verified by
// VerifySchemaContract. The permanent tenant marker is verified separately.
func SchemaContractManifest() ContractManifest {
	out := ContractManifest{
		Tables:            append([]string(nil), schemaContract.Tables...),
		Indexes:           append([]string(nil), schemaContract.Indexes...),
		IndexDefinitions:  append([]IndexDefinition(nil), schemaContract.IndexDefinitions...),
		Columns:           make(map[string][]string, len(schemaContract.Columns)),
		ColumnDefinitions: append([]ColumnDefinition(nil), schemaContract.ColumnDefinitions...),
		Constraints:       append([]ConstraintDefinition(nil), schemaContract.Constraints...),
		RequiredRows:      append([]RequiredRow(nil), schemaContract.RequiredRows...),
	}
	for i := range out.IndexDefinitions {
		out.IndexDefinitions[i].Keys = append([]string(nil), out.IndexDefinitions[i].Keys...)
	}
	for table, columns := range schemaContract.Columns {
		out.Columns[table] = append([]string(nil), columns...)
	}
	for i := range out.Constraints {
		out.Constraints[i].Columns = append([]string(nil), out.Constraints[i].Columns...)
	}
	for i := range out.RequiredRows {
		out.RequiredRows[i].Values = make(map[string]string, len(schemaContract.RequiredRows[i].Values))
		for column, value := range schemaContract.RequiredRows[i].Values {
			out.RequiredRows[i].Values[column] = value
		}
	}
	return out
}

// SchemaContractChecksum is a deterministic SHA-256 over the declared
// catalog manifest. It does not inspect or hash tenant data.
func SchemaContractChecksum() string {
	manifest := SchemaContractManifest()
	sort.Strings(manifest.Tables)
	sort.Strings(manifest.Indexes)
	var canonical strings.Builder
	for _, table := range manifest.Tables {
		fmt.Fprintf(&canonical, "table:%s:relkind=ordinary-or-partitioned-table\n", table)
	}
	sort.Slice(manifest.IndexDefinitions, func(i, j int) bool { return manifest.IndexDefinitions[i].Name < manifest.IndexDefinitions[j].Name })
	for _, index := range manifest.IndexDefinitions {
		fmt.Fprintf(&canonical, "index:%s:table=%s:unique=%t:method=%s:keys=%s:predicate=%s\n", index.Name, index.Table, index.Unique, index.AccessMethod, strings.Join(index.Keys, ","), index.Predicate)
	}
	tables := make([]string, 0, len(manifest.Columns))
	for table := range manifest.Columns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		columns := append([]string(nil), manifest.Columns[table]...)
		sort.Strings(columns)
		for _, column := range columns {
			fmt.Fprintf(&canonical, "column:%s.%s\n", table, column)
		}
	}
	sort.Slice(manifest.ColumnDefinitions, func(i, j int) bool {
		a, b := manifest.ColumnDefinitions[i], manifest.ColumnDefinitions[j]
		return a.Table+"."+a.Column < b.Table+"."+b.Column
	})
	for _, definition := range manifest.ColumnDefinitions {
		defaultValue := ""
		if definition.RequireDefault {
			defaultValue = canonicalCatalogExpression(definition.Default)
		}
		fmt.Fprintf(&canonical, "column-definition:%s.%s:%s:udt=%s:nullable=%t:require-default=%t:default=%s\n", definition.Table, definition.Column, definition.DataType, definition.UDTName, definition.Nullable, definition.RequireDefault, defaultValue)
	}
	sort.Slice(manifest.Constraints, func(i, j int) bool {
		a, b := manifest.Constraints[i], manifest.Constraints[j]
		return a.Table+":"+a.Kind+":"+strings.Join(a.Columns, ",") < b.Table+":"+b.Kind+":"+strings.Join(b.Columns, ",")
	})
	for _, constraint := range manifest.Constraints {
		fmt.Fprintf(&canonical, "constraint:%s:kind=%s:columns=%s\n", constraint.Table, constraint.Kind, strings.Join(constraint.Columns, ","))
	}
	sort.Slice(manifest.RequiredRows, func(i, j int) bool {
		return manifest.RequiredRows[i].Table < manifest.RequiredRows[j].Table
	})
	for _, row := range manifest.RequiredRows {
		columns := make([]string, 0, len(row.Values))
		for column := range row.Values {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		for _, column := range columns {
			fmt.Fprintf(&canonical, "required-row:%s.%s=%s\n", row.Table, column, row.Values[column])
		}
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

// EnsureTenantIdentityMarker creates or upgrades the permanent marker in the
// named schema. An empty schema selects the connection's current schema.
// Existing immutable identity fields must match before contract metadata is
// advanced.
func EnsureTenantIdentityMarker(ctx context.Context, catalog ContractCatalog, schema string, tenantID, operationID uuid.UUID) error {
	table, err := ensureTenantIdentityMarkerTable(ctx, catalog, schema)
	if err != nil {
		return err
	}
	var currentTenantID, currentOperationID uuid.UUID
	var currentVersion *int
	var currentChecksum *string
	err = catalog.QueryRow(ctx, "SELECT tenant_id,operation_id,schema_contract_version,schema_contract_checksum FROM "+table+" WHERE singleton=true").Scan(&currentTenantID, &currentOperationID, &currentVersion, &currentChecksum)
	switch {
	case err == nil:
		if currentTenantID != tenantID || currentOperationID != operationID {
			return errors.New("permanent tenant identity marker does not match immutable tenant identity")
		}
		switch {
		case currentVersion == nil && currentChecksum == nil:
			tag, updateErr := catalog.Exec(ctx, "UPDATE "+table+" SET schema_contract_version=$3,schema_contract_checksum=$4 WHERE singleton=true AND tenant_id=$1 AND operation_id=$2 AND schema_contract_version IS NULL AND schema_contract_checksum IS NULL", tenantID, operationID, SchemaContractVersion, SchemaContractChecksum())
			if updateErr != nil {
				return fmt.Errorf("advance legacy permanent tenant contract marker: %w", updateErr)
			}
			if tag.RowsAffected() != 1 {
				return errors.New("permanent tenant contract marker compare-and-set conflict")
			}
		case currentVersion != nil && currentChecksum != nil && *currentVersion == SchemaContractVersion && *currentChecksum == SchemaContractChecksum():
		default:
			return errors.New("permanent tenant contract marker has stale, newer, or partial metadata")
		}
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := catalog.Exec(ctx, "INSERT INTO "+table+" (tenant_id,operation_id,schema_contract_version,schema_contract_checksum) VALUES ($1,$2,$3,$4)", tenantID, operationID, SchemaContractVersion, SchemaContractChecksum()); err != nil {
			return fmt.Errorf("insert permanent tenant identity marker: %w", err)
		}
	default:
		return fmt.Errorf("read permanent tenant identity marker: %w", err)
	}
	return enforceTenantIdentityContractNotNull(ctx, catalog, table)
}

func ensureTenantIdentityMarkerTable(ctx context.Context, catalog ContractCatalog, schema string) (string, error) {
	table := pgx.Identifier{TenantIdentityTable}.Sanitize()
	if schema != "" {
		table = pgx.Identifier{schema, TenantIdentityTable}.Sanitize()
	}
	if _, err := catalog.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+` (
		singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
		tenant_id uuid NOT NULL,
		operation_id uuid NOT NULL,
		schema_contract_version integer NOT NULL,
		schema_contract_checksum text NOT NULL
	)`); err != nil {
		return "", fmt.Errorf("create permanent tenant identity marker: %w", err)
	}
	for _, definition := range []string{
		"schema_contract_version integer",
		"schema_contract_checksum text",
	} {
		if _, err := catalog.Exec(ctx, "ALTER TABLE "+table+" ADD COLUMN IF NOT EXISTS "+definition); err != nil {
			return "", fmt.Errorf("upgrade permanent tenant identity marker: %w", err)
		}
	}
	return table, nil
}

func enforceTenantIdentityContractNotNull(ctx context.Context, catalog ContractCatalog, table string) error {
	for _, column := range []string{"schema_contract_version", "schema_contract_checksum"} {
		if _, err := catalog.Exec(ctx, "ALTER TABLE "+table+" ALTER COLUMN "+column+" SET NOT NULL"); err != nil {
			return fmt.Errorf("enforce permanent tenant contract marker: %w", err)
		}
	}
	return nil
}

// MigrateTenantIdentityMarker is the explicit migration-only marker CAS.
func MigrateTenantIdentityMarker(ctx context.Context, catalog ContractCatalog, schema string, tenantID, operationID uuid.UUID, expected SchemaContractState) error {
	if err := ValidateSchemaContractTransition(expected.Version, expected.Checksum, SchemaContractVersion, SchemaContractChecksum()); err != nil {
		return err
	}
	table, err := ensureTenantIdentityMarkerTable(ctx, catalog, schema)
	if err != nil {
		return err
	}
	var markerTenant, markerOperation uuid.UUID
	var version *int
	var checksum *string
	err = catalog.QueryRow(ctx, "SELECT tenant_id,operation_id,schema_contract_version,schema_contract_checksum FROM "+table+" WHERE singleton=true").Scan(&markerTenant, &markerOperation, &version, &checksum)
	if errors.Is(err, pgx.ErrNoRows) {
		if expected.Version != nil || expected.Checksum != nil {
			return errors.New("missing marker does not match expected old contract")
		}
		_, err = catalog.Exec(ctx, "INSERT INTO "+table+" (tenant_id,operation_id,schema_contract_version,schema_contract_checksum) VALUES($1,$2,$3,$4)", tenantID, operationID, SchemaContractVersion, SchemaContractChecksum())
		if err != nil {
			if confirmErr := confirmLandedTenantIdentityContract(ctx, catalog, table, tenantID, operationID); confirmErr == nil {
				return nil
			} else {
				return errors.Join(err, confirmErr)
			}
		}
		return enforceTenantIdentityContractNotNull(ctx, catalog, table)
	}
	if err != nil {
		return err
	}
	if markerTenant != tenantID || markerOperation != operationID {
		return errors.New("permanent tenant identity marker does not match immutable tenant identity")
	}
	if version != nil && checksum != nil && *version == SchemaContractVersion && *checksum == SchemaContractChecksum() {
		return enforceTenantIdentityContractNotNull(ctx, catalog, table)
	}
	if !sameContractState(version, checksum, expected.Version, expected.Checksum) {
		return errors.New("marker does not match expected old contract")
	}
	tag, err := catalog.Exec(ctx, "UPDATE "+table+" SET schema_contract_version=$5,schema_contract_checksum=$6 WHERE singleton=true AND tenant_id=$1 AND operation_id=$2 AND schema_contract_version IS NOT DISTINCT FROM $3 AND schema_contract_checksum IS NOT DISTINCT FROM $4", tenantID, operationID, expected.Version, expected.Checksum, SchemaContractVersion, SchemaContractChecksum())
	if err != nil {
		if confirmErr := confirmLandedTenantIdentityContract(ctx, catalog, table, tenantID, operationID); confirmErr == nil {
			return nil
		} else {
			return errors.Join(err, confirmErr)
		}
	}
	if tag.RowsAffected() != 1 {
		if confirmErr := confirmLandedTenantIdentityContract(ctx, catalog, table, tenantID, operationID); confirmErr == nil {
			return nil
		} else {
			return errors.Join(errors.New("permanent tenant contract migration compare-and-set conflict"), confirmErr)
		}
	}
	return enforceTenantIdentityContractNotNull(ctx, catalog, table)
}

func confirmLandedTenantIdentityContract(ctx context.Context, catalog ContractCatalog, table string, tenantID, operationID uuid.UUID) error {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var landedTenant, landedOperation uuid.UUID
	var landedVersion *int
	var landedChecksum *string
	readErr := catalog.QueryRow(readCtx, "SELECT tenant_id,operation_id,schema_contract_version,schema_contract_checksum FROM "+table+" WHERE singleton=true").Scan(&landedTenant, &landedOperation, &landedVersion, &landedChecksum)
	if readErr != nil {
		return fmt.Errorf("re-read permanent tenant contract after ambiguous migration: %w", readErr)
	}
	if landedTenant != tenantID || landedOperation != operationID || landedVersion == nil || landedChecksum == nil || *landedVersion != SchemaContractVersion || *landedChecksum != SchemaContractChecksum() {
		return errors.New("permanent tenant contract migration did not land exact target state")
	}
	return enforceTenantIdentityContractNotNull(readCtx, catalog, table)
}

// RestoreTenantIdentityMarkerContract rolls a marker back from the compiled
// contract to the exact state captured before cutover. The operation is an
// idempotent CAS so callers can coordinate it with separately-authorized
// registry and catalog phases without claiming cross-identity atomicity.
func RestoreTenantIdentityMarkerContract(ctx context.Context, catalog ContractCatalog, schema string, tenantID, operationID uuid.UUID, restore SchemaContractState) error {
	if (restore.Version == nil) != (restore.Checksum == nil) {
		return errors.New("restore schema contract pair is partial")
	}
	table := pgx.Identifier{TenantIdentityTable}.Sanitize()
	if schema != "" {
		table = pgx.Identifier{schema, TenantIdentityTable}.Sanitize()
	}
	if restore.Version == nil {
		for _, column := range []string{"schema_contract_version", "schema_contract_checksum"} {
			if _, err := catalog.Exec(ctx, "ALTER TABLE "+table+" ALTER COLUMN "+column+" DROP NOT NULL"); err != nil {
				return fmt.Errorf("relax permanent tenant contract marker for rollback: %w", err)
			}
		}
	}
	tag, err := catalog.Exec(ctx, "UPDATE "+table+" SET schema_contract_version=$3,schema_contract_checksum=$4 WHERE singleton=true AND tenant_id=$1 AND operation_id=$2 AND schema_contract_version=$5 AND schema_contract_checksum=$6", tenantID, operationID, restore.Version, restore.Checksum, SchemaContractVersion, SchemaContractChecksum())
	if err != nil {
		return fmt.Errorf("restore permanent tenant contract marker: %w", err)
	}
	if tag.RowsAffected() != 1 {
		var currentTenant, currentOperation uuid.UUID
		var currentVersion *int
		var currentChecksum *string
		readErr := catalog.QueryRow(ctx, "SELECT tenant_id,operation_id,schema_contract_version,schema_contract_checksum FROM "+table+" WHERE singleton=true").Scan(&currentTenant, &currentOperation, &currentVersion, &currentChecksum)
		if readErr == nil && currentTenant == tenantID && currentOperation == operationID && sameContractState(currentVersion, currentChecksum, restore.Version, restore.Checksum) {
			return nil
		}
		if readErr != nil {
			return errors.Join(errors.New("permanent tenant contract marker rollback compare-and-set conflict"), readErr)
		}
		return errors.New("permanent tenant contract marker rollback compare-and-set conflict")
	}
	return nil
}

func ValidateSchemaContractTransition(expectedVersion *int, expectedChecksum *string, targetVersion int, targetChecksum string) error {
	if (expectedVersion == nil) != (expectedChecksum == nil) {
		return errors.New("expected schema contract pair is partial")
	}
	decodedTarget, targetErr := hex.DecodeString(targetChecksum)
	if targetVersion <= 0 || targetErr != nil || len(decodedTarget) != sha256.Size || strings.ToLower(targetChecksum) != targetChecksum {
		return errors.New("target schema contract is invalid")
	}
	if expectedVersion != nil {
		decodedExpected, expectedErr := hex.DecodeString(*expectedChecksum)
		if *expectedVersion <= 0 || expectedErr != nil || len(decodedExpected) != sha256.Size || strings.ToLower(*expectedChecksum) != *expectedChecksum {
			return errors.New("expected schema contract is invalid")
		}
		if *expectedVersion >= targetVersion {
			if *expectedVersion == targetVersion && *expectedChecksum == targetChecksum {
				return nil
			}
			return errors.New("schema contract downgrade or newer-source migration is forbidden")
		}
	}
	return nil
}

func sameContractState(aVersion *int, aChecksum *string, bVersion *int, bChecksum *string) bool {
	if (aVersion == nil) != (bVersion == nil) || (aChecksum == nil) != (bChecksum == nil) {
		return false
	}
	if aVersion == nil {
		return true
	}
	return *aVersion == *bVersion && *aChecksum == *bChecksum
}

// ReadTenantIdentityMarker reads the permanent marker from the named schema.
func ReadTenantIdentityMarker(ctx context.Context, catalog ContractCatalog, schema string) (TenantIdentity, error) {
	table := pgx.Identifier{TenantIdentityTable}.Sanitize()
	if schema != "" {
		table = pgx.Identifier{schema, TenantIdentityTable}.Sanitize()
	}
	var identity TenantIdentity
	var tenantID, operationID *uuid.UUID
	var version *int
	var checksum *string
	err := catalog.QueryRow(ctx, "SELECT tenant_id,operation_id,schema_contract_version,schema_contract_checksum FROM "+table+" WHERE singleton=true").Scan(&tenantID, &operationID, &version, &checksum)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703")) {
			return identity, &SchemaContractMismatchError{Reason: "permanent tenant identity marker is missing or malformed", Cause: err}
		}
		return identity, fmt.Errorf("read permanent tenant identity: %w", err)
	}
	if tenantID == nil || operationID == nil || version == nil || checksum == nil {
		return identity, &SchemaContractMismatchError{Reason: "permanent tenant identity marker contains null metadata"}
	}
	identity = TenantIdentity{TenantID: *tenantID, OperationID: *operationID, SchemaContractVersion: *version, SchemaContractChecksum: *checksum}
	return identity, nil
}

// EnsureTenantIdentity applies the permanent marker in the current schema.
func (s *DB) EnsureTenantIdentity(ctx context.Context, tenantID, operationID uuid.UUID) error {
	return EnsureTenantIdentityMarker(ctx, s.pool, "", tenantID, operationID)
}

// ReadTenantIdentity reads the permanent marker in the current schema.
func (s *DB) ReadTenantIdentity(ctx context.Context) (TenantIdentity, error) {
	return ReadTenantIdentityMarker(ctx, s.pool, "")
}

// EnsureSchemaContract centralizes the existing additive startup/provisioning
// operations, then verifies the declared catalog. Individual Ensure methods
// remain the source of migration behavior.
func (s *DB) EnsureSchemaContract() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return s.EnsureSchemaContractContext(ctx)
}

// EnsureSchemaContractContext applies startup-safe additive operations and
// verifies the declared contract. It deliberately does not build deployment
// indexes that can block tenant writes; a missing deployment index therefore
// fails closed and requires the stopped-service fleet migration.
func (s *DB) EnsureSchemaContractContext(ctx context.Context) error {
	if err := s.ensureSchemaContractObjectsContext(ctx); err != nil {
		return err
	}
	return s.VerifySchemaContractContext(ctx)
}

// MigrateSchemaContract applies the complete tenant contract while the tenant
// is offline or not yet active.
func (s *DB) MigrateSchemaContract() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return s.MigrateSchemaContractContext(ctx)
}

// MigrateSchemaContractContext is the explicit deployment/provisioning path.
// The caller must guarantee that no tenant traffic is active while regular
// deployment indexes are built.
func (s *DB) MigrateSchemaContractContext(ctx context.Context) error {
	if err := s.ensureSchemaContractObjectsContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureDeploymentIndexesContext(ctx); err != nil {
		return err
	}
	return s.VerifySchemaContractContext(ctx)
}

func (s *DB) ensureSchemaContractObjectsContext(ctx context.Context) error {
	if err := s.EnsureAllTablesContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureIndexesContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureAIBriefingsTableContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureAIBriefingBlocksTableContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureEnergySnapshotsTableContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureReadinessRedesignTablesContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureSubjectiveCheckinsTableContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureContextPromptInteractionsTableContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureDerivedMetricsTablesContext(ctx); err != nil {
		return err
	}
	if err := s.EnsureAuthSessionsTableContext(ctx); err != nil {
		return err
	}
	return nil
}

// VerifySchemaContract checks the manifest compiled into this binary.
func (s *DB) VerifySchemaContract() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.VerifySchemaContractContext(ctx)
}

// VerifySchemaContractContext verifies using the caller's cancellation and
// deadline. Long-running audit paths must use this instead of detaching work
// onto context.Background.
func (s *DB) VerifySchemaContractContext(ctx context.Context) error {
	manifest := SchemaContractManifest()
	var missing []string
	missingSet := make(map[string]struct{})
	addMissing := func(item string) {
		if _, exists := missingSet[item]; exists {
			return
		}
		missingSet[item] = struct{}{}
		missing = append(missing, item)
	}
	for _, name := range manifest.Tables {
		var relkind string
		if err := s.pool.QueryRow(ctx, `
			SELECT c.relkind::text
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid=c.relnamespace
			 WHERE n.nspname=current_schema() AND c.relname=$1`, name).Scan(&relkind); errors.Is(err, pgx.ErrNoRows) {
			addMissing("table:" + name)
			continue
		} else if err != nil {
			return fmt.Errorf("verify table %s: %w", name, err)
		}
		if relkind != "r" && relkind != "p" {
			return &SchemaContractMismatchError{Reason: "required table relation kind differs"}
		}
	}
	for _, expected := range manifest.IndexDefinitions {
		var table, method, predicate string
		var unique, valid, ready bool
		var keys []string
		err := s.pool.QueryRow(ctx, `
			SELECT tbl.relname,
			       idx.indisunique,
			       idx.indisvalid,
			       idx.indisready,
			       am.amname,
			       ARRAY(
			           SELECT pg_get_indexdef(idx.indexrelid, key_no, true)
			                  || CASE
			                       WHEN (idx.indoption[key_no - 1] & 1) = 1 THEN
			                           ' desc' || CASE WHEN (idx.indoption[key_no - 1] & 2) = 0 THEN ' nulls last' ELSE '' END
			                       WHEN (idx.indoption[key_no - 1] & 2) = 2 THEN ' nulls first'
			                       ELSE ''
			                     END
			             FROM generate_series(1, idx.indnkeyatts) AS key_no
			            ORDER BY key_no
			       ),
			       COALESCE(pg_get_expr(idx.indpred, idx.indrelid, true), '')
			  FROM pg_class ic
			  JOIN pg_namespace ns ON ns.oid=ic.relnamespace
			  JOIN pg_index idx ON idx.indexrelid=ic.oid
			  JOIN pg_class tbl ON tbl.oid=idx.indrelid
			  JOIN pg_am am ON am.oid=ic.relam
			 WHERE ns.nspname=current_schema() AND ic.relname=$1`, expected.Name).Scan(&table, &unique, &valid, &ready, &method, &keys, &predicate)
		if errors.Is(err, pgx.ErrNoRows) {
			addMissing("index:" + expected.Name)
			continue
		}
		if err != nil {
			return fmt.Errorf("verify index %s: %w", expected.Name, err)
		}
		for i := range keys {
			keys[i] = canonicalCatalogExpression(keys[i])
		}
		if table != expected.Table || unique != expected.Unique || !valid || !ready || method != expected.AccessMethod || !equalStrings(keys, expected.Keys) || canonicalCatalogExpression(predicate) != canonicalCatalogExpression(expected.Predicate) {
			return &SchemaContractMismatchError{Reason: "required index definition differs"}
		}
	}
	for table, names := range manifest.Columns {
		for _, name := range names {
			var ok bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2)`, table, name).Scan(&ok); err != nil {
				return fmt.Errorf("verify column %s.%s: %w", table, name, err)
			}
			if !ok {
				addMissing("column:" + table + "." + name)
			}
		}
	}
	for _, definition := range manifest.ColumnDefinitions {
		var dataType, udtName, isNullable, defaultValue string
		err := s.pool.QueryRow(ctx, `SELECT data_type,udt_name,is_nullable,COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2`, definition.Table, definition.Column).Scan(&dataType, &udtName, &isNullable, &defaultValue)
		if errors.Is(err, pgx.ErrNoRows) {
			addMissing("column:" + definition.Table + "." + definition.Column)
			continue
		}
		if err != nil {
			return fmt.Errorf("verify column definition %s.%s: %w", definition.Table, definition.Column, err)
		}
		if dataType != definition.DataType || (definition.UDTName != "" && udtName != definition.UDTName) || (isNullable == "YES") != definition.Nullable || (definition.RequireDefault && canonicalCatalogExpression(defaultValue) != canonicalCatalogExpression(definition.Default)) {
			return &SchemaContractMismatchError{Reason: "required column definition differs"}
		}
	}
	for _, expected := range manifest.Constraints {
		var present bool
		err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				  FROM pg_constraint con
				  JOIN pg_class tbl ON tbl.oid=con.conrelid
				  JOIN pg_namespace ns ON ns.oid=tbl.relnamespace
				 WHERE ns.nspname=current_schema() AND tbl.relname=$1 AND con.contype=$2
				   AND ARRAY(SELECT att.attname::text FROM unnest(con.conkey) WITH ORDINALITY key(attnum,ord) JOIN pg_attribute att ON att.attrelid=con.conrelid AND att.attnum=key.attnum ORDER BY key.ord)=$3::text[]
			)`, expected.Table, expected.Kind, expected.Columns).Scan(&present)
		if err != nil {
			return fmt.Errorf("verify constraint %s(%s): %w", expected.Table, strings.Join(expected.Columns, ","), err)
		}
		if !present {
			return &SchemaContractMismatchError{Reason: "required primary or unique constraint differs"}
		}
	}
	for _, row := range manifest.RequiredRows {
		var present bool
		columns := make([]string, 0, len(row.Values))
		for column := range row.Values {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		predicates := make([]string, 0, len(columns))
		args := make([]any, 0, len(columns))
		for _, column := range columns {
			if row.Values[column] == "NULL" {
				predicates = append(predicates, pgx.Identifier{column}.Sanitize()+" IS NULL")
				continue
			}
			args = append(args, row.Values[column])
			predicates = append(predicates, pgx.Identifier{column}.Sanitize()+fmt.Sprintf("=$%d", len(args)))
		}
		query := "SELECT EXISTS(SELECT 1 FROM " + pgx.Identifier{row.Table}.Sanitize() + " WHERE " + strings.Join(predicates, " AND ") + ")"
		if err := s.pool.QueryRow(ctx, query, args...).Scan(&present); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703") {
				return &SchemaContractMismatchError{Reason: "required row catalog shape is missing", Cause: err}
			}
			return fmt.Errorf("verify required row %s: %w", row.Table, err)
		}
		if !present {
			return &SchemaContractMismatchError{Reason: "required row is missing"}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &SchemaContractMismatchError{Reason: fmt.Sprintf("incomplete provisioned tenant schema: %v", missing)}
	}
	return nil
}

func canonicalCatalogExpression(value string) string {
	var out strings.Builder
	pendingSpace := false
	writeOutside := func(ch byte) {
		if pendingSpace && out.Len() > 0 {
			current := ch
			previous := out.String()[out.Len()-1]
			if current != ',' && current != ')' && current != ']' && previous != '(' && previous != '[' && previous != ',' {
				out.WriteByte(' ')
			}
		}
		pendingSpace = false
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		out.WriteByte(ch)
	}
	for i := 0; i < len(value); {
		if value[i] == '\'' {
			if pendingSpace {
				writeOutside('\'')
			} else {
				out.WriteByte('\'')
			}
			i++
			for i < len(value) {
				out.WriteByte(value[i])
				if value[i] == '\'' {
					if i+1 < len(value) && value[i+1] == '\'' {
						out.WriteByte(value[i+1])
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			continue
		}
		if value[i] == '"' {
			start := i
			i++
			var identifier strings.Builder
			valid := true
			for i < len(value) {
				if value[i] == '"' {
					if i+1 < len(value) && value[i+1] == '"' {
						identifier.WriteByte('"')
						valid = false
						i += 2
						continue
					}
					i++
					break
				}
				identifier.WriteByte(value[i])
				i++
			}
			name := identifier.String()
			if valid && isCanonicalUnquotedIdentifier(name) {
				for j := 0; j < len(name); j++ {
					writeOutside(name[j])
				}
			} else {
				if pendingSpace {
					writeOutside(value[start])
					out.WriteString(value[start+1 : i])
				} else {
					out.WriteString(value[start:i])
				}
			}
			continue
		}
		if i+6 <= len(value) && strings.EqualFold(value[i:i+6], "::text") {
			i += 6
			continue
		}
		if value[i] == ' ' || value[i] == '\t' || value[i] == '\n' || value[i] == '\r' {
			pendingSpace = true
			i++
			continue
		}
		writeOutside(value[i])
		i++
	}
	value = strings.TrimSpace(out.String())
	for hasSingleOuterParentheses(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func isCanonicalUnquotedIdentifier(value string) bool {
	if value == "" || value != strings.ToLower(value) || !((value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for i := 1; i < len(value); i++ {
		ch := value[i]
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$') {
			return false
		}
	}
	return true
}

func hasSingleOuterParentheses(value string) bool {
	if len(value) < 2 || value[0] != '(' || value[len(value)-1] != ')' {
		return false
	}
	depth := 0
	inLiteral, inIdentifier := false, false
	for i := 0; i < len(value); i++ {
		if inLiteral {
			if value[i] == '\'' {
				if i+1 < len(value) && value[i+1] == '\'' {
					i++
				} else {
					inLiteral = false
				}
			}
			continue
		}
		if inIdentifier {
			if value[i] == '"' {
				if i+1 < len(value) && value[i+1] == '"' {
					i++
				} else {
					inIdentifier = false
				}
			}
			continue
		}
		switch value[i] {
		case '\'':
			inLiteral = true
		case '"':
			inIdentifier = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(value)-1 {
				return false
			}
		}
	}
	return depth == 0
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if canonicalCatalogExpression(got[i]) != canonicalCatalogExpression(want[i]) {
			return false
		}
	}
	return true
}
