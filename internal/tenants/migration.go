package tenants

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

const RollbackManifestVersion = 1

type OwnedObject struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Identity        string   `json:"identity,omitempty"`
	Owner           string   `json:"owner"`
	RelKind         string   `json:"relkind,omitempty"`
	SecurityDefiner bool     `json:"security_definer,omitempty"`
	ProConfig       []string `json:"proconfig,omitempty"`
	RawACL          []string `json:"raw_acl,omitempty"`
	ACLIsNull       bool     `json:"acl_is_null"`
}
type ACLRecord struct {
	ObjectType string `json:"object_type"`
	ObjectName string `json:"object_name"`
	Grantor    string `json:"grantor"`
	Grantee    string `json:"grantee"`
	Privilege  string `json:"privilege"`
	Grantable  bool   `json:"grantable"`
}
type GrantRecord = ACLRecord
type DefaultPrivilege struct {
	Owner      string `json:"owner"`
	Schema     string `json:"schema"`
	ObjectType string `json:"object_type"`
	Grantor    string `json:"grantor"`
	Grantee    string `json:"grantee"`
	Privilege  string `json:"privilege"`
	Grantable  bool   `json:"grantable"`
}
type MembershipRecord struct {
	Role          string `json:"role"`
	Member        string `json:"member"`
	Grantor       string `json:"grantor"`
	AdminOption   bool   `json:"admin_option"`
	InheritOption *bool  `json:"inherit_option,omitempty"`
	SetOption     *bool  `json:"set_option,omitempty"`
}
type RoleMetadata struct {
	Name        string   `json:"name"`
	Exists      bool     `json:"exists"`
	Login       bool     `json:"login"`
	Superuser   bool     `json:"superuser"`
	CreateRole  bool     `json:"create_role"`
	CreateDB    bool     `json:"create_db"`
	Replication bool     `json:"replication"`
	BypassRLS   bool     `json:"bypass_rls"`
	Inherit     bool     `json:"inherit"`
	ConnLimit   int      `json:"connection_limit"`
	ValidUntil  *string  `json:"valid_until,omitempty"`
	Comment     *string  `json:"comment,omitempty"`
	Config      []string `json:"config,omitempty"`
}
type RegistryMetadata struct {
	Username          string    `json:"username"`
	TenantID          uuid.UUID `json:"tenant_id"`
	Schema            string    `json:"schema"`
	Role              string    `json:"role"`
	CredentialVersion int       `json:"credential_version"`
	IsolationReady    bool      `json:"isolation_ready"`
	State             string    `json:"state"`
	IsPrimary         bool      `json:"is_primary"`
}
type TenantInventory struct {
	Schema            string             `json:"schema"`
	TenantID          uuid.UUID          `json:"tenant_id"`
	Role              string             `json:"role"`
	CredentialVersion int                `json:"credential_version"`
	IsPrimary         bool               `json:"is_primary"`
	Registry          RegistryMetadata   `json:"registry"`
	SchemaOwner       string             `json:"schema_owner"`
	SchemaRawACL      []string           `json:"schema_raw_acl,omitempty"`
	SchemaACLIsNull   bool               `json:"schema_acl_is_null"`
	SchemaACL         []ACLRecord        `json:"schema_acl"`
	Objects           []OwnedObject      `json:"objects"`
	Grants            []GrantRecord      `json:"grants"`
	DefaultPrivileges []DefaultPrivilege `json:"default_privileges"`
	Memberships       []MembershipRecord `json:"memberships"`
	RoleCatalog       RoleMetadata       `json:"role_catalog"`
	Blockers          []string           `json:"blockers"`
}
type PlannedStatement struct {
	SQL             string                `json:"sql,omitempty"`
	Operation       string                `json:"operation,omitempty"`
	SecretParameter *TypedSecretParameter `json:"secret_parameter,omitempty"`
}
type TypedSecretParameter struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
}
type MigrationPlan struct {
	Schema     string             `json:"schema"`
	Role       string             `json:"role"`
	Blocked    bool               `json:"blocked"`
	Blockers   []string           `json:"blockers,omitempty"`
	Statements []PlannedStatement `json:"statements"`
	Actions    []string           `json:"actions"`
}
type RollbackPlan struct {
	Schema     string             `json:"schema"`
	Role       string             `json:"role"`
	Statements []PlannedStatement `json:"statements"`
	Actions    []string           `json:"actions"`
}
type RollbackManifest struct {
	Version        int             `json:"version"`
	ImageReference string          `json:"image_reference"`
	Inventory      TenantInventory `json:"inventory"`
	Checksum       string          `json:"checksum"`
}

func quoteIdent(s string) string           { return pgx.Identifier{s}.Sanitize() }
func qualified(schema, name string) string { return pgx.Identifier{schema, name}.Sanitize() }

