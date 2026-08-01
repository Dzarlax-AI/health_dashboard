package tenants

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
)

type FixedIdentityFinding struct {
	Code  string `json:"code"`
	Scope string `json:"scope"`
}

type FixedIdentityResult struct {
	Status   string                 `json:"status"`
	Findings []FixedIdentityFinding `json:"findings"`
}

type FixedIdentityVerifier struct {
	admin    *pgxpool.Pool
	registry *pgxpool.Pool
}

func NewFixedIdentityVerifier(ctx context.Context, adminDSN, registryDSN string) (*FixedIdentityVerifier, error) {
	open := func(dsn, expected string) (*pgxpool.Pool, error) {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			return nil, errors.New("parse identity verification database configuration (details redacted)")
		}
		if err = secureFixedPoolConfig(cfg); err != nil {
			return nil, err
		}
		cfg.MaxConns, cfg.MinConns = 1, 0
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, errors.New("open identity verification database (details redacted)")
		}
		if err = requireExactPoolIdentity(ctx, p, expected); err != nil {
			p.Close()
			return nil, err
		}
		return p, nil
	}
	a, err := open(adminDSN, DatabaseAdminRole)
	if err != nil {
		return nil, err
	}
	r, err := open(registryDSN, DatabaseRegistryRole)
	if err != nil {
		a.Close()
		return nil, err
	}
	return &FixedIdentityVerifier{admin: a, registry: r}, nil
}

func (v *FixedIdentityVerifier) Close() { v.admin.Close(); v.registry.Close() }

func (v *FixedIdentityVerifier) Verify(ctx context.Context, allowLegacyBridge bool) (FixedIdentityResult, error) {
	schemas, roles, metadataFindings, err := registeredTenantIdentities(ctx, v.registry)
	if err != nil {
		return FixedIdentityResult{Status: AuditStatusFail, Findings: []FixedIdentityFinding{}}, err
	}
	r, err := verifyFixedIdentityCatalog(ctx, v.admin, allowLegacyBridge, roles)
	if err != nil {
		return r, err
	}
	r.Findings = append(r.Findings, metadataFindings...)
	if findings, checkErr := registryTenantCatalogFindings(ctx, v.admin, schemas, roles); checkErr != nil {
		return r, checkErr
	} else {
		r.Findings = append(r.Findings, findings...)
	}
	probe, probeErr := verifyRegistryPoolDeniedAll(ctx, v.registry, schemas)
	if probe.CrossTenantFailures > 0 {
		r.Findings = append(r.Findings, FixedIdentityFinding{"registry_tenant_access_allowed", "isolation"})
	}
	if probe.OperationalFailures > 0 {
		r.Findings = append(r.Findings, FixedIdentityFinding{"registry_tenant_probe_failed", "isolation"})
		return normalizeFixedIdentityResult(r), probeErr
	}
	return normalizeFixedIdentityResult(r), nil
}

