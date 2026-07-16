package tenants

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

const validImage = "repo/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func canonicalInventory() TenantInventory {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	return TenantInventory{Schema: "health_a", TenantID: id, Role: TenantRoleName(id), CredentialVersion: 1, Registry: RegistryMetadata{Username: "a", TenantID: id, Schema: "health_a", Role: TenantRoleName(id), CredentialVersion: 1, State: "active"}}
}

func TestBuildMigrationPlanIsExactAndSecretFree(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	i := TenantInventory{Schema: "health_a", TenantID: id, Role: TenantRoleName(id), CredentialVersion: 1,
		Registry: RegistryMetadata{TenantID: id, Schema: "health_a", Role: TenantRoleName(id), CredentialVersion: 1, State: "active"},
		Objects:  []OwnedObject{{Kind: "TABLE", Name: "metric_points", Owner: "legacy"}}}
	p, err := BuildMigrationPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	for _, s := range p.Statements {
		statements = append(statements, s.SQL)
	}
	joined := strings.Join(statements, "\n")
	if !strings.Contains(joined, `ALTER TABLE "health_a"."metric_points" OWNER TO "`+i.Role+`"`) {
		t.Fatalf("missing exact owner SQL: %s", joined)
	}
	if strings.Contains(joined, "test-master-secret") || strings.Contains(joined, "postgres://") {
		t.Fatalf("secret-bearing plan: %s", joined)
	}
}

func TestSafetyPolicyBlocksEveryUnsafeCatalogClass(t *testing.T) {
	i := canonicalInventory()
	i.RoleCatalog = RoleMetadata{Exists: true, Login: true, Superuser: true}
	i.Memberships = []MembershipRecord{{Role: "admin", Member: i.Role}}
	i.SchemaACL = []ACLRecord{{ObjectType: "SCHEMA", ObjectName: i.Schema, Grantee: "PUBLIC", Privilege: "CREATE"}}
	i.DefaultPrivileges = []DefaultPrivilege{{ObjectType: "FUNCTION", Grantee: "PUBLIC", Privilege: "EXECUTE"}}
	i.applySafetyPolicy()
	joined := strings.Join(i.Blockers, "\n")
	for _, want := range []string{"unsafe catalog attributes", "unexpected memberships"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "PUBLIC") {
		t.Fatal("default PUBLIC grants must be planned revocations, not blockers")
	}
}

func TestPlanOrderingIsStable(t *testing.T) {
	i := canonicalInventory()
	i.Objects = []OwnedObject{{Kind: "TABLE", Name: "z", Owner: "legacy"}, {Kind: "SEQUENCE", Name: "s", Owner: "legacy"}, {Kind: "TABLE", Name: "a", Owner: "legacy"}}
	p, err := BuildMigrationPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range p.Statements {
		got = append(got, s.SQL)
	}
	joined := strings.Join(got, "\n")
	if strings.Index(joined, `ALTER SEQUENCE "health_a"."s"`) > strings.Index(joined, `ALTER TABLE "health_a"."a"`) {
		t.Fatal("statements are not kind/name ordered")
	}
	if strings.Index(joined, `ALTER TABLE "health_a"."a"`) > strings.Index(joined, `ALTER TABLE "health_a"."z"`) {
		t.Fatal("table statements are not name ordered")
	}
}

func TestManifestRoundTripChecksumAndNoOverwrite(t *testing.T) {
	m, err := NewRollbackManifest(canonicalInventory(), validImage)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "m.json")
	if err = WriteRollbackManifest(path, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRollbackManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checksum != m.Checksum {
		t.Fatal("checksum changed")
	}
	if err = WriteRollbackManifest(path, m); err == nil {
		t.Fatal("expected no-overwrite error")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-3] ^= 1
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadRollbackManifest(path); err == nil {
		t.Fatal("expected tamper detection")
	}
}

