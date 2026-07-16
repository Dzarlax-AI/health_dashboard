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