func registeredTenantIdentities(ctx context.Context, p *pgxpool.Pool) ([]string, map[string]bool, []FixedIdentityFinding, error) {
	rows, err := p.Query(ctx, `SELECT DISTINCT schema_name,tenant_id,db_role FROM health_registry.users ORDER BY schema_name,tenant_id,db_role`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var schemas []string
	roles := map[string]bool{}
	metadataInvalid := false
	for rows.Next() {
		var schema string
		var tenantID *uuid.UUID
		var role *string
		if err = rows.Scan(&schema, &tenantID, &role); err != nil {
			return nil, nil, nil, err
		}
		if !validRegisteredTenantIdentity(schema, tenantID, role) {
			metadataInvalid = true
			continue
		}
		schemas = append(schemas, schema)
		roles[*role] = true
	}
	if err = rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	// A schema may appear in more than one transitional row; preserve a stable
	// exact set for catalog and denial probes.
	sort.Strings(schemas)
	out := schemas[:0]
	for _, schema := range schemas {
		if len(out) == 0 || out[len(out)-1] != schema {
			out = append(out, schema)
		}
	}
	var findings []FixedIdentityFinding
	if metadataInvalid {
		findings = append(findings, FixedIdentityFinding{"registry_tenant_metadata_invalid", "registry"})
	}
	return out, roles, findings, nil
}

func validRegisteredTenantIdentity(schema string, tenantID *uuid.UUID, role *string) bool {
	return tenantID != nil && role != nil && *role == TenantRoleName(*tenantID) && registry.ValidateSchemaName(schema) == nil
}

func verifyFixedIdentityCatalog(ctx context.Context, p *pgxpool.Pool, allowLegacyBridge bool, allowedTenantRoles map[string]bool) (FixedIdentityResult, error) {
	r := FixedIdentityResult{Status: AuditStatusFail, Findings: []FixedIdentityFinding{}}
	var version int
	if err := p.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return r, err
	}
	if version < 160000 {
		r.Findings = append(r.Findings, FixedIdentityFinding{"postgres_version_unsupported", "database"})
	}
	for _, role := range []string{DatabaseAdminRole, DatabaseRegistryRole} {
		var login, su, cr, cdb, repl, bypass, inherit bool
		var limit int
		var valid *time.Time
		var config []string
		var comment *string
		err := p.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls,rolinherit,rolconnlimit,rolvaliduntil,coalesce(rolconfig,'{}'::text[]),shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, role).Scan(&login, &su, &cr, &cdb, &repl, &bypass, &inherit, &limit, &valid, &config, &comment)
		if errors.Is(err, pgx.ErrNoRows) {
			r.Findings = append(r.Findings, FixedIdentityFinding{"fixed_role_missing", "role"})
			continue
		}
		if err != nil {
			return r, err
		}
		wantCR := role == DatabaseAdminRole
		if !login || su || cr != wantCR || cdb || repl || bypass || !inherit || limit != -1 || valid != nil || len(config) > 0 {
			r.Findings = append(r.Findings, FixedIdentityFinding{"fixed_role_attributes_drift", "role"})
		}
		if comment == nil || *comment != databaseIdentityMarker {
			r.Findings = append(r.Findings, FixedIdentityFinding{"fixed_role_marker_mismatch", "role"})
		}
	}
	if findings, err := fixedDatabaseGrantFindings(ctx, p); err != nil {
		return r, err
	} else {
		r.Findings = append(r.Findings, findings...)
	}
	if findings, err := fixedMembershipFindings(ctx, p, allowLegacyBridge, allowedTenantRoles); err != nil {
		return r, err
	} else {
		r.Findings = append(r.Findings, findings...)
	}
	var registrySchemaOwned int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM pg_namespace n JOIN pg_roles o ON o.oid=n.nspowner WHERE n.nspname='health_registry' AND o.rolname='health_registry'`).Scan(&registrySchemaOwned); err != nil {
		return r, err
	}
	if registrySchemaOwned != 1 {
		r.Findings = append(r.Findings, FixedIdentityFinding{"registry_schema_owner_drift", "schema"})
	}
	if findings, err := registryRequiredDefaultACLFindings(ctx, p); err != nil {
		return r, err
	} else {
		r.Findings = append(r.Findings, findings...)
	}
	checks := []struct {
		code, scope, sql string
		args             []any
	}{
		{"registry_relation_owner_drift", "object", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles o ON o.oid=c.relowner WHERE n.nspname='health_registry' AND o.rolname<>'health_registry'`, nil},
		{"registry_routine_owner_drift", "object", `SELECT count(*) FROM pg_proc x JOIN pg_namespace n ON n.oid=x.pronamespace JOIN pg_roles o ON o.oid=x.proowner WHERE n.nspname='health_registry' AND o.rolname<>'health_registry'`, nil},
		{"registry_type_owner_drift", "object", `SELECT count(*) FROM pg_type x JOIN pg_namespace n ON n.oid=x.typnamespace JOIN pg_roles o ON o.oid=x.typowner WHERE n.nspname='health_registry' AND o.rolname<>'health_registry'`, nil},
		{"registry_default_acl_owner_drift", "acl", `SELECT count(*) FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace JOIN pg_roles o ON o.oid=d.defaclrole WHERE (n.nspname='health_registry' OR (d.defaclnamespace=0 AND o.rolname='health_registry')) AND o.rolname<>'health_registry'`, nil},
		{"registry_public_schema_access", "acl", `SELECT count(*) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a WHERE n.nspname='health_registry' AND a.grantee=0`, nil},
		{"registry_public_routine_access", "acl", `SELECT count(*) FROM pg_proc x JOIN pg_namespace n ON n.oid=x.pronamespace CROSS JOIN LATERAL aclexplode(coalesce(x.proacl,acldefault('f',x.proowner))) a WHERE n.nspname='health_registry' AND a.grantee=0`, nil},
		{"registry_public_type_access", "acl", `SELECT count(*) FROM pg_type x JOIN pg_namespace n ON n.oid=x.typnamespace CROSS JOIN LATERAL aclexplode(coalesce(x.typacl,acldefault('T',x.typowner))) a WHERE n.nspname='health_registry' AND x.typelem=0 AND a.grantee=0`, nil},
		{"registry_public_relation_access", "acl", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a WHERE n.nspname='health_registry' AND a.grantee=0`, nil},
		{"registry_public_default_acl_access", "acl", `SELECT count(*) FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace JOIN pg_roles owner ON owner.oid=d.defaclrole CROSS JOIN LATERAL aclexplode(d.defaclacl) a WHERE (n.nspname='health_registry' OR (d.defaclnamespace=0 AND owner.rolname='health_registry')) AND a.grantee=0`, nil},
		{"registry_unexpected_schema_grant", "acl", `SELECT count(*) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a LEFT JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname<>'health_registry' AND NOT ($1 AND g.rolname='health_user' AND a.privilege_type='USAGE')`, []any{allowLegacyBridge}},
		{"registry_unexpected_relation_grant", "acl", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a LEFT JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname<>'health_registry' AND NOT ($1 AND g.rolname='health_user' AND ((c.relkind='S' AND a.privilege_type IN ('USAGE','SELECT','UPDATE')) OR (c.relkind<>'S' AND a.privilege_type IN ('SELECT','INSERT','UPDATE','DELETE'))))`, []any{allowLegacyBridge}},
		{"registry_unexpected_routine_grant", "acl", `SELECT count(*) FROM pg_proc x JOIN pg_namespace n ON n.oid=x.pronamespace CROSS JOIN LATERAL aclexplode(coalesce(x.proacl,acldefault('f',x.proowner))) a LEFT JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname<>'health_registry'`, nil},
		{"registry_unexpected_type_grant", "acl", `SELECT count(*) FROM pg_type x JOIN pg_namespace n ON n.oid=x.typnamespace CROSS JOIN LATERAL aclexplode(coalesce(x.typacl,acldefault('T',x.typowner))) a LEFT JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND x.typelem=0 AND g.rolname<>'health_registry'`, nil},
		{"registry_unexpected_default_acl_grant", "acl", `SELECT count(*) FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a LEFT JOIN pg_roles owner ON owner.oid=d.defaclrole LEFT JOIN pg_roles g ON g.oid=a.grantee WHERE (n.nspname='health_registry' OR (d.defaclnamespace=0 AND owner.rolname='health_registry')) AND (owner.rolname<>'health_registry' OR g.rolname IS DISTINCT FROM 'health_registry')`, nil},
	}
	for _, c := range checks {
		var count int
		if err := p.QueryRow(ctx, c.sql, c.args...).Scan(&count); err != nil {
			return r, err
		}
		if count > 0 {
			r.Findings = append(r.Findings, FixedIdentityFinding{c.code, c.scope})
		}
	}
	return normalizeFixedIdentityResult(r), nil
}

