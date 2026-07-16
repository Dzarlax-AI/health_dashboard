package tenants

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"health-receiver/internal/storage"
)

func auditFixture() fleetSnapshot {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	op := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	version, checksum, credential := storage.SchemaContractVersion, storage.SchemaContractChecksum(), 1
	return fleetSnapshot{
		Registry: []auditRegistryRow{{TenantID: &id, Schema: "health_secret", Role: TenantRoleName(id), CredentialVersion: &credential, IsolationReady: true, State: "active", ContractVersion: &version, ContractChecksum: &checksum}},
		Markers:  []auditMarkerRow{auditMarker("health_secret", id, op, version, checksum)},
		Roles:    []auditRoleRow{{TenantID: id, Role: TenantRoleName(id), OperationID: op, Valid: true}},
	}
}

func auditMarker(schema string, id, op uuid.UUID, version int, checksum string) auditMarkerRow {
	singleton := true
	return auditMarkerRow{Schema: schema, RelKind: "r", Persistence: "p", Rows: []auditMarkerIdentity{{Singleton: &singleton, TenantID: &id, OperationID: &op, ContractVersion: &version, ContractChecksum: &checksum}}}
}

func TestEvaluateFleetSnapshotPassAndDeterministicDigest(t *testing.T) {
	a := auditFixture()
	b := a
	b.Registry = append([]auditRegistryRow(nil), a.Registry...)
	b.Markers = append([]auditMarkerRow(nil), a.Markers...)
	b.Roles = append([]auditRoleRow(nil), a.Roles...)
	if got := evaluateFleetSnapshot(a); len(got) != 0 {
		t.Fatalf("findings=%+v", got)
	}
	if fleetDigest(a) != fleetDigest(b) {
		t.Fatal("digest is not deterministic")
	}
	changed := a
	changed.Registry = append([]auditRegistryRow(nil), a.Registry...)
	changed.Registry[0].IsolationReady = false
	if fleetDigest(a) == fleetDigest(changed) {
		t.Fatal("digest did not change with inventory")
	}
}

func TestFleetDigestCanonicalizesPointersAndPermutation(t *testing.T) {
	a := auditFixture()
	failedID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	credential := 2
	a.Registry = append(a.Registry, auditRegistryRow{TenantID: &failedID, Schema: "health_failed", Role: TenantRoleName(failedID), CredentialVersion: &credential, State: "failed"})
	a.Operations = []auditOperationRow{{OperationID: uuid.New(), TenantID: failedID, Schema: "health_failed", Role: TenantRoleName(failedID), CredentialVersion: 2, State: "provisioning"}}
	b := auditFixture()
	failedIDCopy := failedID
	credentialCopy := 2
	b.Registry = append([]auditRegistryRow{{TenantID: &failedIDCopy, Schema: "health_failed", Role: TenantRoleName(failedIDCopy), CredentialVersion: &credentialCopy, State: "failed"}}, b.Registry...)
	b.Operations = append([]auditOperationRow(nil), a.Operations...)
	if fleetDigest(a) != fleetDigest(b) {
		t.Fatalf("pointer/order changed digest: %s != %s", fleetDigest(a), fleetDigest(b))
	}
	*b.Registry[0].CredentialVersion = 3
	if fleetDigest(a) == fleetDigest(b) {
		t.Fatal("changed scalar did not change digest")
	}
}

func TestFleetDigestTypedEncodingRejectsDelimiterCollisions(t *testing.T) {
	a := fleetSnapshot{Registry: []auditRegistryRow{{Schema: "a|b", Role: "c", State: "active"}}}
	b := fleetSnapshot{Registry: []auditRegistryRow{{Schema: "a", Role: "b|c", State: "active"}}}
	if fleetDigest(a) == fleetDigest(b) {
		t.Fatal("typed digest collapsed delimiter-shaped values")
	}
}

func TestFleetDigestIncludesAuditedCatalogSurface(t *testing.T) {
	s := auditFixture()
	id := *s.Registry[0].TenantID
	inventory := TenantInventory{Schema: s.Registry[0].Schema, TenantID: id, Role: s.Registry[0].Role, SchemaOwner: s.Registry[0].Role, Objects: []OwnedObject{{Kind: "TABLE", Name: "metric_points", Owner: s.Registry[0].Role}}, Grants: []GrantRecord{{ObjectType: "TABLE", ObjectName: "metric_points", Grantor: s.Registry[0].Role, Grantee: s.Registry[0].Role, Privilege: "SELECT"}}, RoleCatalog: RoleMetadata{Name: s.Registry[0].Role, Exists: true, Login: true, Inherit: true, ConnLimit: -1}}
	s.Inventories = []TenantInventory{inventory}
	base := fleetDigest(s)
	mutations := []func(*TenantInventory){func(i *TenantInventory) { i.Objects[0].Owner = "other" }, func(i *TenantInventory) {
		i.Grants = append(i.Grants, GrantRecord{ObjectType: "TABLE", ObjectName: "metric_points", Grantee: "PUBLIC", Privilege: "SELECT"})
	}, func(i *TenantInventory) { i.RoleCatalog.Superuser = true }, func(i *TenantInventory) {
		i.Memberships = append(i.Memberships, MembershipRecord{Role: "other", Member: i.Role})
	}, func(i *TenantInventory) { i.Blockers = append(i.Blockers, "fixture blocker") }}
	for idx, mutate := range mutations {
		changed := s
		changed.Inventories = append([]TenantInventory(nil), s.Inventories...)
		changed.Inventories[0].Objects = append([]OwnedObject(nil), inventory.Objects...)
		changed.Inventories[0].Grants = append([]GrantRecord(nil), inventory.Grants...)
		mutate(&changed.Inventories[0])
		if fleetDigest(changed) == base {
			t.Errorf("catalog mutation %d did not change digest", idx)
		}
	}
}