func TestManifestPathAllIsUniquePerCanonicalSchema(t *testing.T) {
	a, b := ManifestPath("rollback.JSON", "health_a", true), ManifestPath("rollback.JSON", "health_b", true)
	if a == b || !strings.Contains(a, "health_a") || !strings.Contains(b, "health_b") {
		t.Fatalf("paths not unique: %q %q", a, b)
	}
}

func TestManifestPreservesNullVersusExplicitACL(t *testing.T) {
	i := canonicalInventory()
	i.SchemaACLIsNull = true
	i.SchemaRawACL = nil
	i.Objects = []OwnedObject{{Kind: "TABLE", Name: "a", Owner: "legacy", ACLIsNull: false, RawACL: []string{"legacy=arwdDxt/legacy"}}, {Kind: "FUNCTION", Name: "f", Identity: "", Owner: "legacy", ACLIsNull: true}}
	m, err := NewRollbackManifest(i, strings.TrimPrefix(validImage, "repo/app@"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"schema_acl_is_null":true`, `"acl_is_null":false`, `"raw_acl":["legacy=arwdDxt/legacy"]`, `"acl_is_null":true`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestPlanRevokesPublicAndPreservesGrantOption(t *testing.T) {
	i := canonicalInventory()
	i.Objects = []OwnedObject{{Kind: "FUNCTION", Name: "f", Identity: "integer", Owner: "legacy"}}
	i.Grants = []GrantRecord{{ObjectType: "FUNCTION", ObjectName: "f(integer)", Grantee: "PUBLIC", Privilege: "EXECUTE"}, {ObjectType: "FUNCTION", ObjectName: "f(integer)", Grantee: "auditor", Privilege: "EXECUTE", Grantable: true}}
	p, err := BuildMigrationPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	s := string(b)
	for _, want := range []string{`REVOKE ALL ON FUNCTION`, `TO \"auditor\" WITH GRANT OPTION`, `set_role_password`, `postgres_role_password`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, `PASSWORD :`) {
		t.Fatal("password rendered as pseudo SQL")
	}
}

func TestPlanDoesNotMaterializeImplicitFormerOwnerACL(t *testing.T) {
	i := canonicalInventory()
	i.SchemaACLIsNull = false
	i.SchemaACL = []ACLRecord{{ObjectType: "SCHEMA", ObjectName: i.Schema, Grantor: i.SchemaOwner, Grantee: i.SchemaOwner, Privilege: "USAGE"}}
	i.Objects = []OwnedObject{{Kind: "TABLE", Name: "metric_points", Owner: i.SchemaOwner, ACLIsNull: false}}
	i.Grants = []GrantRecord{
		{ObjectType: "TABLE", ObjectName: "metric_points", Grantor: i.SchemaOwner, Grantee: i.SchemaOwner, Privilege: "SELECT"},
		{ObjectType: "TABLE", ObjectName: "metric_points", Grantor: i.SchemaOwner, Grantee: "reporter", Privilege: "SELECT"},
		{ObjectType: "SEQUENCE", ObjectName: "metric_points_id_seq", Grantor: i.SchemaOwner, Grantee: i.SchemaOwner, Privilege: "USAGE"},
	}
	p, err := BuildMigrationPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	s := string(b)
	if strings.Contains(s, `TO \"`+i.SchemaOwner+`\"`) {
		t.Fatalf("implicit former-owner ACL was materialized: %s", s)
	}
	if !strings.Contains(s, `TO \"reporter\"`) {
		t.Fatalf("reviewed third-party ACL was not preserved: %s", s)
	}
}

func TestPlanUsesTablePrivilegeSyntaxForTableLikeRelations(t *testing.T) {
	for _, kind := range []string{"TABLE", "VIEW", "MATERIALIZED VIEW", "FOREIGN TABLE"} {
		i := canonicalInventory()
		i.Objects = []OwnedObject{{Kind: kind, Name: "report", Owner: "legacy"}}
		p, err := BuildMigrationPlan(i)
		if err != nil {
			t.Fatal(err)
		}
		var sql []string
		for _, statement := range p.Statements {
			sql = append(sql, statement.SQL)
		}
		text := strings.Join(sql, "\n")
		if !strings.Contains(text, `REVOKE ALL ON TABLE "health_a"."report" FROM PUBLIC`) {
			t.Fatalf("%s missing TABLE privilege syntax: %s", kind, text)
		}
		for _, invalid := range []string{"REVOKE ALL ON VIEW ", "REVOKE ALL ON MATERIALIZED VIEW ", "REVOKE ALL ON FOREIGN TABLE "} {
			if strings.Contains(text, invalid) {
				t.Fatalf("%s emitted invalid syntax %q", kind, invalid)
			}
		}
	}
}

func TestRollbackPlanRestoresOwnersAndEffectivePrivileges(t *testing.T) {
	i := canonicalInventory()
	i.SchemaOwner = "legacy_owner"
	i.Objects = []OwnedObject{{Kind: "TABLE", Name: "metric_points", Owner: "legacy_owner"}}
	i.SchemaACL = []ACLRecord{{ObjectType: "SCHEMA", ObjectName: i.Schema, Grantor: "legacy_owner", Grantee: "PUBLIC", Privilege: "USAGE"}}
	i.Grants = []GrantRecord{{ObjectType: "TABLE", ObjectName: "metric_points", Grantor: "legacy_owner", Grantee: "reporter", Privilege: "SELECT", Grantable: true}}
	i.DefaultPrivileges = []DefaultPrivilege{{Owner: "legacy_owner", Schema: i.Schema, ObjectType: "f", Grantor: "legacy_owner", Grantee: "PUBLIC", Privilege: "EXECUTE"}}
	p, err := BuildRollbackPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	s := string(b)
	for _, want := range []string{
		`ALTER TABLE \"health_a\".\"metric_points\" OWNER TO \"legacy_owner\"`,
		`ALTER SCHEMA \"health_a\" OWNER TO \"legacy_owner\"`,
		`REVOKE ALL ON TABLE \"health_a\".\"metric_points\" FROM \"` + i.Role + `\"`,
		`GRANT USAGE ON SCHEMA \"health_a\" TO PUBLIC`,
		`GRANT SELECT ON TABLE \"health_a\".\"metric_points\" TO \"reporter\" WITH GRANT OPTION`,
		`registry_compare_and_set_isolation_ready_false`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, `TO \"PUBLIC\"`) {
		t.Fatal("PUBLIC was incorrectly emitted as a quoted role")
	}
}

func TestRollbackPlanPreservesExistingTenantMarker(t *testing.T) {
	i := canonicalInventory()
	i.Objects = []OwnedObject{{Kind: "TABLE", Name: storage.TenantIdentityTable, Owner: "legacy_owner"}}
	i.Marker = &TenantMarkerMetadata{TenantID: i.TenantID, OperationID: uuid.New()}
	p, err := BuildRollbackPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), "restore_tenant_identity_marker_contract") || strings.Contains(string(b), "drop_migration_tenant_identity_marker") {
		t.Fatalf("existing marker rollback operations = %s", b)
	}
}