func BuildMigrationPlan(i TenantInventory) (MigrationPlan, error) {
	if err := validateInventoryIdentity(i); err != nil {
		return MigrationPlan{}, err
	}
	if err := rejectSecretBearingInventory(i); err != nil {
		return MigrationPlan{}, err
	}
	p := MigrationPlan{Schema: i.Schema, Role: i.Role, Blocked: len(i.Blockers) > 0, Blockers: append([]string(nil), i.Blockers...)}
	sort.Strings(p.Blockers)
	if p.Blocked {
		return p, nil
	}
	p.Actions = []string{"validate canonical active registry metadata", "create or validate restricted tenant role", "set derived password via an out-of-band parameter (value never rendered)", "transfer schema and every inventoried object owner", "replace explicit and default ACLs from reviewed plan", "open restricted pool and prove own access plus cross-tenant/registry denial", "compare-and-set registry credential version/state only after proof"}
	if i.RoleCatalog.Exists {
		p.Actions = append(p.Actions, "validate existing role catalog attributes and comment against inventory")
	} else {
		p.Statements = append(p.Statements,
			PlannedStatement{SQL: "CREATE ROLE " + quoteIdent(i.Role) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"},
			PlannedStatement{SQL: "COMMENT ON ROLE " + quoteIdent(i.Role) + " IS 'health-tenant-v1:" + i.TenantID.String() + ":" + i.TenantID.String() + "'"},
		)
	}
	p.Statements = append(p.Statements,
		PlannedStatement{Operation: "ensure_exact_tenant_identity_marker"},
	)
	p.Statements = append(p.Statements, PlannedStatement{Operation: "set_role_password", SecretParameter: &TypedSecretParameter{Name: "derived_credential", Type: "postgres_role_password", Source: "credential_deriver"}})
	objects := append([]OwnedObject(nil), i.Objects...)
	sort.Slice(objects, func(a, b int) bool {
		if objects[a].Kind != objects[b].Kind {
			return objects[a].Kind < objects[b].Kind
		}
		if objects[a].Name != objects[b].Name {
			return objects[a].Name < objects[b].Name
		}
		return objects[a].Identity < objects[b].Identity
	})
	p.Statements = append(p.Statements, PlannedStatement{SQL: "REVOKE ALL ON SCHEMA " + quoteIdent(i.Schema) + " FROM PUBLIC"}, PlannedStatement{SQL: "ALTER SCHEMA " + quoteIdent(i.Schema) + " OWNER TO " + quoteIdent(i.Role)})
	for _, o := range objects {
		target := qualified(i.Schema, o.Name)
		if o.Kind == "FUNCTION" || o.Kind == "PROCEDURE" {
			target = quoteIdent(i.Schema) + "." + quoteIdent(o.Name) + "(" + o.Identity + ")"
		}
		p.Statements = append(p.Statements, PlannedStatement{SQL: "ALTER " + o.Kind + " " + target + " OWNER TO " + quoteIdent(i.Role)})
		if privilegeClass := privilegeObjectClass(o.Kind); privilegeClass != "" {
			p.Statements = append(p.Statements, PlannedStatement{SQL: "REVOKE ALL ON " + privilegeClass + " " + target + " FROM PUBLIC"})
		}
		if o.Kind == "FUNCTION" || o.Kind == "PROCEDURE" {
			p.Statements = append(p.Statements, PlannedStatement{SQL: "REVOKE ALL ON " + o.Kind + " " + target + " FROM PUBLIC"})
			p.Statements = append(p.Statements, PlannedStatement{SQL: "GRANT EXECUTE ON " + o.Kind + " " + target + " TO " + quoteIdent(i.Role)})
		}
		if o.Kind == "TYPE" || o.Kind == "DOMAIN" {
			p.Statements = append(p.Statements, PlannedStatement{SQL: "REVOKE ALL ON " + o.Kind + " " + target + " FROM PUBLIC"})
			p.Statements = append(p.Statements, PlannedStatement{SQL: "GRANT USAGE ON " + o.Kind + " " + target + " TO " + quoteIdent(i.Role)})
		}
	}
	grants := append([]GrantRecord(nil), i.SchemaACL...)
	grants = append(grants, i.Grants...)
	sort.Slice(grants, func(a, b int) bool {
		x, y := grants[a], grants[b]
		return x.ObjectType+x.ObjectName+x.Grantee+x.Privilege < y.ObjectType+y.ObjectName+y.Grantee+y.Privilege
	})
	for _, g := range grants {
		if g.Grantee == "PUBLIC" {
			continue
		}
		target, err := grantTarget(i.Schema, g)
		if err != nil {
			return MigrationPlan{}, err
		}
		sql := "GRANT " + g.Privilege + " ON " + g.ObjectType + " " + target + " TO " + quoteIdent(g.Grantee)
		if g.Grantable {
			sql += " WITH GRANT OPTION"
		}
		p.Statements = append(p.Statements, PlannedStatement{SQL: sql})
	}
	defaults := append([]DefaultPrivilege(nil), i.DefaultPrivileges...)
	sort.Slice(defaults, func(a, b int) bool {
		x, y := defaults[a], defaults[b]
		return x.Owner+x.Schema+x.ObjectType+x.Grantee+x.Privilege < y.Owner+y.Schema+y.ObjectType+y.Grantee+y.Privilege
	})
	for _, d := range defaults {
		kind, ok := map[string]string{"r": "TABLES", "S": "SEQUENCES", "f": "FUNCTIONS", "T": "TYPES", "n": "SCHEMAS"}[d.ObjectType]
		if !ok {
			return MigrationPlan{}, fmt.Errorf("unsupported default ACL object kind %q", d.ObjectType)
		}
		prefix := "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(d.Owner)
		if d.Schema != "" {
			prefix += " IN SCHEMA " + quoteIdent(d.Schema)
		}
		if d.Grantee == "PUBLIC" {
			p.Statements = append(p.Statements, PlannedStatement{SQL: prefix + " REVOKE " + d.Privilege + " ON " + kind + " FROM PUBLIC"})
			continue
		}
		sql := prefix + " GRANT " + d.Privilege + " ON " + kind + " TO " + quoteIdent(d.Grantee)
		if d.Grantable {
			sql += " WITH GRANT OPTION"
		}
		p.Statements = append(p.Statements, PlannedStatement{SQL: sql})
	}
	p.Statements = append(p.Statements,
		PlannedStatement{SQL: "GRANT USAGE, CREATE ON SCHEMA " + quoteIdent(i.Schema) + " TO " + quoteIdent(i.Role)},
		PlannedStatement{SQL: "GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA " + quoteIdent(i.Schema) + " TO " + quoteIdent(i.Role)}, PlannedStatement{SQL: "GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA " + quoteIdent(i.Schema) + " TO " + quoteIdent(i.Role)}, PlannedStatement{SQL: "GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA " + quoteIdent(i.Schema) + " TO " + quoteIdent(i.Role)},
		PlannedStatement{SQL: "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(i.Role) + " IN SCHEMA " + quoteIdent(i.Schema) + " GRANT ALL ON TABLES TO " + quoteIdent(i.Role)}, PlannedStatement{SQL: "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(i.Role) + " IN SCHEMA " + quoteIdent(i.Schema) + " GRANT ALL ON SEQUENCES TO " + quoteIdent(i.Role)}, PlannedStatement{SQL: "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(i.Role) + " IN SCHEMA " + quoteIdent(i.Schema) + " GRANT EXECUTE ON FUNCTIONS TO " + quoteIdent(i.Role)},
		PlannedStatement{Operation: "registry_compare_and_set_after_restricted_pool_proof"})
	return p, nil
}

// BuildRollbackPlan restores the pre-cutover ownership and effective ACLs
// captured in the immutable manifest. The restricted login is deliberately
// retained: deleting it would make rollback non-idempotent and could race
// still-open connections. Runtime access is disabled by the final registry
// compare-and-set operation.
func BuildRollbackPlan(i TenantInventory) (RollbackPlan, error) {
	if err := validateInventoryIdentity(i); err != nil {
		return RollbackPlan{}, err
	}
	if err := rejectSecretBearingInventory(i); err != nil {
		return RollbackPlan{}, err
	}
	p := RollbackPlan{Schema: i.Schema, Role: i.Role, Actions: []string{
		"restore every inventoried object owner", "restore schema owner",
		"remove cutover-only tenant grants and default privileges",
		"restore inventoried explicit and default privileges",
		"compare-and-set registry isolation readiness to false",
	}}
	objects := append([]OwnedObject(nil), i.Objects...)
	sort.Slice(objects, func(a, b int) bool {
		if objects[a].Kind != objects[b].Kind {
			return objects[a].Kind < objects[b].Kind
		}
		return objects[a].Name+objects[a].Identity < objects[b].Name+objects[b].Identity
	})
	for _, o := range objects {
		target := qualified(i.Schema, o.Name)
		if o.Kind == "FUNCTION" || o.Kind == "PROCEDURE" {
			target = quoteIdent(i.Schema) + "." + quoteIdent(o.Name) + "(" + o.Identity + ")"
		}
		p.Statements = append(p.Statements, PlannedStatement{SQL: "ALTER " + o.Kind + " " + target + " OWNER TO " + quoteIdent(o.Owner)})
		if class := privilegeObjectClass(o.Kind); class != "" {
			p.Statements = append(p.Statements, PlannedStatement{SQL: "REVOKE ALL ON " + class + " " + target + " FROM " + quoteIdent(i.Role)})
		}
	}
	p.Statements = append(p.Statements,
		PlannedStatement{SQL: "ALTER SCHEMA " + quoteIdent(i.Schema) + " OWNER TO " + quoteIdent(i.SchemaOwner)},
		PlannedStatement{SQL: "REVOKE ALL ON SCHEMA " + quoteIdent(i.Schema) + " FROM " + quoteIdent(i.Role)},
		PlannedStatement{SQL: "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(i.Role) + " IN SCHEMA " + quoteIdent(i.Schema) + " REVOKE ALL ON TABLES FROM " + quoteIdent(i.Role)},
		PlannedStatement{SQL: "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(i.Role) + " IN SCHEMA " + quoteIdent(i.Schema) + " REVOKE ALL ON SEQUENCES FROM " + quoteIdent(i.Role)},
		PlannedStatement{SQL: "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(i.Role) + " IN SCHEMA " + quoteIdent(i.Schema) + " REVOKE EXECUTE ON FUNCTIONS FROM " + quoteIdent(i.Role)},
	)
	if !inventoryHasObject(i, "TABLE", "__tenant_identity") {
		p.Statements = append(p.Statements, PlannedStatement{Operation: "drop_migration_tenant_identity_marker"})
	}
	grants := append([]GrantRecord(nil), i.SchemaACL...)
	grants = append(grants, i.Grants...)
	sort.Slice(grants, func(a, b int) bool {
		x, y := grants[a], grants[b]
		return x.ObjectType+x.ObjectName+x.Grantee+x.Privilege < y.ObjectType+y.ObjectName+y.Grantee+y.Privilege
	})
	for _, g := range grants {
		target, err := grantTarget(i.Schema, g)
		if err != nil {
			return RollbackPlan{}, err
		}
		sql := "GRANT " + g.Privilege + " ON " + g.ObjectType + " " + target + " TO " + grantPrincipal(g.Grantee)
		if g.Grantable {
			sql += " WITH GRANT OPTION"
		}
		p.appendRoleScoped(g.Grantor, sql)
	}
	for _, d := range i.DefaultPrivileges {
		kind, ok := map[string]string{"r": "TABLES", "S": "SEQUENCES", "f": "FUNCTIONS", "T": "TYPES", "n": "SCHEMAS"}[d.ObjectType]
		if !ok {
			return RollbackPlan{}, fmt.Errorf("unsupported default ACL object kind %q", d.ObjectType)
		}
		prefix := "ALTER DEFAULT PRIVILEGES FOR ROLE " + quoteIdent(d.Owner)
		if d.Schema != "" {
			prefix += " IN SCHEMA " + quoteIdent(d.Schema)
		}
		sql := prefix + " GRANT " + d.Privilege + " ON " + kind + " TO " + grantPrincipal(d.Grantee)
		if d.Grantable {
			sql += " WITH GRANT OPTION"
		}
		p.appendRoleScoped(d.Grantor, sql)
	}
	p.Statements = append(p.Statements, PlannedStatement{Operation: "registry_compare_and_set_isolation_ready_false"})
	return p, nil
}

func inventoryHasObject(i TenantInventory, kind, name string) bool {
	for _, o := range i.Objects {
		if o.Kind == kind && o.Name == name {
			return true
		}
	}
	return false
}

func grantPrincipal(name string) string {
	if name == "PUBLIC" {
		return "PUBLIC"
	}
	return quoteIdent(name)
}

func (p *RollbackPlan) appendRoleScoped(role, sql string) {
	_ = role // grantor is audit metadata; restoration preserves effective ACLs.
	p.Statements = append(p.Statements, PlannedStatement{SQL: sql})
}
func grantTarget(schema string, g GrantRecord) (string, error) {
	if g.ObjectType == "FUNCTION" || g.ObjectType == "PROCEDURE" {
		open := strings.IndexByte(g.ObjectName, '(')
		if open < 1 {
			return "", errors.New("routine grant lacks identity arguments")
		}
		return quoteIdent(schema) + "." + quoteIdent(g.ObjectName[:open]) + g.ObjectName[open:], nil
	}
	switch g.ObjectType {
	case "TABLE", "SEQUENCE", "TYPE", "DOMAIN":
		return qualified(schema, g.ObjectName), nil
	case "SCHEMA":
		return quoteIdent(g.ObjectName), nil
	default:
		return "", fmt.Errorf("unsupported grant object type %q", g.ObjectType)
	}
}
func privilegeObjectClass(kind string) string {
	switch kind {
	case "TABLE", "VIEW", "MATERIALIZED VIEW", "FOREIGN TABLE":
		return "TABLE"
	case "SEQUENCE", "FUNCTION", "PROCEDURE", "TYPE", "DOMAIN":
		return kind
	}
	return ""
}
func validateInventoryIdentity(i TenantInventory) error {
	if i.TenantID == uuid.Nil || i.Schema == "" || i.Role != TenantRoleName(i.TenantID) || i.CredentialVersion <= 0 || i.Registry.State != "active" || i.Registry.Schema != i.Schema || i.Registry.Role != i.Role || i.Registry.TenantID != i.TenantID {
		return errors.New("inventory does not contain canonical active tenant isolation metadata")
	}
	return nil
}

func NewRollbackManifest(i TenantInventory, image string) (RollbackManifest, error) {
	normalized, err := normalizeImageReference(image)
	if err != nil {
		return RollbackManifest{}, err
	}
	image = normalized
	if err := validateInventoryIdentity(i); err != nil {
		return RollbackManifest{}, err
	}
	if err := validatePersistedString("image_reference", image); err != nil {
		return RollbackManifest{}, err
	}
	if err := rejectSecretBearingInventory(i); err != nil {
		return RollbackManifest{}, err
	}
	m := RollbackManifest{Version: RollbackManifestVersion, ImageReference: image, Inventory: i}
	sum, err := manifestChecksum(m)
	if err != nil {
		return RollbackManifest{}, err
	}
	m.Checksum = sum
	return m, nil
}

var imageReferencePattern = regexp.MustCompile(`^(?:[A-Za-z0-9._/-]+@)?sha256:([A-Fa-f0-9]{64})$`)

func normalizeImageReference(s string) (string, error) {
	m := imageReferencePattern.FindStringSubmatch(s)
	if m == nil {
		return "", errors.New("immutable image reference must contain exactly 64 hexadecimal sha256 characters")
	}
	return strings.TrimSuffix(s, m[1]) + strings.ToLower(m[1]), nil
}

var secretWord = regexp.MustCompile(`(?i)(password|passfile|token|api[_-]?key|secret|authorization|bearer)`)
var dsnWord = regexp.MustCompile(`(?i)(^|\s)(user|password|passfile|host|dbname|sslkey)\s*=`)

func rejectSecretBearingInventory(i TenantInventory) error {
	return walkStrings(reflect.ValueOf(i), "inventory")
}
func walkStrings(v reflect.Value, path string) error {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return walkStrings(v.Elem(), path)
	}
	switch v.Kind() {
	case reflect.Struct:
		for n := 0; n < v.NumField(); n++ {
			name := v.Type().Field(n).Tag.Get("json")
			name = strings.Split(name, ",")[0]
			if name == "" {
				name = v.Type().Field(n).Name
			}
			if err := walkStrings(v.Field(n), path+"."+name); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for n := 0; n < v.Len(); n++ {
			if err := walkStrings(v.Index(n), fmt.Sprintf("%s[%d]", path, n)); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := walkStrings(iter.Value(), path+"[map]"); err != nil {
				return err
			}
		}
	case reflect.String:
		return validatePersistedString(path, v.String())
	}
	return nil
}
func validatePersistedString(path, s string) error {
	if secretWord.MatchString(s) || dsnWord.MatchString(s) {
		return fmt.Errorf("secret-like persisted string at %s", path)
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		if u.User != nil {
			return fmt.Errorf("URI userinfo at %s", path)
		}
		for k := range u.Query() {
			if secretWord.MatchString(k) {
				return fmt.Errorf("credential query parameter at %s", path)
			}
		}
	}
	return nil
}
func manifestChecksum(m RollbackManifest) (string, error) {
	m.Checksum = ""
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}
func WriteRollbackManifest(path string, m RollbackManifest) error {
	if _, err := os.Stat(path); err == nil {
		return errors.New("refusing to overwrite rollback manifest")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func ReadRollbackManifest(path string) (RollbackManifest, error) {
	var m RollbackManifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Version != RollbackManifestVersion {
		return m, fmt.Errorf("unsupported rollback manifest version %d", m.Version)
	}
	want, err := manifestChecksum(m)
	if err != nil {
		return m, err
	}
	if m.Checksum == "" || m.Checksum != want {
		return m, errors.New("rollback manifest checksum mismatch")
	}
	if err = validateInventoryIdentity(m.Inventory); err != nil {
		return m, err
	}
	if err = validatePersistedString("image_reference", m.ImageReference); err != nil {
		return m, err
	}
	if err = rejectSecretBearingInventory(m.Inventory); err != nil {
		return m, err
	}
	return m, nil
}
func ManifestPath(base, schema string, all bool) string {
	if !all {
		return base
	}
	ext := filepath.Ext(base)
	prefix := base
	if strings.EqualFold(ext, ".json") {
		prefix = strings.TrimSuffix(base, ext)
	}
	return prefix + "-" + schema + ".json"
}

var manifestSchemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func WriteRollbackManifestSet(items map[string]RollbackManifest) error {
	paths := make([]string, 0, len(items))
	seen := map[string]bool{}
	for path, m := range items {
		if !manifestSchemaPattern.MatchString(m.Inventory.Schema) {
			return fmt.Errorf("unsafe manifest schema %q", m.Inventory.Schema)
		}
		key := strings.ToLower(filepath.Clean(path))
		if seen[key] {
			return errors.New("duplicate manifest target")
		}
		seen[key] = true
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("manifest target already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var written []string
	for _, path := range paths {
		b, err := json.MarshalIndent(items[path], "", "  ")
		if err == nil {
			var f *os.File
			f, err = manifestOpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_, err = f.Write(append(b, '\n'))
				closeErr := f.Close()
				if err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			failure := err
			var partial []string
			for _, p := range written {
				if removeErr := manifestRemove(p); removeErr != nil {
					partial = append(partial, p)
					failure = errors.Join(failure, fmt.Errorf("remove partial manifest %s: %w", p, removeErr))
				}
			}
			if len(partial) > 0 {
				failure = errors.Join(failure, fmt.Errorf("partial rollback manifest set remains at %v", partial))
			}
			return failure
		}
		written = append(written, path)
	}
	return nil
}

var (
	manifestOpenFile = os.OpenFile
	manifestRemove   = os.Remove
)

type Migrator struct {
	admin      *pgxpool.Pool
	tenantBase string
	deriver    CredentialDeriver
}

type MigrationConnectionError struct {
	stage string
	cause error
}

func (e *MigrationConnectionError) Error() string {
	return e.stage + " administrator connection failed (details redacted)"
}
func (e *MigrationConnectionError) Unwrap() error { return e.cause }

func NewMigrator(ctx context.Context, adminDSN, tenantBase string, deriver CredentialDeriver) (*Migrator, error) {
	if err := deriverValidateForReadOnly(deriver); err != nil {
		return nil, err
	}
	c, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		return nil, &MigrationConnectionError{stage: "parse", cause: err}
	}
	c.MaxConns = 1
	c.MinConns = 0
	p, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, &MigrationConnectionError{stage: "open", cause: err}
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, &MigrationConnectionError{stage: "ping", cause: err}
	}
	if err := validateTenantDSNBase(tenantBase); err != nil {
		p.Close()
		return nil, fmt.Errorf("tenant database base: %w", err)
	}
	return &Migrator{admin: p, tenantBase: tenantBase, deriver: deriver}, nil
}
func deriverValidateForReadOnly(d CredentialDeriver) error { return d.validate() }
func (m *Migrator) Close()                                 { m.admin.Close() }
func (m *Migrator) CanonicalSchemas(ctx context.Context) ([]string, error) {
	rows, err := m.admin.Query(ctx, `SELECT schema_name FROM health_registry.users WHERE provisioning_state='active' ORDER BY created_at ASC,schema_name ASC`)
	if err != nil {
		return nil, err
	}
	return scanRows[string](rows, func(r rowsLike, s *string) error { return r.Scan(s) })
}

func (m *Migrator) VerifyRestrictedTenant(ctx context.Context, i TenantInventory, otherSchema string) error {
	if err := validateInventoryIdentity(i); err != nil {
		return err
	}
	password, err := m.deriver.Derive(i.TenantID, i.Role, i.CredentialVersion)
	if err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(m.tenantBase)
	if err != nil {
		return &MigrationConnectionError{stage: "parse tenant", cause: err}
	}
	cfg.ConnConfig.User = i.Role
	cfg.ConnConfig.Password = password
	cfg.ConnConfig.RuntimeParams["search_path"] = i.Schema
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return &MigrationConnectionError{stage: "open tenant", cause: err}
	}
	tenantDB := storage.NewFromPool(pool)
	defer tenantDB.Close()
	if err = pool.Ping(ctx); err != nil {
		return &MigrationConnectionError{stage: "ping tenant", cause: err}
	}
	var currentUser, currentSchema string
	if err = pool.QueryRow(ctx, `SELECT current_user,current_schema()`).Scan(&currentUser, &currentSchema); err != nil {
		return err
	}
	if currentUser != i.Role || currentSchema != i.Schema {
		return fmt.Errorf("restricted identity mismatch: user=%q schema=%q", currentUser, currentSchema)
	}
	var ownTable *string
	if err = pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, i.Schema+".metric_points").Scan(&ownTable); err != nil {
		return err
	}
	if ownTable == nil {
		return errors.New("restricted tenant cannot resolve its own metric_points table")
	}
	if _, err = pool.Exec(ctx, "SELECT 1 FROM "+qualified(i.Schema, "metric_points")+" LIMIT 0"); err != nil {
		return fmt.Errorf("restricted own-schema read: %w", err)
	}
	var markerTenant uuid.UUID
	if err = pool.QueryRow(ctx, "SELECT tenant_id FROM "+qualified(i.Schema, "__tenant_identity")+" WHERE singleton=true").Scan(&markerTenant); err != nil {
		return fmt.Errorf("restricted tenant identity marker: %w", err)
	}
	if markerTenant != i.TenantID {
		return errors.New("restricted tenant identity marker mismatch")
	}
	if err = tenantDB.VerifyProvisionedSchema(); err != nil {
		return fmt.Errorf("restricted tenant schema contract: %w", err)
	}
	probes := []string{
		`SELECT 1 FROM health_registry.users LIMIT 0`,
		`INSERT INTO health_registry.global_settings(key,value) VALUES ('__isolation_probe__','x')`,
		`CREATE TABLE health_registry.__isolation_probe__(id integer)`,
	}
	if otherSchema != "" {
		if err := registry.ValidateSchemaName(otherSchema); err != nil {
			return err
		}
		probes = append(probes,
			"SELECT 1 FROM "+qualified(otherSchema, "metric_points")+" LIMIT 0",
			"INSERT INTO "+qualified(otherSchema, "settings")+"(key,value) VALUES ('__isolation_probe__','x')",
			"CREATE TABLE "+qualified(otherSchema, "__isolation_probe__")+"(id integer)",
		)
	}
	for _, probe := range probes {
		if err := expectPermissionDenied(ctx, pool, probe); err != nil {
			return err
		}
	}
	return nil
}

// ApplyRestrictedTenant performs the catalog cutover, proves the restricted
// login, and only then exposes the tenant to isolated runtime routing. A failed
// proof is compensated from the supplied pre-change inventory.
func (m *Migrator) ApplyRestrictedTenant(ctx context.Context, i TenantInventory, otherSchema string) error {
	plan, err := BuildMigrationPlan(i)
	if err != nil {
		return err
	}
	if plan.Blocked {
		return fmt.Errorf("tenant migration is blocked: %s", strings.Join(plan.Blockers, "; "))
	}
	if i.Registry.IsolationReady {
		return m.VerifyRestrictedTenant(ctx, i, otherSchema)
	}
	if err := m.executeMigrationPlan(ctx, i, plan); err != nil {
		return fmt.Errorf("apply tenant catalog cutover: %w", err)
	}
	if err := m.VerifyRestrictedTenant(ctx, i, otherSchema); err != nil {
		restoreErr := m.RestoreTenant(ctx, i)
		return errors.Join(fmt.Errorf("restricted tenant proof failed: %w", err), restoreErr)
	}
	tag, err := m.admin.Exec(ctx, `UPDATE health_registry.users SET db_isolation_ready=true WHERE tenant_id=$1 AND schema_name=$2 AND db_role=$3 AND db_credential_version=$4 AND provisioning_state='active' AND db_isolation_ready=false`, i.TenantID, i.Schema, i.Role, i.CredentialVersion)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = registry.ErrProvisioningStateConflict
		}
		restoreErr := m.RestoreTenant(ctx, i)
		return errors.Join(fmt.Errorf("activate isolated registry routing: %w", err), restoreErr)
	}
	return nil
}

func (m *Migrator) executeMigrationPlan(ctx context.Context, i TenantInventory, plan MigrationPlan) error {
	tx, err := m.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	password, err := m.deriver.Derive(i.TenantID, i.Role, i.CredentialVersion)
	if err != nil {
		return err
	}
	for _, statement := range plan.Statements {
		switch statement.Operation {
		case "":
			if _, err = tx.Exec(ctx, statement.SQL); err != nil {
				return err
			}
		case "set_role_password":
			var sql string
			if err = tx.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L',$1::text,$2::text)`, i.Role, password).Scan(&sql); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, sql); err != nil {
				return err
			}
		case "ensure_exact_tenant_identity_marker":
			if err = ensureMigrationTenantMarker(ctx, tx, i); err != nil {
				return err
			}
		case "registry_compare_and_set_after_restricted_pool_proof":
			// Deliberately executed only after this transaction commits and the
			// restricted connection proves its denial boundaries.
		default:
			return fmt.Errorf("unsupported migration operation %q", statement.Operation)
		}
	}
	return tx.Commit(ctx)
}

func ensureMigrationTenantMarker(ctx context.Context, tx pgx.Tx, i TenantInventory) error {
	table := qualified(i.Schema, "__tenant_identity")
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+` (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), tenant_id uuid NOT NULL, operation_id uuid NOT NULL)`); err != nil {
		return err
	}
	var tenantID uuid.UUID
	err := tx.QueryRow(ctx, "SELECT tenant_id FROM "+table+" WHERE singleton=true").Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, "INSERT INTO "+table+" (tenant_id,operation_id) VALUES ($1,$2)", i.TenantID, i.TenantID)
	} else if err == nil && tenantID != i.TenantID {
		return errors.New("tenant identity marker belongs to another tenant")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "ALTER TABLE "+table+" OWNER TO "+quoteIdent(i.Role))
	return err
}