func TestFleetPeerSchemasIsDeduplicatedActiveAndPhysicalUnion(t *testing.T) {
	s := auditFixture()
	markerlessID := uuid.New()
	credential := 1
	s.Registry = append(s.Registry, auditRegistryRow{TenantID: &markerlessID, Schema: "health_markerless", Role: TenantRoleName(markerlessID), CredentialVersion: &credential, State: "active"})
	s.Markers = append(s.Markers, auditMarkerRow{Schema: "health_orphan"}, s.Markers[0])
	want := []string{"health_markerless", "health_orphan", "health_secret"}
	if got := fleetPeerSchemas(s); !slices.Equal(got, want) {
		t.Fatalf("peer schemas=%v want=%v", got, want)
	}
}

func TestEvaluateFleetSnapshotClassifiesPhysicalMalformedMarkers(t *testing.T) {
	s := auditFixture()
	trueValue := true
	s.Markers = []auditMarkerRow{
		{Schema: "health_secret", Issues: []string{"marker_column_type_mismatch", "marker_column_nullability_mismatch"}},
		{Schema: "health_empty"},
		{Schema: "health_multiple", Rows: []auditMarkerIdentity{{Singleton: &trueValue}, {Singleton: &trueValue}}, Issues: []string{"marker_contract_columns_partial", "marker_column_missing"}},
		{Schema: "health_wrong_relation", RelKind: "p", Issues: []string{"marker_relation_kind_invalid:p", "marker_singleton_primary_key_missing", "marker_singleton_check_missing", "marker_singleton_default_mismatch"}},
	}
	codes := findingCodes(evaluateFleetSnapshot(s))
	for _, want := range []string{"marker_empty", "marker_multiple_rows", "marker_multiple_singleton_rows", "marker_column_type_mismatch", "marker_column_nullability_mismatch", "marker_contract_columns_partial", "marker_column_missing", "marker_relation_kind_invalid", "marker_singleton_primary_key_missing", "marker_singleton_check_missing", "marker_singleton_default_mismatch", "marker_registry_missing"} {
		if !codes[want] {
			t.Errorf("missing %s in %v", want, codes)
		}
	}
	if countsForSnapshot(s).Markers != 4 {
		t.Fatalf("physical marker count=%d", countsForSnapshot(s).Markers)
	}
}

func TestEvaluateFleetSnapshotAllStatesAndExactSets(t *testing.T) {
	s := auditFixture()
	failedID, orphanID := uuid.New(), uuid.New()
	zero := 0
	s.Registry = append(s.Registry,
		auditRegistryRow{TenantID: &failedID, Schema: "health_failed", Role: TenantRoleName(failedID), CredentialVersion: &zero, State: "failed"},
		s.Registry[0],
	)
	s.Markers = append(s.Markers, auditMarker("health_orphan", orphanID, uuid.New(), storage.SchemaContractVersion, storage.SchemaContractChecksum()), s.Markers[0])
	s.Roles = append(s.Roles, auditRoleRow{TenantID: orphanID, Role: TenantRoleName(orphanID)}, s.Roles[0])
	s.Operations = append(s.Operations, auditOperationRow{TenantID: failedID, State: "provisioning"})
	codes := findingCodes(evaluateFleetSnapshot(s))
	for _, want := range []string{"registry_state_failed", "registry_nonterminal", "duplicate_registry_tenant", "duplicate_marker_schema", "duplicate_role", "marker_registry_missing", "role_registry_missing"} {
		if !codes[want] {
			t.Errorf("missing code %s in %v", want, codes)
		}
	}
}

func TestEvaluateFleetSnapshotIdentityContractAndCredentialFailures(t *testing.T) {
	s := auditFixture()
	wrongID := uuid.New()
	badChecksum, zero := "stale", 0
	s.Registry[0].Role = "wrong_role"
	s.Registry[0].CredentialVersion = &zero
	s.Registry[0].IsolationReady = false
	s.Registry[0].ContractChecksum = &badChecksum
	s.Markers[0].Rows[0].TenantID = &wrongID
	s.Markers[0].Rows[0].ContractChecksum = &badChecksum
	s.Roles = nil
	codes := findingCodes(evaluateFleetSnapshot(s))
	for _, want := range []string{"registry_role_invalid", "credential_version_mismatch", "isolation_not_ready", "contract_checksum_mismatch", "marker_identity_mismatch", "role_registry_missing"} {
		if !codes[want] {
			t.Errorf("missing code %s in %v", want, codes)
		}
	}
}