func TestRegistryContractStateMatchesRequiresExactLandedState(t *testing.T) {
	i := canonicalInventory()
	version, checksum := storage.SchemaContractVersion, storage.SchemaContractChecksum()
	if !registryContractStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", true, &version, &checksum) {
		t.Fatal("exact landed registry state was not accepted")
	}
	wrongChecksum := strings.Repeat("f", 64)
	for name, matched := range map[string]bool{
		"wrong tenant":   registryContractStateMatches(i, uuid.New(), i.Schema, i.Role, i.CredentialVersion, "active", true, &version, &checksum),
		"not ready":      registryContractStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", false, &version, &checksum),
		"wrong state":    registryContractStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "pending", true, &version, &checksum),
		"wrong checksum": registryContractStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", true, &version, &wrongChecksum),
		"null contract":  registryContractStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", true, nil, nil),
	} {
		if matched {
			t.Fatalf("%s was accepted as landed", name)
		}
	}
}

func TestRegistryRollbackStateMatchesRequiresExactRestoredState(t *testing.T) {
	i := canonicalInventory()
	if !registryRollbackStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", false, nil, nil) {
		t.Fatal("exact NULL/NULL rollback state was not accepted")
	}
	version, checksum := storage.SchemaContractVersion, storage.SchemaContractChecksum()
	i.Registry.ContractVersion, i.Registry.ContractChecksum = &version, &checksum
	if !registryRollbackStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", false, &version, &checksum) {
		t.Fatal("exact versioned rollback state was not accepted")
	}
	for name, matched := range map[string]bool{
		"still ready":      registryRollbackStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", true, &version, &checksum),
		"wrong identity":   registryRollbackStateMatches(i, uuid.New(), i.Schema, i.Role, i.CredentialVersion, "active", false, &version, &checksum),
		"wrong state":      registryRollbackStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "pending", false, &version, &checksum),
		"wrong contract":   registryRollbackStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", false, nil, nil),
		"partial contract": registryRollbackStateMatches(i, i.TenantID, i.Schema, i.Role, i.CredentialVersion, "active", false, &version, nil),
	} {
		if matched {
			t.Fatalf("%s was accepted as restored", name)
		}
	}
}

