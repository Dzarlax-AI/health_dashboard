package tenants

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

const (
	AuditStatusPass = "pass"
	AuditStatusFail = "fail"
)

// AuditFinding is deliberately identifier-free except for TenantRef, a
// stable one-way correlation token. Remediation tooling must opt in to raw
// catalog identifiers through a separate, non-public path.
type AuditFinding struct {
	Code      string `json:"code"`
	Scope     string `json:"scope"`
	TenantRef string `json:"tenant_ref,omitempty"`
}

type AuditCounts struct {
	RegistryByState map[string]int `json:"registry_by_state"`
	Markers         int            `json:"markers"`
	Roles           int            `json:"roles"`
	Operations      int            `json:"nonterminal_operations"`
}

type AuditProbeTotals struct {
	Attempted int `json:"attempted"`
	Denied    int `json:"denied"`
	Failed    int `json:"failed"`
}

// IsolationProbeResult exposes counts only. It intentionally carries neither
// SQL text nor foreign schema identifiers.
type IsolationProbeResult struct {
	Total               int
	Denied              int
	RegistryFailures    int
	CrossTenantFailures int
	OperationalFailures int
}

type AuditResult struct {
	Status                 string           `json:"status"`
	TargetContractVersion  int              `json:"target_contract_version"`
	TargetContractChecksum string           `json:"target_contract_checksum"`
	StartDigest            string           `json:"start_digest"`
	EndDigest              string           `json:"end_digest"`
	Counts                 AuditCounts      `json:"counts"`
	Probes                 AuditProbeTotals `json:"probes"`
	ElapsedMS              int64            `json:"elapsed_ms"`
	Findings               []AuditFinding   `json:"findings"`
}

type AuditOperationalError struct {
	stage string
	cause error
}

func (e *AuditOperationalError) Error() string {
	return "tenant fleet audit " + e.stage + " failed (details redacted)"
}
func (e *AuditOperationalError) Unwrap() error { return e.cause }

// Internal snapshot rows intentionally have no JSON tags and never leave this
// package. Usernames are immediately domain-hashed for drift detection;
// email/authentication columns are never read.
type auditRegistryRow struct {
	IdentityHash      string
	TenantID          *uuid.UUID
	Schema            string
	Role              string
	CredentialVersion *int
	IsolationReady    bool
	IsPrimary         bool
	State             string
	ContractVersion   *int
	ContractChecksum  *string
}
type auditMarkerRow struct {
	Schema  string
	RelKind string
	Rows    []auditMarkerIdentity
	Issues  []string
}
type auditMarkerIdentity struct {
	Singleton        *bool
	TenantID         *uuid.UUID
	OperationID      *uuid.UUID
	ContractVersion  *int
	ContractChecksum *string
}
type auditRoleRow struct {
	TenantID    uuid.UUID
	Role        string
	OperationID uuid.UUID
	Valid       bool
}
type auditOperationRow struct {
	OperationID       uuid.UUID
	TenantID          uuid.UUID
	Schema            string
	Role              string
	CredentialVersion int
	State             string
}
type fleetSnapshot struct {
	Registry    []auditRegistryRow
	Markers     []auditMarkerRow
	Roles       []auditRoleRow
	Operations  []auditOperationRow
	Inventories []TenantInventory
}

func tenantRef(id uuid.UUID, schema string) string {
	sum := sha256.Sum256([]byte("health-fleet-audit-tenant-v1\x00" + id.String() + "\x00" + schema))
	return hex.EncodeToString(sum[:16])
}

