package tenants

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DatabaseAdminRole      = "health_admin"
	DatabaseRegistryRole   = "health_registry"
	legacyDatabaseRole     = "health_user"
	databaseIdentityMarker = "health-db-identity-v1"
)

var databaseIdentityIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var canonicalTenantRolePattern = regexp.MustCompile(`^health_t_[0-9a-f]{32}$`)

const databaseIdentityManifestVersion = 2
const databaseIdentityManifestMaxBytes = 4 << 20
const databaseIdentityAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('health-db-identity-bootstrap',0))`

// DatabaseIdentityConfig is deliberately independent from Config: identity
// bootstrap must be runnable before TENANT_DB_ISOLATION_ENABLED is enabled.
type DatabaseIdentityConfig struct {
	BootstrapDSN     string
	AdminPassword    string
	RegistryPassword string
}

func ParseDatabaseIdentityConfig(lookup func(string) (string, bool)) (DatabaseIdentityConfig, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}
	c := DatabaseIdentityConfig{
		BootstrapDSN:     get("TENANT_DB_BOOTSTRAP_DATABASE_URL"),
		AdminPassword:    get("HEALTH_ADMIN_DB_PASSWORD"),
		RegistryPassword: get("HEALTH_REGISTRY_DB_PASSWORD"),
	}
	if c.BootstrapDSN == "" {
		return DatabaseIdentityConfig{}, errors.New("database identity bootstrap connection is not configured (value redacted)")
	}
	return c, nil
}

type DatabaseIdentityBootstrap struct{ pool *pgxpool.Pool }

type DatabaseIdentityObjectOwner struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

type DatabaseIdentityLegacyGrant struct {
	ObjectType string `json:"object_type"`
	ObjectName string `json:"object_name"`
	Privilege  string `json:"privilege"`
}

type DatabaseIdentityManifest struct {
	Version               int                             `json:"version"`
	CreatedAt             string                          `json:"created_at"`
	Target                DatabaseIdentityTarget          `json:"target"`
	Checksum              string                          `json:"checksum"`
	RegistrySchemaExisted bool                            `json:"registry_schema_existed"`
	RegistrySchemaOwner   string                          `json:"registry_schema_owner,omitempty"`
	RegistrySchemaACLNull bool                            `json:"registry_schema_acl_was_null"`
	RegistrySchemaACL     []DatabaseIdentityACL           `json:"registry_schema_acl"`
	CatalogObjects        []DatabaseIdentityCatalogObject `json:"catalog_objects"`
	DefaultACLs           []DatabaseIdentityDefaultACL    `json:"default_acls"`
	Memberships           []DatabaseIdentityMembership    `json:"memberships"`
	FixedRoles            []DatabaseIdentityRoleState     `json:"fixed_roles"`
	DatabaseGrants        []DatabaseIdentityACL           `json:"database_grants"`
	UnsupportedObjects    []string                        `json:"unsupported_objects"`
	// Deprecated v1 compatibility fields remain empty in sealed v2 manifests.
	ObjectOwners        []DatabaseIdentityObjectOwner `json:"object_owners"`
	LegacyGrants        []DatabaseIdentityLegacyGrant `json:"legacy_grants"`
	LegacyBridgeExisted bool                          `json:"legacy_bridge_existed"`
}

type DatabaseIdentityACL struct {
	Grantor   string `json:"grantor"`
	Grantee   string `json:"grantee"`
	Privilege string `json:"privilege"`
	Grantable bool   `json:"grantable"`
}

type DatabaseIdentityCatalogObject struct {
	Kind            string                `json:"kind"`
	Name            string                `json:"name"`
	IdentityArgs    string                `json:"identity_args,omitempty"`
	Owner           string                `json:"owner"`
	ACLWasNull      bool                  `json:"acl_was_null"`
	ACL             []DatabaseIdentityACL `json:"acl"`
	SecurityDefiner bool                  `json:"security_definer,omitempty"`
	RoleConfig      []string              `json:"role_config,omitempty"`
}

type DatabaseIdentityDefaultACL struct {
	Owner      string                `json:"owner"`
	Schema     string                `json:"schema,omitempty"`
	ObjectType string                `json:"object_type"`
	ACL        []DatabaseIdentityACL `json:"acl"`
}

type DatabaseIdentityMembership struct {
	Granted       string `json:"granted"`
	Member        string `json:"member"`
	Grantor       string `json:"grantor"`
	AdminOption   bool   `json:"admin_option"`
	InheritOption bool   `json:"inherit_option"`
	SetOption     bool   `json:"set_option"`
}

type DatabaseIdentityRoleState struct {
	Name        string   `json:"name"`
	Existed     bool     `json:"existed"`
	Marker      *string  `json:"marker,omitempty"`
	Login       bool     `json:"login"`
	Superuser   bool     `json:"superuser"`
	CreateRole  bool     `json:"create_role"`
	CreateDB    bool     `json:"create_db"`
	Replication bool     `json:"replication"`
	BypassRLS   bool     `json:"bypass_rls"`
	Inherit     bool     `json:"inherit"`
	ConnLimit   int      `json:"conn_limit"`
	ValidUntil  *string  `json:"valid_until,omitempty"`
	Config      []string `json:"config"`
}

type DatabaseIdentityTarget struct {
	SystemIdentifier string `json:"system_identifier"`
	DatabaseOID      uint32 `json:"database_oid"`
	DatabaseName     string `json:"database_name"`
}

func (b *DatabaseIdentityBootstrap) Snapshot(ctx context.Context) (DatabaseIdentityManifest, error) {
	return snapshotDatabaseIdentity(ctx, b.pool)
}

func snapshotDatabaseIdentity(ctx context.Context, query databaseIdentityQuerier) (DatabaseIdentityManifest, error) {
	m := DatabaseIdentityManifest{Version: databaseIdentityManifestVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ObjectOwners: []DatabaseIdentityObjectOwner{}, LegacyGrants: []DatabaseIdentityLegacyGrant{}}
	if err := query.QueryRow(ctx, `SELECT system_identifier::text,(SELECT oid FROM pg_database WHERE datname=current_database()),current_database() FROM pg_control_system()`).Scan(&m.Target.SystemIdentifier, &m.Target.DatabaseOID, &m.Target.DatabaseName); err != nil {
		return m, err
	}
	if err := query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='health_registry')`).Scan(&m.RegistrySchemaExisted); err != nil {
		return m, err
	}
	if !m.RegistrySchemaExisted {
		if err := snapshotFixedIdentityState(ctx, query, &m); err != nil {
			return m, err
		}
		return sealDatabaseIdentityManifest(m)
	}
	if err := snapshotRegistryCatalog(ctx, query, &m); err != nil {
		return m, err
	}
	if err := snapshotFixedIdentityState(ctx, query, &m); err != nil {
		return m, err
	}
	return sealDatabaseIdentityManifest(m)
}