func TestCredentialRotationFailsClosedBeforeDatabaseMutation(t *testing.T) {
	i := canonicalInventory()
	m := &Migrator{}
	if err := m.RotateTenantCredential(t.Context(), i, 1, 2, ""); err == nil || !strings.Contains(err.Error(), "isolation-ready") {
		t.Fatalf("non-ready rotation error = %v", err)
	}
	i.Registry.IsolationReady = true
	if err := m.RotateTenantCredential(t.Context(), i, 1, 1, ""); !errors.Is(err, registry.ErrProvisioningStateConflict) {
		t.Fatalf("same-version rotation error = %v", err)
	}
}

func TestRollbackImageRequiresFullSHA256Digest(t *testing.T) {
	i := canonicalInventory()
	for _, image := range []string{"sha256:abc", "sha256:" + strings.Repeat("a", 63), "sha256:" + strings.Repeat("a", 65), "repo/app@sha256:" + strings.Repeat("g", 64), "repo/app@sha256:" + strings.Repeat("a", 64) + "extra"} {
		if _, err := NewRollbackManifest(i, image); err == nil {
			t.Fatalf("accepted malformed image %q", image)
		}
	}
	m, err := NewRollbackManifest(i, "repo/app@sha256:"+strings.Repeat("A", 64))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.ImageReference, "A") {
		t.Fatalf("digest not normalized: %s", m.ImageReference)
	}
}

func TestManifestSetReportsCleanupFailureAndPartialPaths(t *testing.T) {
	dir := t.TempDir()
	a, b := canonicalInventory(), canonicalInventory()
	b.Schema, b.Registry.Schema = "health_b", "health_b"
	ma, _ := NewRollbackManifest(a, validImage)
	mb, _ := NewRollbackManifest(b, validImage)
	pa, pb := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	originalOpen, originalRemove := manifestOpenFile, manifestRemove
	t.Cleanup(func() { manifestOpenFile, manifestRemove = originalOpen, originalRemove })
	manifestOpenFile = func(path string, flag int, perm os.FileMode) (*os.File, error) {
		if path == pb {
			return nil, errors.New("write boom")
		}
		return os.OpenFile(path, flag, perm)
	}
	manifestRemove = func(path string) error { return fmt.Errorf("remove boom") }
	err := WriteRollbackManifestSet(map[string]RollbackManifest{pa: ma, pb: mb})
	if err == nil || !strings.Contains(err.Error(), "write boom") || !strings.Contains(err.Error(), "remove boom") || !strings.Contains(err.Error(), pa) {
		t.Fatalf("incomplete cleanup error: %v", err)
	}
}

