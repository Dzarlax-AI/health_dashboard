package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestVerifySchemaContractContextHonorsCancellation(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.VerifySchemaContractContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification error=%v", err)
	}
}

func TestEnsureSchemaContractContextHonorsCancellation(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.EnsureSchemaContractContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ensure error=%v", err)
	}
}

func TestEnsureSchemaContractIgnoresImportHousekeepingFailure(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `
		CREATE FUNCTION reject_stage_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'cleanup blocked by fixture'; END $$;
		CREATE TRIGGER reject_stage_cleanup BEFORE DELETE ON import_stage_points
		FOR EACH STATEMENT EXECUTE FUNCTION reject_stage_cleanup();
		INSERT INTO import_runs(origin,source_name,started_at,snapshot_at,status,heartbeat_at)
		VALUES('fixture','fixture',NOW()-INTERVAL '3 days',NOW()-INTERVAL '3 days','running',NOW()-INTERVAL '3 days')`); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchemaContractContext(ctx); err != nil {
		t.Fatalf("contract migration was coupled to best-effort import cleanup: %v", err)
	}
}

func TestVerifySchemaContractRejectsViewSpoofingRequiredTable(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	defer func() {
		ctx := context.Background()
		_, _ = db.pool.Exec(ctx, `DROP VIEW IF EXISTS auth_sessions`)
		if err := db.EnsureAuthSessionsTableContext(ctx); err != nil {
			t.Errorf("restore auth_sessions: %v", err)
		}
	}()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `DROP TABLE auth_sessions CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `CREATE VIEW auth_sessions AS SELECT ''::text AS id_hash, NOW() AS created_at, NOW() AS expires_at, NULL::timestamptz AS last_seen_at`); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "relation kind differs") {
		t.Fatalf("view spoof verification error=%v", err)
	}
}

func TestVerifySchemaContractRejectsWrongIndexDefinition(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	defer func() {
		ctx := context.Background()
		_, _ = db.pool.Exec(ctx, `DROP INDEX IF EXISTS idx_auth_sessions_expires`)
		if err := db.EnsureAuthSessionsTableContext(ctx); err != nil {
			t.Errorf("restore auth_sessions index: %v", err)
		}
	}()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `DROP INDEX idx_auth_sessions_expires`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `CREATE INDEX idx_auth_sessions_expires ON auth_sessions (last_seen_at)`); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "index definition differs") {
		t.Fatalf("wrong-index verification error=%v", err)
	}
}

func TestVerifySchemaContractRejectsWrongPartialIndexLiteralCase(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	defer func() {
		ctx := context.Background()
		_, _ = db.pool.Exec(ctx, `DROP INDEX IF EXISTS idx_points_quality_metric`)
		if err := db.EnsureIndexesContext(ctx); err != nil {
			t.Errorf("restore quality index: %v", err)
		}
	}()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `DROP INDEX idx_points_quality_metric`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `CREATE INDEX idx_points_quality_metric ON metric_points (metric_name, SUBSTRING(date,1,10)) WHERE quality = 'OK'`); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "index definition differs") {
		t.Fatalf("wrong partial-index literal verification error=%v", err)
	}
}

func TestVerifySchemaContractRejectsWrongRuntimeColumnShape(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `ALTER TABLE target_snapshots ALTER COLUMN formula_version TYPE bigint`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.pool.Exec(context.Background(), `ALTER TABLE target_snapshots ALTER COLUMN formula_version TYPE integer USING formula_version::integer`); err != nil {
			t.Errorf("restore target_snapshots.formula_version: %v", err)
		}
	}()
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "column definition differs") {
		t.Fatalf("wrong column shape verification error=%v", err)
	}
}

func TestVerifySchemaContractRejectsWrongArrayElementType(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `ALTER TABLE daily_scores ALTER COLUMN stress_flags TYPE integer[] USING NULL::integer[]`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.pool.Exec(context.Background(), `ALTER TABLE daily_scores ALTER COLUMN stress_flags TYPE text[] USING NULL::text[]`); err != nil {
			t.Errorf("restore daily_scores.stress_flags: %v", err)
		}
	}()
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "column definition differs") {
		t.Fatalf("wrong array element type verification error=%v", err)
	}
}

func TestVerifySchemaContractRejectsWrongCompositeConflictKey(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `ALTER TABLE target_snapshots DROP CONSTRAINT target_snapshots_pkey, ADD UNIQUE (date, sub_score)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.pool.Exec(context.Background(), `
			ALTER TABLE target_snapshots
				DROP CONSTRAINT IF EXISTS target_snapshots_date_sub_score_key,
				ADD CONSTRAINT target_snapshots_pkey PRIMARY KEY (date, sub_score, target_kind)`); err != nil {
			t.Errorf("restore target_snapshots conflict key: %v", err)
		}
	}()
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "primary or unique constraint differs") {
		t.Fatalf("wrong composite key verification error=%v", err)
	}
}

func TestVerifySchemaContractRejectsMalformedBootstrapTuple(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `UPDATE source_epochs SET confirmed=false WHERE epoch_id=$1`, InitialSourceEpoch); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifySchemaContractContext(ctx); err == nil || !strings.Contains(err.Error(), "required row is missing") {
		t.Fatalf("malformed bootstrap tuple verification error=%v", err)
	}
}