func TestEvaluateFleetSnapshotDetectsRoleMarkerOperationMismatch(t *testing.T) {
	s := auditFixture()
	s.Roles[0].OperationID = uuid.New()
	if !findingCodes(evaluateFleetSnapshot(s))["role_marker_operation_mismatch"] {
		t.Fatalf("findings=%+v", evaluateFleetSnapshot(s))
	}
}

func TestAuditFindingsAreSortedPseudonymousAndRedacted(t *testing.T) {
	s := auditFixture()
	s.Markers = nil
	findings := evaluateFleetSnapshot(s)
	if len(findings) < 1 {
		t.Fatal("expected findings")
	}
	ref := tenantRef(*s.Registry[0].TenantID, s.Registry[0].Schema)
	if len(ref) != 32 {
		t.Fatalf("tenant ref length=%d want 32", len(ref))
	}
	if findings[0].TenantRef == "" || ref != tenantRef(*s.Registry[0].TenantID, s.Registry[0].Schema) || ref == tenantRef(*s.Registry[0].TenantID, "health_other") {
		t.Fatal("unstable tenant ref")
	}
	for _, finding := range findings {
		if finding.TenantRef == "" {
			t.Fatalf("finding missing tenant_ref: %+v", finding)
		}
	}
	b, err := json.Marshal(AuditResult{Status: AuditStatusFail, Findings: findings})
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, secret := range []string{"health_secret", TenantRoleName(*s.Registry[0].TenantID), s.Registry[0].TenantID.String(), "postgres://"} {
		if strings.Contains(text, secret) {
			t.Fatalf("public JSON leaks %q: %s", secret, text)
		}
	}
	for i := 1; i < len(findings); i++ {
		if findingLess(findings[i], findings[i-1]) {
			t.Fatalf("findings not sorted: %+v", findings)
		}
	}
}

func TestInventorySafetyFindingsExplicitAllowRules(t *testing.T) {
	s := auditFixture()
	id := *s.Registry[0].TenantID
	i := TenantInventory{Schema: s.Registry[0].Schema, TenantID: id, Role: s.Registry[0].Role, SchemaOwner: s.Registry[0].Role,
		RoleCatalog:       RoleMetadata{Exists: true, Login: true, Inherit: true, ConnLimit: -1},
		Objects:           []OwnedObject{{Kind: "TABLE", Name: "metric_points", Owner: s.Registry[0].Role}},
		SchemaACL:         []ACLRecord{{ObjectType: "SCHEMA", Grantee: s.Registry[0].Role, Grantor: s.Registry[0].Role, Privilege: "USAGE"}},
		Grants:            []GrantRecord{{ObjectType: "TABLE", ObjectName: "metric_points", Grantee: s.Registry[0].Role, Grantor: s.Registry[0].Role, Privilege: "SELECT"}},
		DefaultPrivileges: []DefaultPrivilege{{Owner: s.Registry[0].Role, Schema: s.Registry[0].Schema, ObjectType: "r", Grantee: s.Registry[0].Role, Grantor: s.Registry[0].Role, Privilege: "SELECT"}},
	}
	if got := inventorySafetyFindings(i); len(got) != 0 {
		t.Fatalf("allowed inventory findings=%+v", got)
	}
	i.Objects[0].Owner = "postgres"
	i.Grants = append(i.Grants, GrantRecord{Grantee: "PUBLIC", Privilege: "SELECT"})
	i.DefaultPrivileges[0].Schema = ""
	i.Memberships = []MembershipRecord{{Role: "unexpected", Member: i.Role}}
	codes := findingCodes(inventorySafetyFindings(i))
	for _, want := range []string{"unexpected_owner", "unexpected_grant", "default_acl_mismatch", "unsafe_role"} {
		if !codes[want] {
			t.Errorf("missing %s in %v", want, codes)
		}
	}
}

func TestRestrictedVerificationErrorTyping(t *testing.T) {
	if got := classifyOwnSchemaReadError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancellation became logical: %T %v", got, got)
	}
	permission := &pgconn.PgError{Code: "42501"}
	var logical *restrictedVerificationError
	if got := classifyOwnSchemaReadError(permission); !errors.As(got, &logical) || logical.code != "own_schema_access_denied" {
		t.Fatalf("permission classification=%T %v", got, got)
	}
	transport := errors.New("transport closed")
	if got := classifyMarkerVerificationError(transport); got != transport {
		t.Fatalf("transport became logical: %T %v", got, got)
	}
	mismatch := &storage.SchemaContractMismatchError{Reason: "fixture"}
	logical = nil
	if got := classifyMarkerVerificationError(mismatch); !errors.As(got, &logical) || logical.code != "marker_read_failed" {
		t.Fatalf("marker mismatch classification=%T %v", got, got)
	}
}

func findingCodes(fs []AuditFinding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Code] = true
	}
	return out
}