func registryTenantCatalogFindings(ctx context.Context, p *pgxpool.Pool, schemas []string, tenantRoles map[string]bool) ([]FixedIdentityFinding, error) {
	seen := map[string]bool{}
	var out []FixedIdentityFinding
	add := func(code string) {
		if !seen[code] {
			seen[code] = true
			out = append(out, FixedIdentityFinding{code, "isolation"})
		}
	}
	for _, schema := range schemas {
		var allowed bool
		if err := p.QueryRow(ctx, `SELECT has_schema_privilege('health_registry',$1,'USAGE') OR has_schema_privilege('health_registry',$1,'CREATE')`, schema).Scan(&allowed); err != nil {
			return nil, err
		}
		if allowed {
			add("registry_tenant_schema_privilege")
		}
		checks := []struct{ code, sql string }{
			{"registry_tenant_relation_privilege", `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry')`},
			{"registry_tenant_routine_privilege", `SELECT EXISTS(SELECT 1 FROM pg_proc x JOIN pg_namespace n ON n.oid=x.pronamespace CROSS JOIN LATERAL aclexplode(x.proacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry')`},
			{"registry_tenant_type_privilege", `SELECT EXISTS(SELECT 1 FROM pg_type x JOIN pg_namespace n ON n.oid=x.typnamespace CROSS JOIN LATERAL aclexplode(x.typacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry')`},
			{"registry_tenant_default_acl_privilege", `SELECT EXISTS(SELECT 1 FROM pg_default_acl d JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry')`},
		}
		for _, check := range checks {
			var hit bool
			if err := p.QueryRow(ctx, check.sql, schema).Scan(&hit); err != nil {
				return nil, err
			}
			if hit {
				add(check.code)
			}
		}
	}
	for role := range tenantRoles {
		var hit bool
		if err := p.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM pg_default_acl d
				JOIN pg_roles owner ON owner.oid=d.defaclrole
				CROSS JOIN LATERAL aclexplode(d.defaclacl) a
				JOIN pg_roles grantee ON grantee.oid=a.grantee
				WHERE d.defaclnamespace=0 AND owner.rolname=$1
				  AND grantee.rolname='health_registry'
			)`, role).Scan(&hit); err != nil {
			return nil, err
		}
		if hit {
			add("registry_tenant_default_acl_privilege")
		}
	}
	return out, nil
}

func fixedDatabaseGrantFindings(ctx context.Context, p *pgxpool.Pool) ([]FixedIdentityFinding, error) {
	rows, err := p.Query(ctx, `SELECT g.rolname,a.privilege_type,a.is_grantable FROM pg_database d CROSS JOIN LATERAL aclexplode(coalesce(d.datacl,acldefault('d',d.datdba))) a JOIN pg_roles g ON g.oid=a.grantee WHERE d.datname=current_database() AND g.rolname IN ('health_admin','health_registry') ORDER BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]bool{}
	count := 0
	for rows.Next() {
		var role, priv string
		var grant bool
		if err = rows.Scan(&role, &priv, &grant); err != nil {
			return nil, err
		}
		got[role+":"+priv+":"+boolKey(grant)] = true
		count++
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	want := map[string]bool{"health_admin:CONNECT:false": true, "health_admin:CREATE:true": true, "health_registry:CONNECT:false": true}
	if count != len(want) {
		return []FixedIdentityFinding{{"fixed_database_grants_drift", "database"}}, nil
	}
	for k := range want {
		if !got[k] {
			return []FixedIdentityFinding{{"fixed_database_grants_drift", "database"}}, nil
		}
	}
	return nil, nil
}

