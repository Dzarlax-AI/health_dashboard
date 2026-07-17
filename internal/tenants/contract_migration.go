package tenants

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/storage"
)

// ErrContractMigrationFleetUnstable means that the registry cannot provide a
// closed, active tenant set for an explicit fleet contract migration.
var ErrContractMigrationFleetUnstable = errors.New("tenant contract migration fleet is unstable")

// ContractMigrationFleet is an internal-operator view used by the CLI. It is
// deliberately not JSON serializable: raw identifiers must never be emitted by
// the release gate.
type ContractMigrationFleet struct {
	Inventories []TenantInventory `json:"-"`
	PeerSchemas []string          `json:"-"`
	Digest      string            `json:"-"`
}

// TenantReference returns the stable one-way correlation token used in audit
// and migration summaries.
func TenantReference(i TenantInventory) string { return tenantRef(i.TenantID, i.Schema) }

// PrepareContractMigrationFleet captures the exact active fleet before a
// migration. Old contract versions are allowed, but provisioning activity,
// non-active registry rows, duplicate identities, or non-isolated tenants are
// rejected because they make the migration target ambiguous.
func (m *Migrator) PrepareContractMigrationFleet(ctx context.Context) (ContractMigrationFleet, error) {
	s, err := m.readFleetSnapshot(ctx)
	if err != nil {
		return ContractMigrationFleet{}, err
	}
	return prepareContractMigrationFleetSnapshot(s)
}

// ValidateContractMigrationFleet proves that the structural fleet captured by
// preflight has not changed. Contract metadata and tenant catalog objects are
// intentionally excluded because the migration itself changes them.
func (m *Migrator) ValidateContractMigrationFleet(ctx context.Context, expectedDigest string) (ContractMigrationFleet, error) {
	s, err := m.readFleetSnapshot(ctx)
	if err != nil {
		return ContractMigrationFleet{}, err
	}
	fleet, err := prepareContractMigrationFleetSnapshot(s)
	if err != nil {
		return ContractMigrationFleet{}, err
	}
	if expectedDigest == "" || fleet.Digest != expectedDigest {
		return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
	}
	return fleet, nil
}