func WriteDatabaseIdentityManifest(path string, m DatabaseIdentityManifest) error {
	sealed, err := sealDatabaseIdentityManifest(m)
	if err != nil {
		return err
	}
	if err = validateDatabaseIdentityManifest(sealed); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+fmt.Sprintf("%x", nonce[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	payload := append(b, '\n')
	for len(payload) > 0 {
		n, writeErr := f.Write(payload)
		if writeErr != nil {
			return writeErr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Link publishes the fully synced inode atomically and, unlike Rename,
	// fails if the destination already exists. The temporary name is then
	// removed; readers can never observe partial bytes.
	if err = os.Link(tmp, path); err != nil {
		return err
	}
	if err = os.Remove(tmp); err != nil {
		_ = os.Remove(path)
		return err
	}
	cleanup = false
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	return errors.Join(syncErr, closeErr)
}

func ReadDatabaseIdentityManifest(path string) (DatabaseIdentityManifest, error) {
	var m DatabaseIdentityManifest
	st, err := os.Lstat(path)
	if err != nil {
		return m, err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return m, errors.New("database identity manifest must be a regular file")
	}
	if st.Mode().Perm() != 0600 {
		return m, errors.New("database identity manifest mode must be 0600")
	}
	if st.Size() < 1 || st.Size() > databaseIdentityManifestMaxBytes {
		return m, errors.New("database identity manifest size is invalid")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return m, errors.New("database identity manifest owner is invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return m, err
	}
	openedStat, openedOK := opened.Sys().(*syscall.Stat_t)
	if !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 ||
		opened.Size() < 1 || opened.Size() > databaseIdentityManifestMaxBytes ||
		!openedOK || int(openedStat.Uid) != os.Geteuid() {
		return m, errors.New("opened database identity manifest metadata is invalid")
	}
	// Lstat rejects symlinks and validates the pathname. Fstat validates the
	// inode actually opened. Matching device/inode closes the pathname-swap
	// window between those two operations without relying on platform-specific
	// O_NOFOLLOW support.
	if stat.Dev != openedStat.Dev || stat.Ino != openedStat.Ino {
		return m, errors.New("database identity manifest changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, databaseIdentityManifestMaxBytes+1))
	if err != nil || len(b) > databaseIdentityManifestMaxBytes {
		return m, errors.New("read database identity manifest failed")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if err = dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return m, errors.New("database identity manifest has trailing data")
	}
	if err = validateDatabaseIdentityManifest(m); err != nil {
		return m, err
	}
	return m, nil
}

func sealDatabaseIdentityManifest(m DatabaseIdentityManifest) (DatabaseIdentityManifest, error) {
	m.Version = databaseIdentityManifestVersion
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	sort.Slice(m.ObjectOwners, func(i, j int) bool {
		a, b := m.ObjectOwners[i], m.ObjectOwners[j]
		return a.Kind+"\x00"+a.Name+"\x00"+a.Owner < b.Kind+"\x00"+b.Name+"\x00"+b.Owner
	})
	sort.Slice(m.LegacyGrants, func(i, j int) bool {
		a, b := m.LegacyGrants[i], m.LegacyGrants[j]
		return a.ObjectType+"\x00"+a.ObjectName+"\x00"+a.Privilege < b.ObjectType+"\x00"+b.ObjectName+"\x00"+b.Privilege
	})
	sortIdentityACL(m.RegistrySchemaACL)
	sortIdentityACL(m.DatabaseGrants)
	for i := range m.CatalogObjects {
		sortIdentityACL(m.CatalogObjects[i].ACL)
		sort.Strings(m.CatalogObjects[i].RoleConfig)
	}
	sort.Slice(m.CatalogObjects, func(i, j int) bool {
		a, b := m.CatalogObjects[i], m.CatalogObjects[j]
		return a.Kind+"\x00"+a.Name+"\x00"+a.IdentityArgs < b.Kind+"\x00"+b.Name+"\x00"+b.IdentityArgs
	})
	for i := range m.DefaultACLs {
		sortIdentityACL(m.DefaultACLs[i].ACL)
	}
	sort.Slice(m.DefaultACLs, func(i, j int) bool {
		a, b := m.DefaultACLs[i], m.DefaultACLs[j]
		return a.Owner+"\x00"+a.Schema+"\x00"+a.ObjectType < b.Owner+"\x00"+b.Schema+"\x00"+b.ObjectType
	})
	sort.Slice(m.Memberships, func(i, j int) bool {
		a, b := m.Memberships[i], m.Memberships[j]
		return a.Granted+"\x00"+a.Member+"\x00"+a.Grantor < b.Granted+"\x00"+b.Member+"\x00"+b.Grantor
	})
	sort.Slice(m.FixedRoles, func(i, j int) bool { return m.FixedRoles[i].Name < m.FixedRoles[j].Name })
	for i := range m.FixedRoles {
		sort.Strings(m.FixedRoles[i].Config)
	}
	sort.Strings(m.UnsupportedObjects)
	m.Checksum = ""
	b, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	sum := sha256.Sum256(b)
	m.Checksum = fmt.Sprintf("%x", sum[:])
	return m, nil
}

func sortIdentityACL(values []DatabaseIdentityACL) {
	sort.Slice(values, func(i, j int) bool {
		a, b := values[i], values[j]
		return a.Grantee+"\x00"+a.Privilege+"\x00"+a.Grantor+fmt.Sprint(a.Grantable) < b.Grantee+"\x00"+b.Privilege+"\x00"+b.Grantor+fmt.Sprint(b.Grantable)
	})
}

func verifyDatabaseIdentityManifestChecksum(m DatabaseIdentityManifest) bool {
	want := m.Checksum
	sealed, err := sealDatabaseIdentityManifest(m)
	return err == nil && len(want) == sha256.Size*2 && sealed.Checksum == want
}

func OpenDatabaseIdentityBootstrap(ctx context.Context, dsn string) (*DatabaseIdentityBootstrap, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("parse bootstrap database configuration (details redacted)")
	}
	delete(cfg.ConnConfig.RuntimeParams, "search_path")
	cfg.MaxConns, cfg.MinConns = 1, 0
	cfg.MaxConnIdleTime = 30 * time.Second
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("open bootstrap database (details redacted)")
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, errors.New("ping bootstrap database (details redacted)")
	}
	return &DatabaseIdentityBootstrap{pool: p}, nil
}

func (b *DatabaseIdentityBootstrap) Close() { b.pool.Close() }

// Bootstrap creates or reconciles the two fixed service identities. All role,
// schema, and database identifiers are constants or server-derived identifiers;
// only passwords are supplied externally and they are never returned or logged.
func (b *DatabaseIdentityBootstrap) Bootstrap(ctx context.Context, manifest DatabaseIdentityManifest, adminPassword, registryPassword string) error {
	if err := validateDatabaseIdentityManifest(manifest); err != nil {
		return err
	}
	if len(manifest.UnsupportedObjects) != 0 {
		return errors.New("database identity bootstrap found unsupported registry objects")
	}
	for _, object := range manifest.CatalogObjects {
		if object.SecurityDefiner && !safeSecurityDefinerSearchPath(object.RoleConfig) {
			return errors.New("database identity bootstrap found an unsafe security-definer routine")
		}
	}
	if adminPassword == "" || registryPassword == "" {
		return errors.New("fixed database identity credentials are not configured (values redacted)")
	}
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, databaseIdentityAdvisoryLockSQL); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return err
	}
	if version < 160000 {
		return errors.New("database identity bootstrap requires PostgreSQL 16 or newer")
	}
	current, err := snapshotDatabaseIdentity(ctx, tx)
	if err != nil {
		return err
	}
	if current.Target != manifest.Target {
		return errors.New("database identity manifest target mismatch")
	}
	if err = preflightMembershipReplay(ctx, tx, manifest.Memberships); err != nil {
		return err
	}
	if !sameDatabaseIdentityPrestate(current, manifest) {
		if err = verifyBootstrappedManifestState(ctx, tx, manifest, current); err != nil {
			return errors.New("database identity manifest no longer matches current pre-bootstrap state")
		}
	}
	created := map[string]bool{}
	for _, role := range []string{DatabaseAdminRole, DatabaseRegistryRole} {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			created[role] = true
			if _, err = tx.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
				return errors.New("create fixed database identity failed (details redacted)")
			}
			var mark string
			if err = tx.QueryRow(ctx, `SELECT format('COMMENT ON ROLE %I IS %L',$1::text,$2::text)`, role, databaseIdentityMarker).Scan(&mark); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, mark); err != nil {
				return err
			}
		} else {
			var marker *string
			if err = tx.QueryRow(ctx, `SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, role).Scan(&marker); err != nil {
				return err
			}
			if marker == nil || *marker != databaseIdentityMarker {
				return errors.New("existing fixed database identity is not bootstrap-managed")
			}
		}
	}
	for _, role := range []string{DatabaseAdminRole, DatabaseRegistryRole} {
		if !created[role] {
			if err = validateFixedRolePreflight(ctx, tx, role); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `ALTER ROLE health_admin LOGIN CREATEROLE NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS INHERIT`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `ALTER ROLE health_registry LOGIN NOCREATEROLE NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS INHERIT`); err != nil {
		return err
	}
	if err = setFixedRolePassword(ctx, tx, DatabaseAdminRole, adminPassword); err != nil {
		return errors.New("set administrative database credential failed (details redacted)")
	}
	if err = setFixedRolePassword(ctx, tx, DatabaseRegistryRole, registryPassword); err != nil {
		return errors.New("set registry database credential failed (details redacted)")
	}
	var adminDatabaseGrant string
	if err = tx.QueryRow(ctx, `SELECT format('REVOKE ALL ON DATABASE %I FROM health_admin',current_database())`).Scan(&adminDatabaseGrant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, adminDatabaseGrant); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT format('GRANT CONNECT ON DATABASE %I TO health_admin',current_database())`).Scan(&adminDatabaseGrant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, adminDatabaseGrant); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT format('GRANT CREATE ON DATABASE %I TO health_admin WITH GRANT OPTION',current_database())`).Scan(&adminDatabaseGrant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, adminDatabaseGrant); err != nil {
		return err
	}
	var registryConnect string
	if err = tx.QueryRow(ctx, `SELECT format('REVOKE ALL ON DATABASE %I FROM health_registry',current_database())`).Scan(&registryConnect); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, registryConnect); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT format('GRANT CONNECT ON DATABASE %I TO health_registry',current_database())`).Scan(&registryConnect); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, registryConnect); err != nil {
		return err
	}
	var registryExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='health_registry')`).Scan(&registryExists); err != nil {
		return err
	}
	if !registryExists {
		if _, err = tx.Exec(ctx, `CREATE SCHEMA health_registry AUTHORIZATION health_registry`); err != nil {
			return err
		}
	}
	if err = reconcileRegistryOwnership(ctx, tx, manifest); err != nil {
		return err
	}
	// Existing shared-role installs need a narrow, explicitly temporary bridge
	// so health_admin can later transfer health_user-owned tenant objects. The
	// finalize mode removes it before the authoritative isolation audit.
	var legacyExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='health_user')`).Scan(&legacyExists); err != nil {
		return err
	}
	if legacyExists {
		if err = replaceLegacyBridgeWithCanonical(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `GRANT USAGE ON SCHEMA health_registry TO health_user`); err != nil {
			return err
		}
		if err = grantLegacyManifestCompatibility(ctx, tx, manifest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func grantLegacyManifestCompatibility(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	for _, object := range manifest.CatalogObjects {
		var privileges string
		switch object.Kind {
		case "TABLE", "PARTITIONED TABLE", "VIEW", "MATERIALIZED VIEW", "FOREIGN TABLE":
			privileges = "SELECT, INSERT, UPDATE, DELETE"
		case "SEQUENCE":
			privileges = "USAGE, SELECT, UPDATE"
		default:
			continue
		}
		_, target, err := catalogObjectSQL(ctx, tx, object, "TARGET", "")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "GRANT "+privileges+" ON "+objectGrantClass(object.Kind)+" "+target+" TO health_user"); err != nil {
			return err
		}
	}
	return nil
}

func sameDatabaseIdentityPrestate(a, b DatabaseIdentityManifest) bool {
	const comparisonTime = "2000-01-01T00:00:00Z"
	a.CreatedAt, b.CreatedAt = comparisonTime, comparisonTime
	a.Checksum, b.Checksum = "", ""
	a, errA := sealDatabaseIdentityManifest(a)
	b, errB := sealDatabaseIdentityManifest(b)
	return errA == nil && errB == nil && a.Checksum == b.Checksum
}

func verifyBootstrappedManifestState(ctx context.Context, tx pgx.Tx, manifest, current DatabaseIdentityManifest) error {
	if err := verifyBootstrapFixedState(ctx, tx); err != nil {
		return err
	}
	if len(current.UnsupportedObjects) != 0 {
		return errors.New("bootstrapped registry contains unsupported objects")
	}
	currentObjects := make(map[string]DatabaseIdentityCatalogObject, len(current.CatalogObjects))
	for _, object := range current.CatalogObjects {
		currentObjects[object.Kind+"\x00"+object.Name+"\x00"+object.IdentityArgs] = object
	}
	for _, expected := range manifest.CatalogObjects {
		object, ok := currentObjects[expected.Kind+"\x00"+expected.Name+"\x00"+expected.IdentityArgs]
		if !ok || object.Owner != DatabaseRegistryRole {
			return errors.New("bootstrapped registry inventory drift")
		}
	}
	var legacyExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='health_user')`).Scan(&legacyExists); err != nil {
		return err
	}
	if legacyExists {
		var exactBridge, allBridge int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT admin_option AND NOT inherit_option AND set_option),count(*) FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE granted.rolname='health_user' AND member.rolname='health_admin'`).Scan(&exactBridge, &allBridge); err != nil {
			return err
		}
		if exactBridge != 1 || allBridge != 1 {
			return errors.New("bootstrapped legacy bridge drift")
		}
	}
	return nil
}