func registryRequiredDefaultACLFindings(ctx context.Context, p *pgxpool.Pool) ([]FixedIdentityFinding, error) {
	rows, err := p.Query(ctx, `
		SELECT d.defaclobjtype::text, grantee.rolname, a.privilege_type, a.is_grantable
		FROM pg_default_acl d
		JOIN pg_roles owner ON owner.oid=d.defaclrole
		CROSS JOIN LATERAL aclexplode(d.defaclacl) a
		LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee
		WHERE d.defaclnamespace=0
		  AND owner.rolname='health_registry'
		  AND d.defaclobjtype IN ('f','T')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	exact := map[string]bool{"f": false, "T": false}
	rowCount := map[string]int{"f": 0, "T": 0}
	publicEffective := false
	for rows.Next() {
		var objectType string
		var grantee *string
		var privilege string
		var grantable bool
		if err = rows.Scan(&objectType, &grantee, &privilege, &grantable); err != nil {
			return nil, err
		}
		rowCount[objectType]++
		if grantee == nil {
			publicEffective = true
			continue
		}
		wantPrivilege := map[string]string{"f": "EXECUTE", "T": "USAGE"}[objectType]
		// ALTER DEFAULT PRIVILEGES stores the owner's replacement ACL without a
		// grant option; ownership itself supplies the implicit grant option.
		if *grantee == DatabaseRegistryRole && privilege == wantPrivilege && !grantable {
			exact[objectType] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	protectionMissing := !exact["f"] || !exact["T"] || rowCount["f"] != 1 || rowCount["T"] != 1
	if rowCount["f"] == 0 || rowCount["T"] == 0 {
		publicEffective = true // absent exception means PostgreSQL's PUBLIC default applies
	}
	var findings []FixedIdentityFinding
	if protectionMissing {
		findings = append(findings, FixedIdentityFinding{"registry_default_acl_protection_missing", "acl"})
	}
	if publicEffective {
		findings = append(findings, FixedIdentityFinding{"registry_public_default_acl_access", "acl"})
	}
	return findings, nil
}

func boolKey(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func fixedMembershipAllowed(granted, member string, admin, inherit, set, allowLegacy bool, allowedTenantRoles map[string]bool) bool {
	tenant := member == DatabaseAdminRole && allowedTenantRoles[granted] && admin && !inherit && !set
	bridge := allowLegacy && member == DatabaseAdminRole && granted == legacyDatabaseRole && !admin && !inherit && set
	return tenant || bridge
}

func fixedMembershipFindings(ctx context.Context, p *pgxpool.Pool, allowLegacy bool, allowedTenantRoles map[string]bool) ([]FixedIdentityFinding, error) {
	rows, err := p.Query(ctx, `SELECT granted.rolname,member.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE granted.rolname IN ('health_admin','health_registry') OR member.rolname IN ('health_admin','health_registry')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenantCounts := make(map[string]int, len(allowedTenantRoles))
	bridgeCount := 0
	for rows.Next() {
		var granted, member string
		var admin, inherit, set bool
		if err = rows.Scan(&granted, &member, &admin, &inherit, &set); err != nil {
			return nil, err
		}
		if !fixedMembershipAllowed(granted, member, admin, inherit, set, allowLegacy, allowedTenantRoles) {
			return []FixedIdentityFinding{{"fixed_role_membership_drift", "role"}}, nil
		}
		if granted == legacyDatabaseRole {
			bridgeCount++
		} else {
			tenantCounts[granted]++
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if bridgeCount > 1 {
		return []FixedIdentityFinding{{"fixed_role_membership_drift", "role"}}, nil
	}
	for role := range allowedTenantRoles {
		if tenantCounts[role] != 1 {
			return []FixedIdentityFinding{{"fixed_role_membership_drift", "role"}}, nil
		}
	}
	return nil, nil
}

func normalizeFixedIdentityResult(r FixedIdentityResult) FixedIdentityResult {
	if r.Findings == nil {
		r.Findings = []FixedIdentityFinding{}
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		if r.Findings[i].Code != r.Findings[j].Code {
			return r.Findings[i].Code < r.Findings[j].Code
		}
		return r.Findings[i].Scope < r.Findings[j].Scope
	})
	if len(r.Findings) == 0 {
		r.Status = AuditStatusPass
	} else {
		r.Status = AuditStatusFail
	}
	return r
}

func verifyRegistryPoolDeniedAll(ctx context.Context, pool *pgxpool.Pool, schemas []string) (IsolationProbeResult, error) {
	var result IsolationProbeResult
	var failures []error
	for _, schema := range schemas {
		for _, statement := range []string{"SELECT 1 FROM " + qualified(schema, "metric_points") + " LIMIT 0", "INSERT INTO " + qualified(schema, "settings") + "(key,value) VALUES ('__registry_identity_probe__','x')", "CREATE TABLE " + qualified(schema, "__registry_identity_probe__") + "(id integer)"} {
			result.Total++
			if err := expectPermissionDenied(ctx, pool, statement); err != nil {
				failures = append(failures, errors.New("registry identity denial probe failed (details redacted)"))
				var allowed *isolationAccessAllowedError
				if errors.As(err, &allowed) {
					result.CrossTenantFailures++
				} else {
					result.OperationalFailures++
				}
			} else {
				result.Denied++
			}
		}
	}
	return result, errors.Join(failures...)
}