// RestoreTenant applies the manifest inventory transactionally and disables
// isolated routing in the same commit. It is safe to retry; the restricted
// role is retained but loses ownership and explicit runtime grants.
func (m *Migrator) RestoreTenant(ctx context.Context, i TenantInventory) error {
	plan, err := BuildRollbackPlan(i)
	if err != nil {
		return err
	}
	tx, err := m.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, statement := range plan.Statements {
		switch statement.Operation {
		case "":
			if _, err = tx.Exec(ctx, statement.SQL); err != nil {
				return err
			}
		case "drop_migration_tenant_identity_marker":
			if _, err = tx.Exec(ctx, "DROP TABLE IF EXISTS "+qualified(i.Schema, "__tenant_identity")); err != nil {
				return err
			}
		case "registry_compare_and_set_isolation_ready_false":
			tag, execErr := tx.Exec(ctx, `UPDATE health_registry.users SET db_isolation_ready=false WHERE tenant_id=$1 AND schema_name=$2 AND db_role=$3 AND db_credential_version=$4 AND provisioning_state='active'`, i.TenantID, i.Schema, i.Role, i.CredentialVersion)
			if execErr != nil {
				return execErr
			}
			if tag.RowsAffected() != 1 {
				return registry.ErrProvisioningStateConflict
			}
		default:
			return fmt.Errorf("unsupported rollback operation %q", statement.Operation)
		}
	}
	return tx.Commit(ctx)
}