func (b *DatabaseIdentityBootstrap) ValidateManifestTarget(ctx context.Context, manifest DatabaseIdentityManifest) error {
	if err := validateDatabaseIdentityManifest(manifest); err != nil {
		return err
	}
	return validateManifestTargetWithQuery(ctx, b.pool, manifest)
}

func validateManifestTargetWithQuery(ctx context.Context, query databaseIdentityQuerier, manifest DatabaseIdentityManifest) error {
	var current DatabaseIdentityTarget
	if err := query.QueryRow(ctx, `SELECT system_identifier::text,(SELECT oid FROM pg_database WHERE datname=current_database()),current_database() FROM pg_control_system()`).Scan(&current.SystemIdentifier, &current.DatabaseOID, &current.DatabaseName); err != nil {
		return err
	}
	if current != manifest.Target {
		return errors.New("database identity manifest target mismatch")
	}
	return nil
}

func safeSecurityDefinerSearchPath(config []string) bool {
	for _, setting := range config {
		if strings.HasPrefix(setting, "search_path=") {
			value := strings.ReplaceAll(strings.TrimPrefix(setting, "search_path="), " ", "")
			return value == "pg_catalog,health_registry"
		}
	}
	return false
}

func validateFixedRolePreflight(ctx context.Context, tx pgx.Tx, role string) error {
	var login, superuser, createRole, createDB, replication, bypassRLS bool
	var inherit bool
	var connLimit int
	var validUntil pgtype.Timestamptz
	var config []string
	if err := tx.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls,rolinherit,rolconnlimit,rolvaliduntil,coalesce(rolconfig,'{}'::text[]) FROM pg_roles WHERE rolname=$1`, role).Scan(&login, &superuser, &createRole, &createDB, &replication, &bypassRLS, &inherit, &connLimit, &validUntil, &config); err != nil {
		return err
	}
	wantCreateRole := role == DatabaseAdminRole
	validUntilUnbounded := !validUntil.Valid || validUntil.InfinityModifier == pgtype.Infinity
	if !login || superuser || createRole != wantCreateRole || createDB || replication || bypassRLS || !inherit || connLimit != -1 || !validUntilUnbounded || len(config) > 0 {
		return errors.New("marked fixed database identity has unsafe role attributes")
	}
	rows, err := tx.Query(ctx, `SELECT granted.rolname,member.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE granted.rolname=$1 OR member.rolname=$1`, role)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var granted, member string
		var admin, inh, set bool
		if err = rows.Scan(&granted, &member, &admin, &inh, &set); err != nil {
			return err
		}
		allowedTenant := role == DatabaseAdminRole && member == DatabaseAdminRole && canonicalTenantRolePattern.MatchString(granted) && admin && !inh && !set
		allowedBridge := role == DatabaseAdminRole && member == DatabaseAdminRole && granted == legacyDatabaseRole && !admin && !inh && set
		if !allowedTenant && !allowedBridge {
			return errors.New("marked fixed database identity has unexpected memberships")
		}
	}
	return rows.Err()
}

func setFixedRolePassword(ctx context.Context, tx pgx.Tx, role, password string) error {
	var statement string
	if err := tx.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L',$1::text,$2::text)`, role, password).Scan(&statement); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, statement)
	return err
}