func TestSecretScannerReportsNestedFieldPath(t *testing.T) {
	for _, mutate := range []func(*TenantInventory){func(i *TenantInventory) { i.RoleCatalog.Comment = ptr("Bearer abc") }, func(i *TenantInventory) {
		i.Objects = []OwnedObject{{Kind: "FUNCTION", Name: "f", Owner: "legacy", ProConfig: []string{"api_key=abc"}}}
	}, func(i *TenantInventory) { i.Registry.Username = "postgres://u:p@host/db" }} {
		i := canonicalInventory()
		mutate(&i)
		_, err := NewRollbackManifest(i, strings.TrimPrefix(validImage, "repo/app@"))
		if err == nil || !strings.Contains(err.Error(), "inventory.") {
			t.Fatalf("missing path error: %v", err)
		}
	}
}
func ptr(s string) *string { return &s }

func TestManifestSetPreflightConflictWritesZero(t *testing.T) {
	dir := t.TempDir()
	a := canonicalInventory()
	b := canonicalInventory()
	b.Schema = "health_b"
	b.Registry.Schema = "health_b"
	base := filepath.Join(dir, "rollback.json")
	ma, _ := NewRollbackManifest(a, validImage)
	mb, _ := NewRollbackManifest(b, validImage)
	pa, pb := ManifestPath(base, a.Schema, true), ManifestPath(base, b.Schema, true)
	if err := os.WriteFile(pb, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteRollbackManifestSet(map[string]RollbackManifest{pa: ma, pb: mb}); err == nil {
		t.Fatal("expected conflict")
	}
	if _, err := os.Stat(pa); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first file was written: %v", err)
	}
}

type fakeRows struct {
	values              [][]any
	at                  int
	scanErr, errorAfter error
	closed              bool
}

func (f *fakeRows) Next() bool { return f.at < len(f.values) }
func (f *fakeRows) Scan(dest ...any) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	row := f.values[f.at]
	f.at++
	for n, v := range row {
		*dest[n].(*string) = v.(string)
	}
	return nil
}
func (f *fakeRows) Err() error { return f.errorAfter }
func (f *fakeRows) Close()     { f.closed = true }

func TestScanRowsPropagatesScanAndRowsErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		rows *fakeRows
		want error
	}{
		{"scan", &fakeRows{values: [][]any{{"x"}}, scanErr: errors.New("scan")}, errors.New("scan")},
		{"rows", &fakeRows{errorAfter: errors.New("rows")}, errors.New("rows")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scanRows[string](tt.rows, func(r rowsLike, v *string) error { return r.Scan(v) })
			if err == nil || err.Error() != tt.want.Error() {
				t.Fatalf("got %v want %v", err, tt.want)
			}
			if !tt.rows.closed {
				t.Fatal("rows not closed")
			}
		})
	}
}

func TestInventoryUnsafeSecurityDefinerBlocks(t *testing.T) {
	id := uuid.New()
	i := TenantInventory{Schema: "health_a", TenantID: id, Role: TenantRoleName(id), CredentialVersion: 1, Registry: RegistryMetadata{TenantID: id, Schema: "health_a", Role: TenantRoleName(id), CredentialVersion: 1, State: "active"}, Blockers: []string{"unsafe SECURITY DEFINER function health_a.f"}}
	p, err := BuildMigrationPlan(i)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Blocked || len(p.Statements) != 0 {
		t.Fatal("expected blocker-only plan")
	}
}

func TestRollbackManifestJSONContainsNoSecretFields(t *testing.T) {
	id := uuid.New()
	i := TenantInventory{Schema: "health_a", TenantID: id, Role: TenantRoleName(id), CredentialVersion: 1, Registry: RegistryMetadata{TenantID: id, Schema: "health_a", Role: TenantRoleName(id), CredentialVersion: 1, State: "active"}}
	m, err := NewRollbackManifest(i, validImage)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(b))
	for _, forbidden := range []string{"derived_password_value", "postgres://", "master_secret"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("manifest contains %q: %s", forbidden, s)
		}
	}
}