// RotateTenantCredential changes the PostgreSQL login first, proves the new
// credential through an ephemeral verification pool, and then advances
// registry metadata with an exact CAS. Runtime pools are not hot-replaced:
// Manager fails closed on credential-version drift until a successful service
// restart opens new-version pools. Keep the previous secret through restart.
func (m *Migrator) RotateTenantCredential(ctx context.Context, i TenantInventory, expectedOldVersion, targetVersion int, otherSchema string) error {
	if err := validateInventoryIdentity(i); err != nil {
		return err
	}
	if !i.Registry.IsolationReady {
		return errors.New("credential rotation requires an isolation-ready tenant")
	}
	if i.CredentialVersion != expectedOldVersion || targetVersion == expectedOldVersion {
		return registry.ErrProvisioningStateConflict
	}
	oldPassword, err := m.deriver.Derive(i.TenantID, i.Role, expectedOldVersion)
	if err != nil {
		return fmt.Errorf("derive previous credential: %w", err)
	}
	newPassword, err := m.deriver.Derive(i.TenantID, i.Role, targetVersion)
	if err != nil {
		return fmt.Errorf("derive target credential: %w", err)
	}
	if err = m.setRolePassword(ctx, i.Role, newPassword); err != nil {
		probe := i
		probe.CredentialVersion = targetVersion
		probe.Registry.CredentialVersion = targetVersion
		if verifyErr := m.VerifyRestrictedTenant(ctx, probe, otherSchema); verifyErr != nil {
			return errors.Join(fmt.Errorf("set target credential: %w", err), verifyErr)
		}
	}
	probe := i
	probe.CredentialVersion = targetVersion
	probe.Registry.CredentialVersion = targetVersion
	if err = m.VerifyRestrictedTenant(ctx, probe, otherSchema); err != nil {
		restoreErr := m.setRolePassword(ctx, i.Role, oldPassword)
		return errors.Join(fmt.Errorf("verify target credential: %w", err), restoreErr)
	}
	tag, err := m.admin.Exec(ctx, `UPDATE health_registry.users SET db_credential_version=$5 WHERE tenant_id=$1 AND schema_name=$2 AND db_role=$3 AND db_credential_version=$4 AND provisioning_state='active' AND db_isolation_ready=true`, i.TenantID, i.Schema, i.Role, expectedOldVersion, targetVersion)
	if err != nil {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		var landedVersion int
		readErr := m.admin.QueryRow(readCtx, `SELECT db_credential_version FROM health_registry.users WHERE tenant_id=$1 AND schema_name=$2 AND db_role=$3 AND provisioning_state='active' AND db_isolation_ready=true`, i.TenantID, i.Schema, i.Role).Scan(&landedVersion)
		if readErr == nil && landedVersion == targetVersion {
			return nil
		}
		restoreErr := m.setRolePassword(readCtx, i.Role, oldPassword)
		return errors.Join(fmt.Errorf("advance credential version: %w", err), readErr, restoreErr)
	}
	if tag.RowsAffected() != 1 {
		if err == nil {
			err = registry.ErrProvisioningStateConflict
		}
		restoreErr := m.setRolePassword(ctx, i.Role, oldPassword)
		return errors.Join(fmt.Errorf("advance credential version: %w", err), restoreErr)
	}
	return nil
}