func reconcileRegistryOwnership(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='health_registry')`).Scan(&exists); err != nil || !exists {
		return err
	}
	if len(manifest.UnsupportedObjects) != 0 {
		return errors.New("reconcile registry ownership blocked by unsupported objects")
	}
	for _, object := range manifest.CatalogObjects {
		var statement string
		switch object.Kind {
		case "TABLE", "PARTITIONED TABLE":
			statement = "ALTER TABLE " + pgx.Identifier{"health_registry", object.Name}.Sanitize() + " OWNER TO health_registry"
		case "VIEW":
			statement = "ALTER VIEW " + pgx.Identifier{"health_registry", object.Name}.Sanitize() + " OWNER TO health_registry"
		case "MATERIALIZED VIEW":
			statement = "ALTER MATERIALIZED VIEW " + pgx.Identifier{"health_registry", object.Name}.Sanitize() + " OWNER TO health_registry"
		case "SEQUENCE":
			statement = "ALTER SEQUENCE " + pgx.Identifier{"health_registry", object.Name}.Sanitize() + " OWNER TO health_registry"
		case "FOREIGN TABLE":
			statement = "ALTER FOREIGN TABLE " + pgx.Identifier{"health_registry", object.Name}.Sanitize() + " OWNER TO health_registry"
		case "TYPE", "DOMAIN":
			statement = "ALTER " + object.Kind + " " + pgx.Identifier{"health_registry", object.Name}.Sanitize() + " OWNER TO health_registry"
		case "FUNCTION", "PROCEDURE":
			identity := "health_registry." + pgx.Identifier{object.Name}.Sanitize() + "(" + object.IdentityArgs + ")"
			if err := tx.QueryRow(ctx, `SELECT format('ALTER %s %s OWNER TO health_registry',$1::text,to_regprocedure($2)::text)`, object.Kind, identity).Scan(&statement); err != nil {
				return err
			}
		default:
			return errors.New("reconcile registry ownership encountered unsupported object kind")
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("reconcile registry ownership: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `ALTER SCHEMA health_registry OWNER TO health_registry`); err != nil {
		return fmt.Errorf("reconcile registry schema ownership: %w", err)
	}
	if err := secureBootstrapRegistryACLs(ctx, tx, manifest); err != nil {
		return fmt.Errorf("secure registry ACLs: %w", err)
	}
	_, err := tx.Exec(ctx, `REVOKE ALL ON SCHEMA health_registry FROM PUBLIC`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE health_registry`); err != nil {
		return err
	}
	// PostgreSQL cannot subtract a global PUBLIC default through a per-schema
	// default ACL. Revoke these role-wide so future registry routines and types
	// are private in every schema where health_registry may create them.
	if _, err = tx.Exec(ctx, `ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `ALTER DEFAULT PRIVILEGES REVOKE USAGE ON TYPES FROM PUBLIC`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `REVOKE ALL ON ALL FUNCTIONS IN SCHEMA health_registry FROM PUBLIC`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DO $types$ DECLARE x record; BEGIN FOR x IN SELECT t.typname FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='health_registry' AND t.typelem=0 LOOP EXECUTE format('REVOKE ALL ON TYPE %I.%I FROM PUBLIC','health_registry',x.typname); END LOOP; END $types$`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `RESET ROLE`)
	return err
}