func fleetDigest(s fleetSnapshot) string {
	type fact struct {
		Kind  string `json:"kind"`
		Value any    `json:"value"`
	}
	rows := make([]string, 0, len(s.Registry)+len(s.Markers)+len(s.Roles)+len(s.Operations)+len(s.Inventories))
	add := func(kind string, value any) {
		b, _ := json.Marshal(fact{Kind: kind, Value: value})
		rows = append(rows, string(b))
	}
	for _, r := range s.Registry {
		add("registry", r)
	}
	for _, r := range s.Markers {
		add("marker", canonicalMarker(r))
	}
	for _, r := range s.Roles {
		add("role", r)
	}
	for _, r := range s.Operations {
		add("operation", r)
	}
	for _, r := range s.Inventories {
		add("inventory", canonicalAuditInventory(r))
	}
	sort.Strings(rows)
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func canonicalMarker(in auditMarkerRow) auditMarkerRow {
	out := in
	out.Issues = sortedCopy(in.Issues)
	out.Rows = append([]auditMarkerIdentity(nil), in.Rows...)
	sortJSON(out.Rows)
	return out
}

func canonicalAuditInventory(in TenantInventory) TenantInventory {
	out := in
	out.SchemaRawACL = sortedCopy(in.SchemaRawACL)
	out.Blockers = sortedCopy(in.Blockers)
	out.SchemaACL = append([]ACLRecord(nil), in.SchemaACL...)
	sortJSON(out.SchemaACL)
	out.Objects = append([]OwnedObject(nil), in.Objects...)
	for idx := range out.Objects {
		out.Objects[idx].RawACL = sortedCopy(out.Objects[idx].RawACL)
		out.Objects[idx].ProConfig = sortedCopy(out.Objects[idx].ProConfig)
	}
	sortJSON(out.Objects)
	out.Grants = append([]GrantRecord(nil), in.Grants...)
	sortJSON(out.Grants)
	out.DefaultPrivileges = append([]DefaultPrivilege(nil), in.DefaultPrivileges...)
	sortJSON(out.DefaultPrivileges)
	out.Memberships = append([]MembershipRecord(nil), in.Memberships...)
	sortJSON(out.Memberships)
	out.RoleCatalog.Config = sortedCopy(in.RoleCatalog.Config)
	return out
}

func sortJSON[T any](rows []T) {
	sort.Slice(rows, func(i, j int) bool {
		a, _ := json.Marshal(rows[i])
		b, _ := json.Marshal(rows[j])
		return string(a) < string(b)
	})
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func findingLess(a, b AuditFinding) bool {
	return a.Code+a.Scope+a.TenantRef < b.Code+b.Scope+b.TenantRef
}
func normalizedFindings(in []AuditFinding) []AuditFinding {
	for idx := range in {
		if in[idx].TenantRef == "" {
			in[idx].TenantRef = tenantRef(uuid.Nil, "")
		}
	}
	sort.Slice(in, func(i, j int) bool { return findingLess(in[i], in[j]) })
	out := in[:0]
	for _, f := range in {
		if len(out) == 0 || out[len(out)-1] != f {
			out = append(out, f)
		}
	}
	return out
}

func evaluateFleetSnapshot(s fleetSnapshot) []AuditFinding {
	var out []AuditFinding
	activeBySchema := map[string][]auditRegistryRow{}
	registryTenant, registrySchema, registryRole := map[uuid.UUID]int{}, map[string]int{}, map[string]int{}
	for _, r := range s.Registry {
		ref := tenantRef(uuid.Nil, r.Schema)
		if r.TenantID != nil {
			ref = tenantRef(*r.TenantID, r.Schema)
			registryTenant[*r.TenantID]++
		}
		registrySchema[r.Schema]++
		if r.Role != "" {
			registryRole[r.Role]++
		}
		if r.State != "active" {
			code := "registry_nonterminal"
			if r.State == "failed" {
				code = "registry_state_failed"
			} else if r.State != "pending" && r.State != "provisioning" {
				code = "registry_state_invalid"
			}
			out = append(out, AuditFinding{code, "registry", ref})
			continue
		}
		activeBySchema[r.Schema] = append(activeBySchema[r.Schema], r)
		if r.TenantID == nil || *r.TenantID == uuid.Nil {
			out = append(out, AuditFinding{"registry_identity_invalid", "registry", ref})
			continue
		}
		id := *r.TenantID
		if registry.ValidateSchemaName(r.Schema) != nil {
			out = append(out, AuditFinding{"registry_schema_invalid", "registry", ref})
		}
		if r.Role != TenantRoleName(id) {
			out = append(out, AuditFinding{"registry_role_invalid", "registry", ref})
		}
		if r.CredentialVersion == nil || *r.CredentialVersion <= 0 {
			out = append(out, AuditFinding{"credential_version_mismatch", "credential", ref})
		}
		if !r.IsolationReady {
			out = append(out, AuditFinding{"isolation_not_ready", "registry", ref})
		}
		if r.ContractVersion == nil || *r.ContractVersion != storage.SchemaContractVersion {
			out = append(out, AuditFinding{"contract_version_mismatch", "contract", ref})
		}
		if r.ContractChecksum == nil || *r.ContractChecksum != storage.SchemaContractChecksum() {
			out = append(out, AuditFinding{"contract_checksum_mismatch", "contract", ref})
		}
	}
	for id, n := range registryTenant {
		if n > 1 {
			out = append(out, AuditFinding{"duplicate_registry_tenant", "registry", tenantRef(id, "")})
		}
	}
	for schema, n := range registrySchema {
		if n > 1 {
			out = append(out, AuditFinding{"duplicate_registry_schema", "registry", tenantRef(uuid.Nil, schema)})
		}
	}
	for _, n := range registryRole {
		if n > 1 {
			out = append(out, AuditFinding{"duplicate_registry_role", "registry", tenantRef(uuid.Nil, "")})
		}
	}

	markersBySchema := map[string][]auditMarkerRow{}
	markerTenants := map[uuid.UUID]int{}
	for _, x := range s.Markers {
		markersBySchema[x.Schema] = append(markersBySchema[x.Schema], x)
	}
	for schema, xs := range markersBySchema {
		identity, identityOK := canonicalMarkerIdentity(xs[0])
		markerID := uuid.Nil
		if identity.TenantID != nil {
			markerID = *identity.TenantID
		}
		ref := tenantRef(markerID, schema)
		if len(xs) > 1 {
			out = append(out, AuditFinding{"duplicate_marker_schema", "schema", ref})
		}
		for _, issue := range xs[0].Issues {
			out = append(out, AuditFinding{strings.SplitN(issue, ":", 2)[0], "schema", ref})
		}
		if len(xs[0].Rows) == 0 {
			out = append(out, AuditFinding{"marker_empty", "schema", ref})
		}
		if len(xs[0].Rows) > 1 {
			out = append(out, AuditFinding{"marker_multiple_rows", "schema", ref})
		}
		trueSingletons := 0
		for _, row := range xs[0].Rows {
			if row.Singleton != nil && *row.Singleton {
				trueSingletons++
			}
		}
		if trueSingletons > 1 {
			out = append(out, AuditFinding{"marker_multiple_singleton_rows", "schema", ref})
		}
		if len(xs[0].Rows) == 1 && (xs[0].Rows[0].Singleton == nil || !*xs[0].Rows[0].Singleton) {
			out = append(out, AuditFinding{"marker_singleton_invalid", "schema", ref})
		}
		if identityOK {
			markerTenants[markerID]++
		}
		rr := activeBySchema[schema]
		if len(rr) == 0 {
			out = append(out, AuditFinding{"marker_registry_missing", "schema", ref})
			continue
		}
		if !identityOK || rr[0].TenantID == nil || markerID != *rr[0].TenantID {
			out = append(out, AuditFinding{"marker_identity_mismatch", "schema", ref})
		}
		if identity.ContractVersion == nil || *identity.ContractVersion != storage.SchemaContractVersion {
			out = append(out, AuditFinding{"contract_version_mismatch", "schema", ref})
		}
		if identity.ContractChecksum == nil || *identity.ContractChecksum != storage.SchemaContractChecksum() {
			out = append(out, AuditFinding{"contract_checksum_mismatch", "schema", ref})
		}
	}
	for id, n := range markerTenants {
		if n > 1 {
			out = append(out, AuditFinding{"duplicate_marker_tenant", "schema", tenantRef(id, "")})
		}
	}

	rolesByName := map[string][]auditRoleRow{}
	roleTenants := map[uuid.UUID]int{}
	for _, x := range s.Roles {
		rolesByName[x.Role] = append(rolesByName[x.Role], x)
		roleTenants[x.TenantID]++
		if !x.Valid {
			out = append(out, AuditFinding{"role_marker_invalid", "role", tenantRef(x.TenantID, "")})
		}
	}
	for role, xs := range rolesByName {
		ref := tenantRef(xs[0].TenantID, "")
		if len(xs) > 1 {
			out = append(out, AuditFinding{"duplicate_role", "role", ref})
		}
		matched := false
		for _, rr := range s.Registry {
			if rr.State == "active" && rr.Role == role {
				matched = true
				if rr.TenantID == nil || *rr.TenantID != xs[0].TenantID {
					out = append(out, AuditFinding{"role_identity_mismatch", "role", ref})
				}
				break
			}
		}
		if !matched {
			out = append(out, AuditFinding{"role_registry_missing", "role", ref})
		}
	}
	for id, n := range roleTenants {
		if n > 1 {
			out = append(out, AuditFinding{"duplicate_role_tenant", "role", tenantRef(id, "")})
		}
	}

	for schema, rr := range activeBySchema {
		ref := tenantRef(uuid.Nil, schema)
		if rr[0].TenantID != nil {
			ref = tenantRef(*rr[0].TenantID, schema)
		}
		if len(markersBySchema[schema]) == 0 {
			out = append(out, AuditFinding{"registry_marker_missing", "schema", ref})
		}
		if len(rolesByName[rr[0].Role]) == 0 {
			out = append(out, AuditFinding{"role_registry_missing", "role", ref})
		}
		marker, markerOK := canonicalMarkerIdentity(firstMarker(markersBySchema[schema]))
		if markerOK && marker.OperationID != nil && len(rolesByName[rr[0].Role]) == 1 && *marker.OperationID != rolesByName[rr[0].Role][0].OperationID {
			out = append(out, AuditFinding{"role_marker_operation_mismatch", "role", ref})
		}
	}
	for _, op := range s.Operations {
		out = append(out, AuditFinding{"registry_nonterminal", "operation", tenantRef(op.TenantID, "")})
	}
	return normalizedFindings(out)
}

func firstMarker(rows []auditMarkerRow) auditMarkerRow {
	if len(rows) == 0 {
		return auditMarkerRow{}
	}
	return rows[0]
}
func canonicalMarkerIdentity(marker auditMarkerRow) (auditMarkerIdentity, bool) {
	if len(marker.Rows) != 1 {
		return auditMarkerIdentity{}, false
	}
	r := marker.Rows[0]
	return r, r.Singleton != nil && *r.Singleton && r.TenantID != nil && *r.TenantID != uuid.Nil && r.OperationID != nil && *r.OperationID != uuid.Nil
}

func inventorySafetyFindings(i TenantInventory) []AuditFinding {
	ref := tenantRef(i.TenantID, i.Schema)
	var out []AuditFinding
	if len(i.Blockers) > 0 {
		out = append(out, AuditFinding{"inventory_blocker", "inventory", ref})
	}
	if i.SchemaOwner != i.Role {
		out = append(out, AuditFinding{"unexpected_owner", "schema", ref})
	}
	for _, o := range i.Objects {
		if o.Owner != i.Role {
			out = append(out, AuditFinding{"unexpected_owner", "object", ref})
		}
	}
	r := i.RoleCatalog
	if !r.Exists || !r.Login || !r.Inherit || r.Superuser || r.CreateRole || r.CreateDB || r.Replication || r.BypassRLS || r.ConnLimit != -1 || r.ValidUntil != nil || len(r.Config) > 0 || len(i.Memberships) > 0 {
		out = append(out, AuditFinding{"unsafe_role", "role", ref})
	}
	allowedGrant := func(g ACLRecord) bool { return g.Grantee == i.Role && g.Grantor == i.Role && !g.Grantable }
	for _, g := range i.SchemaACL {
		if !allowedGrant(g) || (g.Privilege != "USAGE" && g.Privilege != "CREATE") {
			out = append(out, AuditFinding{"unexpected_grant", "acl", ref})
		}
	}
	for _, g := range i.Grants {
		if !allowedGrant(g) {
			out = append(out, AuditFinding{"unexpected_grant", "acl", ref})
		}
	}
	for _, d := range i.DefaultPrivileges {
		if d.Owner != i.Role || d.Schema != i.Schema || d.Grantor != i.Role || d.Grantee != i.Role || d.Grantable || (d.ObjectType != "r" && d.ObjectType != "S" && d.ObjectType != "f") {
			out = append(out, AuditFinding{"default_acl_mismatch", "acl", ref})
		}
	}
	return normalizedFindings(out)
}

func countsForSnapshot(s fleetSnapshot) AuditCounts {
	c := AuditCounts{RegistryByState: map[string]int{}, Markers: len(s.Markers), Roles: len(s.Roles), Operations: len(s.Operations)}
	for _, r := range s.Registry {
		state := r.State
		if state != "pending" && state != "provisioning" && state != "active" && state != "failed" {
			state = "unknown"
		}
		c.RegistryByState[state]++
	}
	return c
}

// AuditFleet performs the core exact-set audit. It has no output side effects;
// callers decide how to render the sanitized result and translate fail status
// into an exit code.
func (m *Migrator) AuditFleet(ctx context.Context) (AuditResult, error) {
	started := time.Now()
	result := AuditResult{Status: AuditStatusFail, TargetContractVersion: storage.SchemaContractVersion, TargetContractChecksum: storage.SchemaContractChecksum()}
	start, err := m.readFleetSnapshot(ctx)
	if err != nil {
		result.ElapsedMS = time.Since(started).Milliseconds()
		return result, &AuditOperationalError{"start snapshot", err}
	}
	result.StartDigest = fleetDigest(start)
	result.Counts = countsForSnapshot(start)
	result.Findings = evaluateFleetSnapshot(start)
	var operational error
	activeSchemaSet := map[string]bool{}
	for _, r := range start.Registry {
		if r.State == "active" && r.TenantID != nil {
			activeSchemaSet[r.Schema] = true
		}
	}
	activeSchemas := make([]string, 0, len(activeSchemaSet))
	for schema := range activeSchemaSet {
		activeSchemas = append(activeSchemas, schema)
	}
	sort.Strings(activeSchemas)
	peerSchemas := fleetPeerSchemas(start)
	inventoryBySchema := map[string]TenantInventory{}
	for _, inventory := range start.Inventories {
		inventoryBySchema[inventory.Schema] = inventory
	}
	for _, schema := range activeSchemas {
		_, sourceOK := snapshotProbeInventory(start, schema)
		if !sourceOK {
			continue
		}
		i, ok := inventoryBySchema[schema]
		if !ok {
			continue
		}
		safety := inventorySafetyFindings(i)
		result.Findings = append(result.Findings, safety...)
		others := make([]string, 0, len(peerSchemas)-1)
		for _, x := range peerSchemas {
			if x != schema {
				others = append(others, x)
			}
		}
		probe, probeErr := m.verifyRestrictedTenantAll(ctx, i, others, markerShapeKnownInvalid(start, schema))
		result.Probes.Attempted += probe.Total
		result.Probes.Denied += probe.Denied
		result.Probes.Failed += probe.RegistryFailures + probe.CrossTenantFailures + probe.OperationalFailures
		ref := tenantRef(i.TenantID, i.Schema)
		if probe.RegistryFailures > 0 {
			result.Findings = append(result.Findings, AuditFinding{"registry_access_allowed", "isolation", ref})
		}
		if probe.CrossTenantFailures > 0 {
			result.Findings = append(result.Findings, AuditFinding{"cross_tenant_access_allowed", "isolation", ref})
		}
		var logical *restrictedVerificationError
		logicalFailure := probeErr != nil && errors.As(probeErr, &logical)
		if logicalFailure {
			result.Findings = append(result.Findings, AuditFinding{logical.code, "isolation", ref})
		}
		if probe.OperationalFailures > 0 {
			result.Findings = append(result.Findings, AuditFinding{"isolation_probe_failed", "isolation", ref})
			if operational == nil {
				operational = probeErr
			}
		}
		if probeErr != nil && probe.Total == 0 && !logicalFailure {
			result.Findings = append(result.Findings, AuditFinding{"isolation_probe_failed", "isolation", ref})
			if operational == nil {
				operational = probeErr
			}
		}
	}
	if m.auditBeforeEndSnapshot != nil {
		if hookErr := m.auditBeforeEndSnapshot(ctx); hookErr != nil && operational == nil {
			operational = hookErr
		}
	}
	end, endErr := m.readFleetSnapshot(ctx)
	if endErr != nil {
		result.Findings = append(result.Findings, AuditFinding{"inventory_reread_failed", "inventory", ""})
		if operational == nil {
			operational = endErr
		}
	} else {
		result.EndDigest = fleetDigest(end)
		if result.EndDigest != result.StartDigest {
			result.Findings = append(result.Findings, AuditFinding{"inventory_changed", "inventory", ""})
		}
	}
	result.Findings = normalizedFindings(result.Findings)
	if len(result.Findings) == 0 {
		result.Status = AuditStatusPass
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	if operational != nil {
		return result, &AuditOperationalError{"catalog or probe", operational}
	}
	return result, nil
}

func markerShapeKnownInvalid(s fleetSnapshot, schema string) bool {
	for _, marker := range s.Markers {
		if marker.Schema == schema {
			_, canonical := canonicalMarkerIdentity(marker)
			return marker.RelKind != "r" || len(marker.Issues) > 0 || !canonical
		}
	}
	return true
}

func fleetPeerSchemas(s fleetSnapshot) []string {
	set := map[string]bool{}
	for _, row := range s.Registry {
		if row.State == "active" && row.Schema != "" {
			set[row.Schema] = true
		}
	}
	for _, marker := range s.Markers {
		if marker.Schema != "" {
			set[marker.Schema] = true
		}
	}
	out := make([]string, 0, len(set))
	for schema := range set {
		out = append(out, schema)
	}
	sort.Strings(out)
	return out
}

func snapshotProbeInventory(s fleetSnapshot, schema string) (TenantInventory, bool) {
	var matches []auditRegistryRow
	for _, r := range s.Registry {
		if r.State == "active" && r.Schema == schema {
			matches = append(matches, r)
		}
	}
	if len(matches) != 1 || matches[0].TenantID == nil || *matches[0].TenantID == uuid.Nil || registry.ValidateSchemaName(schema) != nil || matches[0].Role != TenantRoleName(*matches[0].TenantID) || matches[0].CredentialVersion == nil || *matches[0].CredentialVersion <= 0 {
		return TenantInventory{}, false
	}
	for _, r := range s.Registry {
		if r.Schema != schema && r.TenantID != nil && *r.TenantID == *matches[0].TenantID {
			return TenantInventory{}, false
		}
		if r.Schema != schema && r.Role == matches[0].Role {
			return TenantInventory{}, false
		}
	}
	r := matches[0]
	i := TenantInventory{Schema: schema, TenantID: *r.TenantID, Role: r.Role, CredentialVersion: *r.CredentialVersion, IsPrimary: r.IsPrimary, Registry: RegistryMetadata{TenantID: *r.TenantID, Schema: schema, Role: r.Role, CredentialVersion: *r.CredentialVersion, IsolationReady: r.IsolationReady, IsPrimary: r.IsPrimary, State: r.State, ContractVersion: r.ContractVersion, ContractChecksum: r.ContractChecksum}}
	return i, true
}

func readPhysicalMarker(ctx context.Context, catalog migrationCatalogReader, schema, relkind string) (auditMarkerRow, error) {
	marker := auditMarkerRow{Schema: schema, RelKind: relkind}
	if relkind != "r" {
		marker.Issues = []string{"marker_relation_kind_invalid:" + relkind}
		return marker, nil
	}
	markerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type columnShape struct{ dataType, nullable, defaultExpr string }
	shapes := map[string]columnShape{}
	rows, err := catalog.Query(markerCtx, `SELECT column_name,data_type,is_nullable,COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, storage.TenantIdentityTable)
	if err != nil {
		return marker, err
	}
	for rows.Next() {
		var name string
		var shape columnShape
		if err = rows.Scan(&name, &shape.dataType, &shape.nullable, &shape.defaultExpr); err != nil {
			rows.Close()
			return marker, err
		}
		shapes[name] = shape
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return marker, err
	}
	rows.Close()
	expected := map[string]string{"singleton": "boolean", "tenant_id": "uuid", "operation_id": "uuid", "schema_contract_version": "integer", "schema_contract_checksum": "text"}
	for name, dataType := range expected {
		shape, ok := shapes[name]
		if !ok {
			marker.Issues = append(marker.Issues, "marker_column_missing:"+name)
			continue
		}
		if shape.dataType != dataType {
			marker.Issues = append(marker.Issues, "marker_column_type_mismatch:"+name+":"+shape.dataType)
		}
		if shape.nullable != "NO" {
			marker.Issues = append(marker.Issues, "marker_column_nullability_mismatch:"+name+":"+shape.nullable)
		}
	}
	_, hasVersion := shapes["schema_contract_version"]
	_, hasChecksum := shapes["schema_contract_checksum"]
	if hasVersion != hasChecksum {
		marker.Issues = append(marker.Issues, "marker_contract_columns_partial")
	}
	defaultExpr := strings.ToLower(strings.TrimSpace(shapes["singleton"].defaultExpr))
	if defaultExpr != "true" && defaultExpr != "true::boolean" {
		marker.Issues = append(marker.Issues, "marker_singleton_default_mismatch")
	}
	var hasPK, hasCheck bool
	constraintQuery := `SELECT EXISTS(SELECT 1 FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname='singleton' WHERE n.nspname=$1 AND c.relname=$2 AND con.contype='p' AND array_length(con.conkey,1)=1 AND a.attnum=ANY(con.conkey)),EXISTS(SELECT 1 FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND con.contype='c' AND regexp_replace(pg_get_expr(con.conbin,con.conrelid),'[() ]','','g') IN ('singleton','singleton=true','true=singleton'))`
	if err = catalog.QueryRow(markerCtx, constraintQuery, schema, storage.TenantIdentityTable).Scan(&hasPK, &hasCheck); err != nil {
		return marker, err
	}
	if !hasPK {
		marker.Issues = append(marker.Issues, "marker_singleton_primary_key_missing")
	}
	if !hasCheck {
		marker.Issues = append(marker.Issues, "marker_singleton_check_missing")
	}
	expr := func(name, want, fallback string) string {
		if shape, ok := shapes[name]; ok && shape.dataType == want {
			return quoteIdent(name)
		}
		return fallback
	}
	table := qualified(schema, storage.TenantIdentityTable)
	var boundedCount int
	if err = catalog.QueryRow(markerCtx, "SELECT count(*) FROM (SELECT 1 FROM "+table+" LIMIT 2) bounded").Scan(&boundedCount); err != nil {
		return marker, err
	}
	if boundedCount > 0 {
		q := "SELECT " + strings.Join([]string{expr("singleton", "boolean", "NULL::boolean"), expr("tenant_id", "uuid", "NULL::uuid"), expr("operation_id", "uuid", "NULL::uuid"), expr("schema_contract_version", "integer", "NULL::integer"), expr("schema_contract_checksum", "text", "NULL::text")}, ",") + " FROM " + table + " LIMIT 2"
		rows, err = catalog.Query(markerCtx, q)
		if err != nil {
			return marker, err
		}
		for rows.Next() {
			var identity auditMarkerIdentity
			if err = rows.Scan(&identity.Singleton, &identity.TenantID, &identity.OperationID, &identity.ContractVersion, &identity.ContractChecksum); err != nil {
				rows.Close()
				return marker, err
			}
			marker.Rows = append(marker.Rows, identity)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return marker, err
		}
		rows.Close()
	}
	marker.Issues = sortedCopy(marker.Issues)
	return marker, nil
}

func (m *Migrator) readFleetSnapshot(ctx context.Context) (fleetSnapshot, error) {
	tx, err := m.admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fleetSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	var s fleetSnapshot
	rows, err := tx.Query(ctx, `SELECT username,tenant_id,schema_name,db_role,db_credential_version,db_isolation_ready,is_admin,provisioning_state,schema_contract_version,schema_contract_checksum FROM health_registry.users ORDER BY schema_name,username`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var r auditRegistryRow
		var username string
		var role *string
		if err = rows.Scan(&username, &r.TenantID, &r.Schema, &role, &r.CredentialVersion, &r.IsolationReady, &r.IsPrimary, &r.State, &r.ContractVersion, &r.ContractChecksum); err != nil {
			rows.Close()
			return s, err
		}
		if role != nil {
			r.Role = *role
		}
		identitySum := sha256.Sum256([]byte("health-fleet-audit-registry-v1\x00" + username))
		r.IdentityHash = hex.EncodeToString(identitySum[:])
		s.Registry = append(s.Registry, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT operation_id,tenant_id,schema_name,db_role,credential_version,state FROM health_registry.tenant_provisioning_operations WHERE state IN ('pending','provisioning') ORDER BY tenant_id,operation_id`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var x auditOperationRow
		if err = rows.Scan(&x.OperationID, &x.TenantID, &x.Schema, &x.Role, &x.CredentialVersion, &x.State); err != nil {
			rows.Close()
			return s, err
		}
		s.Operations = append(s.Operations, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT n.nspname,c.relkind::text FROM pg_namespace n JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=$1 ORDER BY n.nspname`, storage.TenantIdentityTable)
	if err != nil {
		return s, err
	}
	type markerRelation struct{ schema, relkind string }
	var relations []markerRelation
	for rows.Next() {
		var x markerRelation
		if err = rows.Scan(&x.schema, &x.relkind); err != nil {
			rows.Close()
			return s, err
		}
		relations = append(relations, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	for _, relation := range relations {
		marker, markerErr := readPhysicalMarker(ctx, tx, relation.schema, relation.relkind)
		if markerErr != nil {
			return s, markerErr
		}
		s.Markers = append(s.Markers, marker)
	}
	rows, err = tx.Query(ctx, `SELECT rolname,shobj_description(oid,'pg_authid') FROM pg_roles WHERE shobj_description(oid,'pg_authid') LIKE 'health-tenant-v1:%' ORDER BY rolname`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var role string
		var comment *string
		if err = rows.Scan(&role, &comment); err != nil {
			rows.Close()
			return s, err
		}
		x := auditRoleRow{Role: role}
		if comment != nil {
			parts := strings.Split(*comment, ":")
			if len(parts) == 3 {
				x.TenantID, err = uuid.Parse(parts[1])
				if err == nil {
					x.OperationID, err = uuid.Parse(parts[2])
				}
				x.Valid = err == nil && x.TenantID != uuid.Nil && x.OperationID != uuid.Nil && role == TenantRoleName(x.TenantID)
			}
		}
		s.Roles = append(s.Roles, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	activeSet := map[string]bool{}
	for _, row := range s.Registry {
		if row.State == "active" {
			activeSet[row.Schema] = true
		}
	}
	activeSchemas := make([]string, 0, len(activeSet))
	for schema := range activeSet {
		activeSchemas = append(activeSchemas, schema)
	}
	sort.Strings(activeSchemas)
	for _, schema := range activeSchemas {
		if _, ok := snapshotProbeInventory(s, schema); !ok {
			continue
		}
		inventory, inventoryErr := m.inventoryWithCatalog(ctx, tx, schema, false)
		if inventoryErr != nil {
			return s, inventoryErr
		}
		s.Inventories = append(s.Inventories, inventory)
	}
	if err = tx.Commit(ctx); err != nil {
		return s, err
	}
	return s, nil
}