func TestEnsureTenantIdentityUpgradesLegacyMarker(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity`)
	t.Cleanup(func() { _, _ = db.pool.Exec(context.Background(), `DROP TABLE IF EXISTS __tenant_identity`) })
	if _, err := db.pool.Exec(ctx, `CREATE TABLE __tenant_identity (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), tenant_id uuid NOT NULL, operation_id uuid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	tenantID, operationID := uuid.New(), uuid.New()
	if _, err := db.pool.Exec(ctx, `INSERT INTO __tenant_identity(tenant_id,operation_id) VALUES($1,$2)`, tenantID, operationID); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureTenantIdentity(ctx, tenantID, operationID); err != nil {
		t.Fatalf("upgrade legacy marker: %v", err)
	}
	if err := db.EnsureTenantIdentity(ctx, tenantID, operationID); err != nil {
		t.Fatalf("idempotent marker ensure: %v", err)
	}
	marker, err := db.ReadTenantIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if marker.TenantID != tenantID || marker.OperationID != operationID || marker.SchemaContractVersion != SchemaContractVersion || marker.SchemaContractChecksum != SchemaContractChecksum() {
		t.Fatalf("upgraded marker = %+v", marker)
	}
	if err := RestoreTenantIdentityMarkerContract(ctx, db.pool, "", tenantID, operationID, SchemaContractState{}); err != nil {
		t.Fatalf("restore legacy NULL contract marker: %v", err)
	}
	if err := RestoreTenantIdentityMarkerContract(ctx, db.pool, "", tenantID, operationID, SchemaContractState{}); err != nil {
		t.Fatalf("retry restore legacy NULL contract marker: %v", err)
	}
	var restoredVersion *int
	var restoredChecksum *string
	if err := db.pool.QueryRow(ctx, `SELECT schema_contract_version,schema_contract_checksum FROM __tenant_identity WHERE singleton=true`).Scan(&restoredVersion, &restoredChecksum); err != nil {
		t.Fatal(err)
	}
	if restoredVersion != nil || restoredChecksum != nil {
		t.Fatalf("restored marker contract = %v/%v, want NULL/NULL", restoredVersion, restoredChecksum)
	}
	if err := MigrateTenantIdentityMarker(ctx, db.pool, "", tenantID, operationID, SchemaContractState{}); err != nil {
		t.Fatalf("reapply marker after NULL rollback: %v", err)
	}
}

func TestRestoreTenantIdentityMarkerContractRetriesOlderVersion(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity`)
	t.Cleanup(func() { _, _ = db.pool.Exec(context.Background(), `DROP TABLE IF EXISTS __tenant_identity`) })
	tenantID, operationID := uuid.New(), uuid.New()
	if err := db.EnsureTenantIdentity(ctx, tenantID, operationID); err != nil {
		t.Fatal(err)
	}
	olderVersion, olderChecksum := SchemaContractVersion-1, strings.Repeat("a", 64)
	restore := SchemaContractState{Version: &olderVersion, Checksum: &olderChecksum}
	if err := RestoreTenantIdentityMarkerContract(ctx, db.pool, "", tenantID, operationID, restore); err != nil {
		t.Fatalf("restore older marker contract: %v", err)
	}
	if err := RestoreTenantIdentityMarkerContract(ctx, db.pool, "", tenantID, operationID, restore); err != nil {
		t.Fatalf("retry restore older marker contract: %v", err)
	}
}

func TestEnsureTenantIdentityRejectsIdentityMismatch(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity`)
	t.Cleanup(func() { _, _ = db.pool.Exec(context.Background(), `DROP TABLE IF EXISTS __tenant_identity`) })
	tenantID, operationID := uuid.New(), uuid.New()
	if err := db.EnsureTenantIdentity(ctx, tenantID, operationID); err != nil {
		t.Fatalf("create canonical marker: %v", err)
	}
	if err := db.EnsureTenantIdentity(ctx, uuid.New(), operationID); err == nil {
		t.Fatal("marker ensure accepted a different tenant identity")
	}
	if err := db.EnsureTenantIdentity(ctx, tenantID, uuid.New()); err == nil {
		t.Fatal("marker ensure accepted a different provisioning operation")
	}
}

func TestEnsureTenantIdentityRejectsNonLegacyContractMismatch(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity`)
	t.Cleanup(func() { _, _ = db.pool.Exec(context.Background(), `DROP TABLE IF EXISTS __tenant_identity`) })
	tenantID, operationID := uuid.New(), uuid.New()
	if err := db.EnsureTenantIdentity(ctx, tenantID, operationID); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][2]any{
		"stale":            {SchemaContractVersion - 1, strings.Repeat("a", 64)},
		"newer":            {SchemaContractVersion + 1, SchemaContractChecksum()},
		"partial version":  {nil, SchemaContractChecksum()},
		"partial checksum": {SchemaContractVersion, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.pool.Exec(ctx, `DROP TABLE IF EXISTS __tenant_identity`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.pool.Exec(ctx, `CREATE TABLE __tenant_identity(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),tenant_id uuid NOT NULL,operation_id uuid NOT NULL,schema_contract_version integer,schema_contract_checksum text)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.pool.Exec(ctx, `INSERT INTO __tenant_identity(tenant_id,operation_id,schema_contract_version,schema_contract_checksum) VALUES($1,$2,$3,$4)`, tenantID, operationID, values[0], values[1]); err != nil {
				t.Fatal(err)
			}
			if err := db.EnsureTenantIdentity(ctx, tenantID, operationID); err == nil {
				t.Fatal("marker ensure accepted non-legacy contract mismatch")
			}
		})
	}
}