func (b *DatabaseIdentityBootstrap) Finalize(ctx context.Context, manifest DatabaseIdentityManifest) error {
	if err := validateDatabaseIdentityManifest(manifest); err != nil {
		return err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, databaseIdentityAdvisoryLockSQL); err != nil {
		return err
	}
	if err = validateManifestTargetWithQuery(ctx, tx, manifest); err != nil {
		return err
	}
	if err = verifyExactBootstrappedState(ctx, tx, manifest); err != nil {
		return err
	}
	var usersExists, operationsExists bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('health_registry.users') IS NOT NULL,to_regclass('health_registry.tenant_provisioning_operations') IS NOT NULL`).Scan(&usersExists, &operationsExists); err != nil {
		return err
	}
	if !usersExists || !operationsExists {
		return errors.New("database identity finalize requires initialized registry provisioning metadata")
	}
	var unsafeUsers, inFlight int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM health_registry.users WHERE provisioning_state IN ('pending','provisioning') OR (provisioning_state='active' AND NOT db_isolation_ready)`).Scan(&unsafeUsers); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM health_registry.tenant_provisioning_operations WHERE state IN ('pending','provisioning')`).Scan(&inFlight); err != nil {
		return err
	}
	if unsafeUsers != 0 || inFlight != 0 {
		return errors.New("database identity finalize blocked by incomplete tenant isolation")
	}
	if err = removeLegacyRegistryAuthority(ctx, tx); err != nil {
		return err
	}
	if err = verifyExactFinalizedState(ctx, tx, manifest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func verifyBootstrapFixedState(ctx context.Context, tx pgx.Tx) error {
	for _, role := range []string{DatabaseAdminRole, DatabaseRegistryRole} {
		var marker *string
		if err := tx.QueryRow(ctx, `SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, role).Scan(&marker); err != nil {
			return err
		}
		if marker == nil || *marker != databaseIdentityMarker {
			return errors.New("database identity fixed marker drift")
		}
		if err := validateFixedRolePreflight(ctx, tx, role); err != nil {
			return err
		}
	}
	var schemaOwner string
	if err := tx.QueryRow(ctx, `SELECT owner.rolname FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname='health_registry'`).Scan(&schemaOwner); err != nil {
		return err
	}
	if schemaOwner != DatabaseRegistryRole {
		return errors.New("database identity registry schema owner drift")
	}
	var foreignOwners int
	if err := tx.QueryRow(ctx, `
		SELECT
		 (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles o ON o.oid=c.relowner WHERE n.nspname='health_registry' AND c.relkind IN ('r','p','v','m','S','f') AND o.rolname<>'health_registry') +
		 (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles o ON o.oid=p.proowner WHERE n.nspname='health_registry' AND o.rolname<>'health_registry') +
		 (SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace JOIN pg_roles o ON o.oid=t.typowner WHERE n.nspname='health_registry' AND t.typrelid=0 AND t.typelem=0 AND t.typtype IN ('b','d','e','r','m') AND o.rolname<>'health_registry')`).Scan(&foreignOwners); err != nil {
		return err
	}
	if foreignOwners != 0 {
		return errors.New("database identity registry object owner drift")
	}
	return nil
}

