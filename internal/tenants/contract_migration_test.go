package tenants

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

func contractMigrationFixture() fleetSnapshot {
	s := auditFixture()
	s.Markers[0].RelKind = "r"
	i, ok := snapshotProbeInventory(s, s.Registry[0].Schema)
	if !ok {
		panic("invalid contract migration fixture")
	}
	i.SchemaOwner = i.Role
	i.RoleCatalog = RoleMetadata{Name: i.Role, Exists: true, Login: true, Inherit: true, ConnLimit: -1}
	s.Inventories = []TenantInventory{i}
	return s
}

func TestMigrateTenantContractRefusesInitialIsolationCutover(t *testing.T) {
	id := uuid.New()
	i := TenantInventory{
		Schema:            "health_contract_test",
		TenantID:          id,
		Role:              TenantRoleName(id),
		CredentialVersion: 1,
		Registry: RegistryMetadata{
			TenantID:          id,
			Schema:            "health_contract_test",
			Role:              TenantRoleName(id),
			CredentialVersion: 1,
			State:             "active",
			IsolationReady:    false,
		},
	}
	err := (&Migrator{}).MigrateTenantContract(t.Context(), i, nil)
	if !errors.Is(err, ErrContractMigrationFleetUnstable) {
		t.Fatalf("error = %v, want unstable fleet sentinel", err)
	}
}

func TestContractMigrationFleetCannotMarshalRawIdentifiers(t *testing.T) {
	id := uuid.New()
	fleet := ContractMigrationFleet{
		Inventories: []TenantInventory{{Schema: "health_secret", TenantID: id, Role: TenantRoleName(id)}},
		PeerSchemas: []string{"health_secret"},
	}
	b, err := json.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"health_secret", id.String(), TenantRoleName(id)} {
		if strings.Contains(string(b), raw) {
			t.Fatalf("raw identifier leaked through fleet marshal: %s", b)
		}
	}
}

func TestMigrateTenantContractRequiresPermanentMarker(t *testing.T) {
	id := uuid.New()
	i := TenantInventory{
		Schema:            "health_contract_test",
		TenantID:          id,
		Role:              TenantRoleName(id),
		CredentialVersion: 1,
		Registry: RegistryMetadata{
			TenantID:          id,
			Schema:            "health_contract_test",
			Role:              TenantRoleName(id),
			CredentialVersion: 1,
			State:             "active",
			IsolationReady:    true,
		},
	}
	err := (&Migrator{}).MigrateTenantContract(t.Context(), i, nil)
	if !errors.Is(err, ErrContractMigrationFleetUnstable) {
		t.Fatalf("error = %v, want unstable fleet sentinel", err)
	}
}

func TestContractMigrationStructuralDigestExcludesExpectedMigrationChanges(t *testing.T) {
	base := contractMigrationFixture()
	changed := contractMigrationFixture()
	oldVersion, oldChecksum := storage.SchemaContractVersion+1, strings.Repeat("a", 64)
	changed.Registry[0].ContractVersion, changed.Registry[0].ContractChecksum = &oldVersion, &oldChecksum
	changed.Markers[0].Rows[0].ContractVersion, changed.Markers[0].Rows[0].ContractChecksum = &oldVersion, &oldChecksum
	changed.Inventories[0].Objects = []OwnedObject{{Kind: "TABLE", Name: "new_contract_object", Owner: changed.Inventories[0].Role}}
	if contractMigrationStructuralDigest(base) != contractMigrationStructuralDigest(changed) {
		t.Fatal("digest included contract metadata or mutable catalog objects")
	}
	changed.Registry[0].IsolationReady = false
	if contractMigrationStructuralDigest(base) == contractMigrationStructuralDigest(changed) {
		t.Fatal("digest ignored structural registry drift")
	}
}