func prepareContractMigrationFleetSnapshot(s fleetSnapshot) (ContractMigrationFleet, error) {
	if len(s.Operations) != 0 {
		return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
	}
	for _, finding := range evaluateFleetSnapshot(s) {
		if finding.Code != "contract_version_mismatch" && finding.Code != "contract_checksum_mismatch" && finding.Code != "marker_column_missing" && finding.Code != "marker_column_nullability_mismatch" {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
	}
	activeSchemas := map[string]int{}
	for _, r := range s.Registry {
		if r.State != "active" || r.TenantID == nil || !r.IsolationReady {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		if err := storage.ValidateSchemaContractTransition(r.ContractVersion, r.ContractChecksum, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		activeSchemas[r.Schema]++
	}
	if len(activeSchemas) == 0 {
		return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
	}
	markers := map[string][]auditMarkerRow{}
	for _, marker := range s.Markers {
		markers[marker.Schema] = append(markers[marker.Schema], marker)
	}
	inventories := make([]TenantInventory, 0, len(activeSchemas))
	for schema, count := range activeSchemas {
		if count != 1 {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		i, ok := snapshotProbeInventory(s, schema)
		if !ok {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		var full TenantInventory
		for _, candidate := range s.Inventories {
			if candidate.Schema == schema {
				full = candidate
				break
			}
		}
		if full.Schema == "" || len(markers[schema]) != 1 {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		if !migrationCompatibleMarkerShape(markers[schema][0]) {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		identity, canonical := canonicalMarkerIdentity(markers[schema][0])
		if !canonical || identity.TenantID == nil || *identity.TenantID != i.TenantID || identity.OperationID == nil {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		if err := storage.ValidateSchemaContractTransition(identity.ContractVersion, identity.ContractChecksum, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		full.Marker = &TenantMarkerMetadata{
			TenantID:         *identity.TenantID,
			OperationID:      *identity.OperationID,
			ContractVersion:  identity.ContractVersion,
			ContractChecksum: identity.ContractChecksum,
		}
		if len(inventorySafetyFindings(full)) != 0 {
			return ContractMigrationFleet{}, ErrContractMigrationFleetUnstable
		}
		inventories = append(inventories, full)
	}
	sort.Slice(inventories, func(i, j int) bool { return inventories[i].Schema < inventories[j].Schema })
	return ContractMigrationFleet{Inventories: inventories, PeerSchemas: fleetPeerSchemas(s), Digest: contractMigrationStructuralDigest(s)}, nil
}

func migrationCompatibleMarkerShape(marker auditMarkerRow) bool {
	if marker.RelKind != "r" || marker.Persistence != "p" {
		return false
	}
	identity, canonical := canonicalMarkerIdentity(marker)
	if !canonical || (identity.ContractVersion == nil) != (identity.ContractChecksum == nil) {
		return false
	}
	if err := storage.ValidateSchemaContractTransition(identity.ContractVersion, identity.ContractChecksum, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
		return false
	}
	if len(marker.Issues) == 0 {
		return true
	}
	issues := sortedCopy(marker.Issues)
	missing := []string{"marker_column_missing:schema_contract_checksum", "marker_column_missing:schema_contract_version"}
	nullable := []string{"marker_column_nullability_mismatch:schema_contract_checksum:YES", "marker_column_nullability_mismatch:schema_contract_version:YES"}
	if equalStrings(issues, missing) {
		return identity.ContractVersion == nil && identity.ContractChecksum == nil
	}
	if !equalStrings(issues, nullable) {
		return false
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
}

func contractMigrationStructuralDigest(s fleetSnapshot) string {
	type registryFact struct {
		IdentityHash      string
		TenantID          any
		Schema            string
		Role              string
		CredentialVersion any
		IsolationReady    bool
		State             string
	}
	type markerIdentityFact struct {
		Singleton   any
		TenantID    any
		OperationID any
	}
	type markerFact struct {
		Schema  string
		RelKind string
		Rows    []markerIdentityFact
		Issues  []string
	}
	type fact struct {
		Kind  string
		Value any
	}
	rows := make([]string, 0, len(s.Registry)+len(s.Markers)+len(s.Roles)+len(s.Operations))
	add := func(kind string, value any) {
		b, _ := json.Marshal(fact{Kind: kind, Value: value})
		rows = append(rows, string(b))
	}
	for _, r := range s.Registry {
		add("registry", registryFact{r.IdentityHash, r.TenantID, r.Schema, r.Role, r.CredentialVersion, r.IsolationReady, r.State})
	}
	for _, marker := range s.Markers {
		issues := sortedCopy(marker.Issues)
		if migrationCompatibleMarkerShape(marker) {
			issues = []string{}
		}
		m := markerFact{Schema: marker.Schema, RelKind: marker.RelKind, Issues: issues}
		for _, row := range marker.Rows {
			m.Rows = append(m.Rows, markerIdentityFact{row.Singleton, row.TenantID, row.OperationID})
		}
		sortJSON(m.Rows)
		add("marker", m)
	}
	for _, role := range s.Roles {
		add("role", role)
	}
	for _, operation := range s.Operations {
		add("operation", operation)
	}
	sort.Strings(rows)
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// MigrateTenantContract upgrades one already-isolated ACTIVE tenant. It never
// performs the initial role/ownership cutover; ApplyRestrictedTenant remains
// the only path for that operation.
func (m *Migrator) MigrateTenantContract(ctx context.Context, i TenantInventory, peerSchemas []string) error {
	if err := validateInventoryIdentity(i); err != nil {
		return err
	}
	if i.Registry.State != "active" || !i.Registry.IsolationReady || i.Marker == nil || i.Marker.TenantID != i.TenantID {
		return ErrContractMigrationFleetUnstable
	}
	if err := storage.ValidateSchemaContractTransition(i.Registry.ContractVersion, i.Registry.ContractChecksum, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
		return err
	}
	password, err := m.deriver.Derive(i.TenantID, i.Role, i.CredentialVersion)
	if err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(m.tenantBase)
	if err != nil {
		return &MigrationConnectionError{stage: "parse tenant contract", cause: err}
	}
	cfg.ConnConfig.User = i.Role
	cfg.ConnConfig.Password = password
	cfg.ConnConfig.RuntimeParams["search_path"] = i.Schema
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return &MigrationConnectionError{stage: "open tenant contract", cause: err}
	}
	db := storage.NewFromPool(pool)
	defer db.Close()
	if err = pool.Ping(ctx); err != nil {
		return &MigrationConnectionError{stage: "ping tenant contract", cause: err}
	}
	if err = db.AssertIdentity(ctx, i.Role, i.Schema); err != nil {
		return err
	}
	if err = db.EnsureSchemaContractContext(ctx); err != nil {
		return err
	}
	old := storage.SchemaContractState{Version: i.Marker.ContractVersion, Checksum: i.Marker.ContractChecksum}
	if err = storage.MigrateTenantIdentityMarker(ctx, pool, "", i.TenantID, i.Marker.OperationID, old); err != nil {
		return err
	}
	others := make([]string, 0, len(peerSchemas))
	for _, schema := range peerSchemas {
		if schema != i.Schema {
			others = append(others, schema)
		}
	}
	if _, err = m.VerifyRestrictedTenantAll(ctx, i, others); err != nil {
		return err
	}
	return m.advanceRegistryContract(ctx, i, true)
}