// Rollback restores pre-bootstrap registry ownership and the legacy role's
// recorded grants. It intentionally retains roles, schemas, and data.
func (b *DatabaseIdentityBootstrap) Rollback(ctx context.Context, m DatabaseIdentityManifest) (DatabaseIdentityRollbackResult, error) {
	result := DatabaseIdentityRollbackResult{RetainedArtifacts: []string{}}
	if err := validateDatabaseIdentityManifest(m); err != nil {
		return result, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, databaseIdentityAdvisoryLockSQL); err != nil {
		return result, err
	}
	if err = validateManifestTargetWithQuery(ctx, tx, m); err != nil {
		return result, err
	}
	if err = preflightMembershipReplay(ctx, tx, m.Memberships); err != nil {
		return result, err
	}
	if m.RegistrySchemaExisted {
		if err = preflightExistingRegistryCatalog(ctx, tx, m); err != nil {
			return result, err
		}
		if err = restoreRegistryCatalog(ctx, tx, m); err != nil {
			return result, err
		}
	} else {
		result.RetainedArtifacts = append(result.RetainedArtifacts, "health_registry schema and data", "health_admin role", "health_registry role")
		if err = cleanupAbsentSchemaBootstrapAuthority(ctx, tx); err != nil {
			return result, err
		}
	}
	if err = restoreFixedIdentityState(ctx, tx, m); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func cleanupAbsentSchemaBootstrapAuthority(ctx context.Context, tx pgx.Tx) error {
	var legacyExists, schemaExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='health_user'),EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='health_registry')`).Scan(&legacyExists, &schemaExists); err != nil {
		return err
	}
	if !legacyExists || !schemaExists {
		return nil
	}
	for _, statement := range []string{
		`REVOKE ALL ON ALL TABLES IN SCHEMA health_registry FROM health_user`,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA health_registry FROM health_user`,
		`REVOKE ALL ON SCHEMA health_registry FROM health_user`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func preflightExistingRegistryCatalog(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	current, err := snapshotDatabaseIdentity(ctx, tx)
	if err != nil {
		return err
	}
	if !current.RegistrySchemaExisted || len(current.UnsupportedObjects) != 0 {
		return errors.New("registry catalog drift makes rollback ambiguous")
	}
	expected := map[string]bool{}
	for _, object := range manifest.CatalogObjects {
		expected[object.Kind+"\x00"+object.Name+"\x00"+object.IdentityArgs] = true
	}
	currentSet := map[string]bool{}
	for _, object := range current.CatalogObjects {
		currentSet[object.Kind+"\x00"+object.Name+"\x00"+object.IdentityArgs] = true
	}
	if len(expected) != len(currentSet) {
		return errors.New("registry catalog additions make rollback ambiguous")
	}
	for key := range expected {
		if !currentSet[key] {
			return errors.New("registry catalog identity drift makes rollback ambiguous")
		}
	}
	return nil
}