func (m *Migrator) setRolePassword(ctx context.Context, role, password string) error {
	tx, err := m.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var statement string
	if err = tx.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L', $1::text, $2::text)`, role, password).Scan(&statement); err != nil {
		return fmt.Errorf("format role password statement: %w", err)
	}
	if _, err = tx.Exec(ctx, statement); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func expectPermissionDenied(ctx context.Context, pool *pgxpool.Pool, statement string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	_, execErr := tx.Exec(ctx, statement)
	if execErr == nil {
		return fmt.Errorf("isolation probe unexpectedly succeeded: %s", statement)
	}
	var pgErr *pgconn.PgError
	if !errors.As(execErr, &pgErr) || pgErr.Code != "42501" {
		return fmt.Errorf("isolation probe returned SQLSTATE other than 42501: %w", execErr)
	}
	return nil
}

type rowsLike interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

func scanRows[T any](rows rowsLike, scan func(rowsLike, *T) error) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Migrator) Inventory(ctx context.Context, schema string) (TenantInventory, error) {
	var i TenantInventory
	i.Schema = schema
	var state string
	err := m.admin.QueryRow(ctx, `SELECT username,tenant_id,schema_name,db_role,db_credential_version,db_isolation_ready,provisioning_state,is_admin FROM health_registry.users WHERE schema_name=$1 AND provisioning_state='active'`, schema).Scan(&i.Registry.Username, &i.TenantID, &i.Registry.Schema, &i.Role, &i.CredentialVersion, &i.Registry.IsolationReady, &state, &i.IsPrimary)
	if err != nil {
		return i, fmt.Errorf("schema is not a canonical active registry tenant: %w", err)
	}
	i.Registry = RegistryMetadata{Username: i.Registry.Username, TenantID: i.TenantID, Schema: i.Schema, Role: i.Role, CredentialVersion: i.CredentialVersion, IsolationReady: i.Registry.IsolationReady, State: state, IsPrimary: i.IsPrimary}
	if err = validateInventoryIdentity(i); err != nil {
		return i, err
	}
	var schemaOID uint32
	if err = m.admin.QueryRow(ctx, `SELECT n.oid,r.rolname,coalesce(n.nspacl,'{}'::aclitem[]),n.nspacl IS NULL FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname=$1`, schema).Scan(&schemaOID, &i.SchemaOwner, &i.SchemaRawACL, &i.SchemaACLIsNull); err != nil {
		return i, err
	}
	if i.SchemaOwner == "" {
		i.Blockers = append(i.Blockers, "schema has unknown owner")
	}
	if i.SchemaACL, err = m.acls(ctx, `SELECT 'SCHEMA',$1::text,grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),x.privilege_type,x.is_grantable FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) x JOIN pg_roles grantor ON grantor.oid=x.grantor LEFT JOIN pg_roles grantee ON grantee.oid=x.grantee WHERE n.oid=$2 ORDER BY 3,4,5`, schema, schemaOID); err != nil {
		return i, err
	}
	rows, err := m.admin.Query(ctx, `SELECT c.relkind::text,c.relname,r.rolname,coalesce(c.relacl,'{}'::aclitem[]),c.relacl IS NULL FROM pg_class c JOIN pg_roles r ON r.oid=c.relowner WHERE c.relnamespace=$1 AND NOT (c.relkind='S' AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.refclassid='pg_class'::regclass AND d.deptype IN ('a','i'))) ORDER BY c.relkind,c.relname`, schemaOID)
	if err != nil {
		return i, err
	}
	for rows.Next() {
		var rk, n, o string
		var raw []string
		var isNull bool
		if err = rows.Scan(&rk, &n, &o, &raw, &isNull); err != nil {
			rows.Close()
			return i, err
		}
		kind, ok := map[string]string{"r": "TABLE", "p": "TABLE", "v": "VIEW", "m": "MATERIALIZED VIEW", "S": "SEQUENCE", "f": "FOREIGN TABLE"}[rk]
		if !ok {
			if rk == "i" || rk == "I" || rk == "c" {
				continue
			}
			i.Blockers = append(i.Blockers, "unknown schema relkind "+rk+" for "+n)
			continue
		}
		if o == "" {
			i.Blockers = append(i.Blockers, "unknown owner for "+n)
		}
		i.Objects = append(i.Objects, OwnedObject{Kind: kind, Name: n, Owner: o, RelKind: rk, RawACL: raw, ACLIsNull: isNull})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return i, err
	}
	rows.Close()
	frows, err := m.admin.Query(ctx, `SELECT p.prokind::text,p.proname,pg_get_function_identity_arguments(p.oid),r.rolname,p.prosecdef,coalesce(p.proconfig,'{}'::text[]),coalesce(p.proacl,'{}'::aclitem[]),p.proacl IS NULL FROM pg_proc p JOIN pg_roles r ON r.oid=p.proowner WHERE p.pronamespace=$1 ORDER BY p.prokind,p.proname,3`, schemaOID)
	if err != nil {
		return i, err
	}
	for frows.Next() {
		var k, n, args, o string
		var sec bool
		var cfg, raw []string
		var isNull bool
		if err = frows.Scan(&k, &n, &args, &o, &sec, &cfg, &raw, &isNull); err != nil {
			frows.Close()
			return i, err
		}
		kind, ok := map[string]string{"f": "FUNCTION", "p": "PROCEDURE", "a": "FUNCTION", "w": "FUNCTION"}[k]
		if !ok {
			i.Blockers = append(i.Blockers, "unknown routine kind "+k+" for "+n)
			continue
		}
		obj := OwnedObject{Kind: kind, Name: n, Identity: args, Owner: o, SecurityDefiner: sec, ProConfig: cfg, RawACL: raw, ACLIsNull: isNull}
		if o == "" {
			i.Blockers = append(i.Blockers, "unknown owner for routine "+n)
		}
		i.Objects = append(i.Objects, obj)
		if sec {
			i.Blockers = append(i.Blockers, "SECURITY DEFINER routine requires reviewed allowlist: "+schema+"."+n+"("+args+")")
		}
	}
	if err = frows.Err(); err != nil {
		frows.Close()
		return i, err
	}
	frows.Close()
	trows, err := m.admin.Query(ctx, `SELECT CASE WHEN t.typtype='d' THEN 'DOMAIN' ELSE 'TYPE' END,t.typname,r.rolname,coalesce(t.typacl,'{}'::aclitem[]),t.typacl IS NULL FROM pg_type t JOIN pg_roles r ON r.oid=t.typowner WHERE t.typnamespace=$1 AND t.typtype IN ('b','c','d','e','r','m') AND t.typcategory<>'A' AND t.typelem=0 AND NOT EXISTS(SELECT 1 FROM pg_class c WHERE c.reltype=t.oid) AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype IN ('i','a')) ORDER BY 1,2`, schemaOID)
	if err != nil {
		return i, err
	}
	for trows.Next() {
		var k, n, o string
		var raw []string
		var isNull bool
		if err = trows.Scan(&k, &n, &o, &raw, &isNull); err != nil {
			trows.Close()
			return i, err
		}
		i.Objects = append(i.Objects, OwnedObject{Kind: k, Name: n, Owner: o, RawACL: raw, ACLIsNull: isNull})
		if o == "" {
			i.Blockers = append(i.Blockers, "unknown owner for type "+n)
		}
	}
	if err = trows.Err(); err != nil {
		trows.Close()
		return i, err
	}
	trows.Close()
	unsupported, err := m.admin.Query(ctx, `SELECT kind,name,owner FROM (SELECT 'COLLATION' kind,c.collname name,r.rolname owner FROM pg_collation c JOIN pg_roles r ON r.oid=c.collowner WHERE c.collnamespace=$1 UNION ALL SELECT 'CONVERSION',c.conname,r.rolname FROM pg_conversion c JOIN pg_roles r ON r.oid=c.conowner WHERE c.connamespace=$1 UNION ALL SELECT 'OPERATOR',o.oprname,r.rolname FROM pg_operator o JOIN pg_roles r ON r.oid=o.oprowner WHERE o.oprnamespace=$1 UNION ALL SELECT 'OPERATOR CLASS',o.opcname,r.rolname FROM pg_opclass o JOIN pg_roles r ON r.oid=o.opcowner WHERE o.opcnamespace=$1 UNION ALL SELECT 'OPERATOR FAMILY',o.opfname,r.rolname FROM pg_opfamily o JOIN pg_roles r ON r.oid=o.opfowner WHERE o.opfnamespace=$1 UNION ALL SELECT 'TEXT SEARCH CONFIGURATION',c.cfgname,r.rolname FROM pg_ts_config c JOIN pg_roles r ON r.oid=c.cfgowner WHERE c.cfgnamespace=$1 UNION ALL SELECT 'TEXT SEARCH DICTIONARY',d.dictname,r.rolname FROM pg_ts_dict d JOIN pg_roles r ON r.oid=d.dictowner WHERE d.dictnamespace=$1 UNION ALL SELECT 'TEXT SEARCH PARSER',p.prsname,'' FROM pg_ts_parser p WHERE p.prsnamespace=$1 UNION ALL SELECT 'TEXT SEARCH TEMPLATE',t.tmplname,'' FROM pg_ts_template t WHERE t.tmplnamespace=$1) q ORDER BY kind,name`, schemaOID)
	if err != nil {
		return i, err
	}
	for unsupported.Next() {
		var k, n, o string
		if err = unsupported.Scan(&k, &n, &o); err != nil {
			unsupported.Close()
			return i, err
		}
		i.Blockers = append(i.Blockers, "unsupported schema-owned "+k+" "+n+" owner="+o)
	}
	if err = unsupported.Err(); err != nil {
		unsupported.Close()
		return i, err
	}
	unsupported.Close()
	deps, err := m.admin.Query(ctx, `SELECT e.extname,pg_describe_object(d.classid,d.objid,d.objsubid) FROM pg_depend d JOIN pg_extension e ON e.oid=d.refobjid WHERE d.deptype='e' AND ((d.classid='pg_class'::regclass AND EXISTS(SELECT 1 FROM pg_class c WHERE c.oid=d.objid AND c.relnamespace=$1)) OR (d.classid='pg_proc'::regclass AND EXISTS(SELECT 1 FROM pg_proc p WHERE p.oid=d.objid AND p.pronamespace=$1)) OR (d.classid='pg_type'::regclass AND EXISTS(SELECT 1 FROM pg_type t WHERE t.oid=d.objid AND t.typnamespace=$1)) OR (d.classid='pg_namespace'::regclass AND d.objid=$1)) ORDER BY 1,2`, schemaOID)
	if err != nil {
		return i, err
	}
	for deps.Next() {
		var e, n string
		if err = deps.Scan(&e, &n); err != nil {
			deps.Close()
			return i, err
		}
		i.Blockers = append(i.Blockers, "extension-owned object "+n+" from "+e)
	}
	if err = deps.Err(); err != nil {
		deps.Close()
		return i, err
	}
	deps.Close()
	if i.Grants, err = m.acls(ctx, `SELECT CASE c.relkind WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,c.relname,grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),x.privilege_type,x.is_grantable FROM pg_class c CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) x JOIN pg_roles grantor ON grantor.oid=x.grantor LEFT JOIN pg_roles grantee ON grantee.oid=x.grantee WHERE c.relnamespace=$1 AND c.relkind IN ('r','p','v','m','S','f') ORDER BY 1,2,3,4,5`, schemaOID); err != nil {
		return i, err
	}
	fg, err := m.acls(ctx, `SELECT CASE WHEN p.prokind='p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,p.proname||'('||pg_get_function_identity_arguments(p.oid)||')',grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),x.privilege_type,x.is_grantable FROM pg_proc p CROSS JOIN LATERAL aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) x JOIN pg_roles grantor ON grantor.oid=x.grantor LEFT JOIN pg_roles grantee ON grantee.oid=x.grantee WHERE p.pronamespace=$1 ORDER BY 1,2,3,4,5`, schemaOID)
	if err != nil {
		return i, err
	}
	i.Grants = append(i.Grants, fg...)
	tg, err := m.acls(ctx, `SELECT CASE WHEN t.typtype='d' THEN 'DOMAIN' ELSE 'TYPE' END,t.typname,grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),x.privilege_type,x.is_grantable FROM pg_type t CROSS JOIN LATERAL aclexplode(coalesce(t.typacl,acldefault('T',t.typowner))) x JOIN pg_roles grantor ON grantor.oid=x.grantor LEFT JOIN pg_roles grantee ON grantee.oid=x.grantee WHERE t.typnamespace=$1 AND t.typcategory<>'A' AND t.typelem=0 AND NOT EXISTS(SELECT 1 FROM pg_class c WHERE c.reltype=t.oid) AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype IN ('i','a')) ORDER BY 1,2,3,4,5`, schemaOID)
	if err != nil {
		return i, err
	}
	i.Grants = append(i.Grants, tg...)
	drows, err := m.admin.Query(ctx, `WITH relevant_owners AS (SELECT nspowner oid FROM pg_namespace WHERE oid=$1 UNION SELECT relowner FROM pg_class WHERE relnamespace=$1 UNION SELECT proowner FROM pg_proc WHERE pronamespace=$1 UNION SELECT typowner FROM pg_type WHERE typnamespace=$1) SELECT owner.rolname,coalesce(n.nspname,''),d.defaclobjtype::text,grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),x.privilege_type,x.is_grantable FROM pg_default_acl d JOIN pg_roles owner ON owner.oid=d.defaclrole LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) x JOIN pg_roles grantor ON grantor.oid=x.grantor LEFT JOIN pg_roles grantee ON grantee.oid=x.grantee WHERE d.defaclrole IN (SELECT oid FROM relevant_owners) AND d.defaclnamespace IN (0,$1) ORDER BY 1,2,3,4,5,6`, schemaOID)
	if err != nil {
		return i, err
	}
	for drows.Next() {
		var d DefaultPrivilege
		if err = drows.Scan(&d.Owner, &d.Schema, &d.ObjectType, &d.Grantor, &d.Grantee, &d.Privilege, &d.Grantable); err != nil {
			drows.Close()
			return i, err
		}
		i.DefaultPrivileges = append(i.DefaultPrivileges, d)
	}
	if err = drows.Err(); err != nil {
		drows.Close()
		return i, err
	}
	drows.Close()
	if err = m.loadRole(ctx, &i); err != nil {
		return i, err
	}
	i.applySafetyPolicy()
	sort.Strings(i.Blockers)
	return i, nil
}
func (m *Migrator) acls(ctx context.Context, q string, args ...any) ([]ACLRecord, error) {
	rows, err := m.admin.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanRows[ACLRecord](rows, func(r rowsLike, a *ACLRecord) error {
		return r.Scan(&a.ObjectType, &a.ObjectName, &a.Grantor, &a.Grantee, &a.Privilege, &a.Grantable)
	})
}
func (m *Migrator) loadRole(ctx context.Context, i *TenantInventory) error {
	r := &i.RoleCatalog
	r.Name = i.Role
	var vu *string
	err := m.admin.QueryRow(ctx, `SELECT true,rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls,rolinherit,rolconnlimit,rolvaliduntil::text,shobj_description(oid,'pg_authid'),coalesce(rolconfig,'{}'::text[]) FROM pg_roles WHERE rolname=$1`, i.Role).Scan(&r.Exists, &r.Login, &r.Superuser, &r.CreateRole, &r.CreateDB, &r.Replication, &r.BypassRLS, &r.Inherit, &r.ConnLimit, &vu, &r.Comment, &r.Config)
	if errors.Is(err, pgx.ErrNoRows) {
		r.Exists = false
		return nil
	}
	if err != nil {
		return err
	}
	r.ValidUntil = vu
	var hasOptions bool
	if err = m.admin.QueryRow(ctx, `SELECT count(*)=2 FROM pg_attribute WHERE attrelid='pg_auth_members'::regclass AND attname IN ('inherit_option','set_option') AND NOT attisdropped`).Scan(&hasOptions); err != nil {
		return err
	}
	query := `SELECT role.rolname,member.rolname,grantor.rolname,m.admin_option,NULL::boolean,NULL::boolean FROM pg_auth_members m JOIN pg_roles role ON role.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=$1) OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=$1) ORDER BY 1,2`
	if hasOptions {
		query = `SELECT role.rolname,member.rolname,grantor.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles role ON role.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=$1) OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=$1) ORDER BY 1,2`
	}
	rows, err := m.admin.Query(ctx, query, i.Role)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var x MembershipRecord
		if err = rows.Scan(&x.Role, &x.Member, &x.Grantor, &x.AdminOption, &x.InheritOption, &x.SetOption); err != nil {
			return err
		}
		i.Memberships = append(i.Memberships, x)
	}
	return rows.Err()
}
func (i *TenantInventory) applySafetyPolicy() {
	r := i.RoleCatalog
	if r.Exists && (!r.Login || r.Superuser || r.CreateRole || r.CreateDB || r.Replication || r.BypassRLS) {
		i.Blockers = append(i.Blockers, "tenant role has unsafe catalog attributes")
	}
	if r.Exists && (r.Comment == nil || !strings.HasPrefix(*r.Comment, "health-tenant-v1:"+i.TenantID.String()+":")) {
		i.Blockers = append(i.Blockers, "existing tenant role marker comment is missing or mismatched")
	}
	if len(i.Memberships) > 0 {
		i.Blockers = append(i.Blockers, "tenant role has unexpected memberships")
	}
}