func TestPrepareContractMigrationFleetAllowsOldPairButRejectsStructuralCorruption(t *testing.T) {
	legacy := contractMigrationFixture()
	legacy.Registry[0].ContractVersion, legacy.Registry[0].ContractChecksum = nil, nil
	legacy.Markers[0].Rows[0].ContractVersion, legacy.Markers[0].Rows[0].ContractChecksum = nil, nil
	fleet, err := prepareContractMigrationFleetSnapshot(legacy)
	if err != nil || fleet.Digest == "" || !slices.Equal(fleet.PeerSchemas, []string{"health_secret"}) {
		t.Fatalf("legacy fleet = %+v, %v", fleet, err)
	}

	mutations := map[string]func(*fleetSnapshot){
		"marker relkind": func(s *fleetSnapshot) { s.Markers[0].RelKind = "v" },
		"marker issue":   func(s *fleetSnapshot) { s.Markers[0].Issues = []string{"marker_column_type_mismatch"} },
		"marker rows":    func(s *fleetSnapshot) { s.Markers[0].Rows = nil },
		"orphan marker": func(s *fleetSnapshot) {
			s.Markers = append(s.Markers, auditMarkerRow{Schema: "health_orphan", RelKind: "r"})
		},
		"missing role":   func(s *fleetSnapshot) { s.Roles = nil },
		"duplicate role": func(s *fleetSnapshot) { s.Roles = append(s.Roles, s.Roles[0]) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := contractMigrationFixture()
			mutate(&s)
			if _, err := prepareContractMigrationFleetSnapshot(s); !errors.Is(err, ErrContractMigrationFleetUnstable) {
				t.Fatalf("error = %v, want unstable fleet", err)
			}
		})
	}
}

func TestLegacyMarkerVariantsNormalizeAcrossTwoTenantProgression(t *testing.T) {
	s := contractMigrationFixture()
	first := &s.Markers[0]
	first.Rows[0].ContractVersion, first.Rows[0].ContractChecksum = nil, nil
	first.Issues = []string{"marker_column_missing:schema_contract_version", "marker_column_missing:schema_contract_checksum"}
	s.Registry[0].ContractVersion, s.Registry[0].ContractChecksum = nil, nil

	id, op, credential := uuid.New(), uuid.New(), 1
	schema, role := "health_second", TenantRoleName(id)
	s.Registry = append(s.Registry, auditRegistryRow{TenantID: &id, Schema: schema, Role: role, CredentialVersion: &credential, IsolationReady: true, State: "active"})
	s.Markers = append(s.Markers, auditMarkerRow{Schema: schema, RelKind: "r", Issues: []string{"marker_column_nullability_mismatch:schema_contract_version:YES", "marker_column_nullability_mismatch:schema_contract_checksum:YES"}, Rows: []auditMarkerIdentity{{Singleton: boolPtr(true), TenantID: &id, OperationID: &op}}})
	s.Roles = append(s.Roles, auditRoleRow{TenantID: id, Role: role, OperationID: op, Valid: true})
	s.Inventories = append(s.Inventories, TenantInventory{Schema: schema, TenantID: id, Role: role, CredentialVersion: 1, SchemaOwner: role, RoleCatalog: RoleMetadata{Name: role, Exists: true, Login: true, Inherit: true, ConnLimit: -1}, Registry: RegistryMetadata{TenantID: id, Schema: schema, Role: role, CredentialVersion: 1, IsolationReady: true, State: "active"}})

	fleet, err := prepareContractMigrationFleetSnapshot(s)
	if err != nil || len(fleet.Inventories) != 2 {
		t.Fatalf("two-tenant legacy preflight = %+v, %v", fleet, err)
	}
	before := fleet.Digest
	version, checksum := storage.SchemaContractVersion, storage.SchemaContractChecksum()
	s.Registry[0].ContractVersion, s.Registry[0].ContractChecksum = &version, &checksum
	s.Markers[0].Rows[0].ContractVersion, s.Markers[0].Rows[0].ContractChecksum = &version, &checksum
	s.Markers[0].Issues = nil
	after, err := prepareContractMigrationFleetSnapshot(s)
	if err != nil || after.Digest != before {
		t.Fatalf("first tenant expected marker upgrade changed structural fleet: before=%s after=%s err=%v", before, after.Digest, err)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestContractMigrationFleetDoesNotCoupleRolloutOrderToAdminStatus(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		s := contractMigrationFixture()
		s.Registry[0].IsPrimary = isAdmin
		if _, err := prepareContractMigrationFleetSnapshot(s); err != nil {
			t.Fatalf("is_admin=%t changed migration eligibility: %v", isAdmin, err)
		}
	}
	a, b := contractMigrationFixture(), contractMigrationFixture()
	b.Registry[0].IsPrimary = !a.Registry[0].IsPrimary
	if contractMigrationStructuralDigest(a) != contractMigrationStructuralDigest(b) {
		t.Fatal("admin authorization leaked into rollout structural digest")
	}
}