func validateDatabaseIdentityManifest(m DatabaseIdentityManifest) error {
	if m.Version != databaseIdentityManifestVersion {
		return errors.New("unsupported database identity manifest version")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		return errors.New("database identity manifest creation time is invalid")
	}
	if m.Target.SystemIdentifier == "" || m.Target.DatabaseOID == 0 || !databaseIdentityIdentifier.MatchString(m.Target.DatabaseName) {
		return errors.New("database identity manifest target is invalid")
	}
	if !verifyDatabaseIdentityManifestChecksum(m) {
		return errors.New("database identity manifest checksum mismatch")
	}
	if len(m.ObjectOwners) != 0 || len(m.LegacyGrants) != 0 {
		return errors.New("database identity manifest contains deprecated v1 inventory fields")
	}
	if len(m.UnsupportedObjects) > 0 {
		for _, value := range m.UnsupportedObjects {
			if value == "" || len(value) > 512 || strings.ContainsRune(value, '\x00') {
				return errors.New("database identity manifest contains invalid unsupported-object evidence")
			}
		}
	}
	if len(m.FixedRoles) != 2 {
		return errors.New("database identity manifest fixed role inventory is incomplete")
	}
	roleSeen := map[string]bool{}
	for _, role := range m.FixedRoles {
		if (role.Name != DatabaseAdminRole && role.Name != DatabaseRegistryRole) || roleSeen[role.Name] || len(role.Config) != 0 {
			return errors.New("database identity manifest fixed role state is invalid")
		}
		roleSeen[role.Name] = true
		if role.ConnLimit < -1 || role.ConnLimit > 2147483647 {
			return errors.New("database identity manifest fixed role connection limit is invalid")
		}
		if role.ValidUntil != nil {
			if _, err := time.Parse(time.RFC3339Nano, *role.ValidUntil); err != nil {
				return errors.New("database identity manifest fixed role validity is invalid")
			}
		}
	}
	if err := validateIdentityACLs("DATABASE", m.DatabaseGrants); err != nil {
		return err
	}
	membershipSeen := map[string]bool{}
	for _, membership := range m.Memberships {
		if !validIdentityPrincipal(membership.Granted) || !validIdentityPrincipal(membership.Member) || !validIdentityPrincipal(membership.Grantor) || membership.Granted == "PUBLIC" || membership.Member == "PUBLIC" || membership.Grantor == "PUBLIC" {
			return errors.New("database identity manifest membership identifiers are invalid")
		}
		if membership.Granted != legacyDatabaseRole || membership.Member != DatabaseAdminRole || membership.Granted == membership.Member || membership.AdminOption || membership.InheritOption || !membership.SetOption {
			return errors.New("database identity manifest membership is outside the exact legacy bridge scope")
		}
		key := membership.Granted + "\x00" + membership.Member + "\x00" + membership.Grantor
		if membershipSeen[key] {
			return errors.New("database identity manifest contains duplicate memberships")
		}
		membershipSeen[key] = true
	}
	if !m.RegistrySchemaExisted {
		if m.RegistrySchemaOwner != "" || len(m.RegistrySchemaACL) != 0 || len(m.CatalogObjects) != 0 || len(m.DefaultACLs) != 0 {
			return errors.New("database identity manifest has objects without a registry schema")
		}
		return nil
	}
	if !databaseIdentityIdentifier.MatchString(m.RegistrySchemaOwner) {
		return errors.New("database identity manifest contains unsafe owner")
	}
	if err := validateIdentityACLs("SCHEMA", m.RegistrySchemaACL); err != nil {
		return err
	}
	objectKinds := map[string]bool{"TABLE": true, "PARTITIONED TABLE": true, "VIEW": true, "MATERIALIZED VIEW": true, "SEQUENCE": true, "FOREIGN TABLE": true, "FUNCTION": true, "PROCEDURE": true, "TYPE": true, "DOMAIN": true}
	objects := map[string]bool{}
	logicalObjects := map[string]string{}
	for _, object := range m.CatalogObjects {
		if !objectKinds[object.Kind] || !databaseIdentityIdentifier.MatchString(object.Name) || !validIdentityPrincipal(object.Owner) || object.Owner == "PUBLIC" {
			return errors.New("database identity manifest catalog object is invalid")
		}
		isRoutine := object.Kind == "FUNCTION" || object.Kind == "PROCEDURE"
		if (!isRoutine && object.IdentityArgs != "") || len(object.IdentityArgs) > 2048 || strings.Contains(object.IdentityArgs, ";") || strings.Contains(object.IdentityArgs, "--") || strings.Contains(object.IdentityArgs, "/*") {
			return errors.New("database identity manifest routine identity is invalid")
		}
		key := object.Kind + "\x00" + object.Name + "\x00" + object.IdentityArgs
		if objects[key] {
			return errors.New("database identity manifest contains duplicate catalog objects")
		}
		objects[key] = true
		logicalClass := "relation\x00" + object.Name
		if isRoutine {
			logicalClass = "routine\x00" + object.Name + "\x00" + object.IdentityArgs
		} else if object.Kind == "TYPE" || object.Kind == "DOMAIN" {
			logicalClass = "type\x00" + object.Name
		}
		if prior, exists := logicalObjects[logicalClass]; exists && prior != object.Kind {
			return errors.New("database identity manifest contains contradictory catalog object kinds")
		}
		logicalObjects[logicalClass] = object.Kind
		if err := validateIdentityACLs(object.Kind, object.ACL); err != nil {
			return err
		}
	}
	defaultKinds := map[string]string{"r": "TABLE", "S": "SEQUENCE", "f": "FUNCTION", "T": "TYPE", "n": "SCHEMA"}
	defaultSeen := map[string]bool{}
	for _, d := range m.DefaultACLs {
		kind, ok := defaultKinds[d.ObjectType]
		if !ok || !validIdentityPrincipal(d.Owner) || d.Owner == "PUBLIC" || (d.Schema != "" && d.Schema != "health_registry") || (d.Schema != "" && d.ObjectType == "n") {
			return errors.New("database identity manifest default ACL is invalid")
		}
		key := d.Owner + "\x00" + d.Schema + "\x00" + d.ObjectType
		if defaultSeen[key] {
			return errors.New("database identity manifest contains duplicate default ACLs")
		}
		defaultSeen[key] = true
		if err := validateIdentityACLs(kind, d.ACL); err != nil {
			return err
		}
	}
	return nil
	/* v1 validation below is intentionally unreachable while the old fields
	remain in this file during the v2 replay refactor. */
	/*
		ownerKinds := map[string]bool{
			"TABLE": true, "SEQUENCE": true, "VIEW": true,
			"MATERIALIZED VIEW": true, "FOREIGN TABLE": true,
		}
		owners := make(map[string]bool, len(m.ObjectOwners))
		ownerKindByName := make(map[string]string, len(m.ObjectOwners))
		for _, o := range m.ObjectOwners {
			if !ownerKinds[o.Kind] || !databaseIdentityIdentifier.MatchString(o.Name) || !databaseIdentityIdentifier.MatchString(o.Owner) {
				return errors.New("database identity manifest contains an unsafe object owner")
			}
			// All supported owner objects share PostgreSQL's relation namespace, so
			// the same name under two claimed kinds is also an unsafe duplicate.
			key := o.Name
			if owners[key] {
				return errors.New("database identity manifest contains duplicate object owners")
			}
			owners[key] = true
			ownerKindByName[o.Name] = o.Kind
		}
		privileges := map[string]map[string]bool{
			"TABLE": {
				"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
				"TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true,
			},
			"SEQUENCE": {"USAGE": true, "SELECT": true, "UPDATE": true},
		}
		grants := make(map[string]bool, len(m.LegacyGrants))
		for _, g := range m.LegacyGrants {
			allowed, objectTypeOK := privileges[g.ObjectType]
			if !objectTypeOK || !allowed[g.Privilege] || !databaseIdentityIdentifier.MatchString(g.ObjectName) {
				return errors.New("database identity manifest contains an unsafe legacy grant")
			}
			ownerKind, objectExists := ownerKindByName[g.ObjectName]
			classMatches := g.ObjectType == "SEQUENCE" && ownerKind == "SEQUENCE"
			if g.ObjectType == "TABLE" {
				classMatches = map[string]bool{"TABLE": true, "VIEW": true, "MATERIALIZED VIEW": true, "FOREIGN TABLE": true}[ownerKind]
			}
			if !objectExists || !classMatches {
				return errors.New("database identity manifest legacy grant does not match its object class")
			}
			key := g.ObjectType + "\x00" + g.ObjectName + "\x00" + g.Privilege
			if grants[key] {
				return errors.New("database identity manifest contains duplicate legacy grants")
			}
			grants[key] = true
		}
		return nil */
}

func validIdentityPrincipal(value string) bool {
	return value == "PUBLIC" || databaseIdentityIdentifier.MatchString(value)
}

func validateIdentityACLs(kind string, values []DatabaseIdentityACL) error {
	privileges := map[string]map[string]bool{
		"SCHEMA":            {"USAGE": true, "CREATE": true},
		"TABLE":             {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
		"PARTITIONED TABLE": {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
		"VIEW":              {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
		"MATERIALIZED VIEW": {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
		"FOREIGN TABLE":     {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
		"SEQUENCE":          {"USAGE": true, "SELECT": true, "UPDATE": true},
		"FUNCTION":          {"EXECUTE": true}, "PROCEDURE": {"EXECUTE": true},
		"TYPE": {"USAGE": true}, "DOMAIN": {"USAGE": true},
		"DATABASE": {"CONNECT": true, "CREATE": true, "TEMPORARY": true, "TEMP": true},
	}
	allowed, ok := privileges[kind]
	if !ok {
		return errors.New("database identity manifest ACL object kind is unsupported")
	}
	seen := map[string]bool{}
	logical := map[string]bool{}
	for _, acl := range values {
		if !validIdentityPrincipal(acl.Grantor) || !validIdentityPrincipal(acl.Grantee) || acl.Grantor == "PUBLIC" || !allowed[acl.Privilege] {
			return errors.New("database identity manifest ACL entry is invalid")
		}
		key := acl.Grantor + "\x00" + acl.Grantee + "\x00" + acl.Privilege + fmt.Sprint(acl.Grantable)
		if seen[key] {
			return errors.New("database identity manifest contains duplicate ACL entries")
		}
		seen[key] = true
		logicalKey := acl.Grantor + "\x00" + acl.Grantee + "\x00" + acl.Privilege
		if grantable, exists := logical[logicalKey]; exists && grantable != acl.Grantable {
			return errors.New("database identity manifest contains contradictory ACL entries")
		}
		logical[logicalKey] = acl.Grantable
	}
	return nil
}

func canonicalAdminMembership(m MembershipRecord) bool {
	return canonicalTenantRolePattern.MatchString(m.Role) && m.Member == DatabaseAdminRole && m.AdminOption && m.InheritOption != nil && !*m.InheritOption && m.SetOption != nil && !*m.SetOption
}

func canonicalTenantMemberships(role string, memberships []MembershipRecord) bool {
	return len(memberships) == 1 && memberships[0].Role == role && canonicalAdminMembership(memberships[0])
}

// withTemporaryAdminSet enables SET only inside the catalog transaction that
// needs an ownership transition. Rollback restores the prior canonical grant;
// successful commits explicitly return to SET FALSE before becoming visible.
func withTemporaryAdminSet(ctx context.Context, tx pgx.Tx, role string, fn func() error) error {
	grant := "GRANT " + pgx.Identifier{role}.Sanitize() + " TO health_admin WITH INHERIT FALSE, SET TRUE"
	if _, err := tx.Exec(ctx, grant); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		return err
	}
	// PostgreSQL 16 records the canonical automatic membership with its own
	// grantor. Remove only our transaction-scoped health_admin grant so the
	// original ADMIN TRUE / INHERIT FALSE / SET FALSE row remains untouched.
	revoke := "REVOKE " + pgx.Identifier{role}.Sanitize() + " FROM health_admin GRANTED BY health_admin"
	_, err := tx.Exec(ctx, revoke)
	return err
}
