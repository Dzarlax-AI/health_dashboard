package tenants

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDatabaseIdentityValidUntilNormalization(t *testing.T) {
	finite := time.Date(2026, 7, 19, 12, 34, 56, 789, time.FixedZone("test", 2*60*60))
	tests := []struct {
		name    string
		value   pgtype.Timestamptz
		want    string
		wantNil bool
		wantErr bool
	}{
		{name: "null", value: pgtype.Timestamptz{}, wantNil: true},
		{name: "positive infinity", value: pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.Infinity}, wantNil: true},
		{name: "finite UTC", value: pgtype.Timestamptz{Valid: true, Time: finite}, want: finite.UTC().Format(time.RFC3339Nano)},
		{name: "negative infinity rejected", value: pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.NegativeInfinity}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := databaseIdentityValidUntil(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got=%q want nil", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("got=%v want=%q", got, tc.want)
			}
		})
	}
}

func TestDatabaseIdentityConfigRequiresDedicatedSecrets(t *testing.T) {
	_, err := ParseDatabaseIdentityConfig(func(key string) (string, bool) {
		values := map[string]string{}
		v, ok := values[key]
		return v, ok
	})
	if err == nil || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("expected redacted missing-connection error, got %v", err)
	}
}

func TestDatabaseIdentityManifestSecureIO(t *testing.T) {
	base := DatabaseIdentityManifest{
		Version:   databaseIdentityManifestVersion,
		CreatedAt: "2026-07-19T12:00:00Z",
		Target:    DatabaseIdentityTarget{SystemIdentifier: "123456", DatabaseOID: 42, DatabaseName: "aistack"},
		FixedRoles: []DatabaseIdentityRoleState{
			{Name: DatabaseAdminRole, Config: []string{}},
			{Name: DatabaseRegistryRole, Config: []string{}},
		},
	}
	base, _ = sealDatabaseIdentityManifest(base)
	t.Run("round trip and no overwrite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := WriteDatabaseIdentityManifest(path, base); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDatabaseIdentityManifest(path); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		if err := WriteDatabaseIdentityManifest(path, base); err == nil {
			t.Fatal("existing manifest was overwritten")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("existing manifest bytes changed")
		}
		if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".manifest.json.tmp-*")); len(matches) != 0 {
			t.Fatalf("temporary files leaked: %v", matches)
		}
	})
	t.Run("wrong mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := WriteDatabaseIdentityManifest(path, base); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDatabaseIdentityManifest(path); err == nil {
			t.Fatal("wrong mode accepted")
		}
	})
	t.Run("wrong owner when privileged", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("changing file ownership requires root")
		}
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := WriteDatabaseIdentityManifest(path, base); err != nil {
			t.Fatal(err)
		}
		foreignUID := 1
		if foreignUID == os.Geteuid() {
			foreignUID = 2
		}
		if err := os.Chown(path, foreignUID, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDatabaseIdentityManifest(path); err == nil {
			t.Fatal("foreign-owned manifest accepted")
		}
	})
	t.Run("symlink and nonregular", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := WriteDatabaseIdentityManifest(target, base); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDatabaseIdentityManifest(link); err == nil {
			t.Fatal("symlink accepted")
		}
		if _, err := ReadDatabaseIdentityManifest(dir); err == nil {
			t.Fatal("directory accepted")
		}
	})
	t.Run("unknown trailing corrupt and oversized", func(t *testing.T) {
		dir := t.TempDir()
		validPath := filepath.Join(dir, "valid")
		if err := WriteDatabaseIdentityManifest(validPath, base); err != nil {
			t.Fatal(err)
		}
		valid, _ := os.ReadFile(validPath)
		cases := map[string][]byte{
			"unknown":  []byte(strings.Replace(string(valid), `"version": 2,`, `"version": 2, "unknown": true,`, 1)),
			"trailing": append(append([]byte{}, valid...), []byte(` {}`)...),
			"corrupt":  []byte(`{"version":2,"created_at":`),
			"checksum": []byte(strings.Replace(string(valid), `"aistack"`, `"otherdb"`, 1)),
			"oversize": make([]byte, databaseIdentityManifestMaxBytes+1),
		}
		for name, data := range cases {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadDatabaseIdentityManifest(path); err == nil {
				t.Fatalf("%s manifest accepted", name)
			}
		}
	})
}

func TestValidateDatabaseIdentityManifestRejectsHostileTokensAndDuplicates(t *testing.T) {
	base := DatabaseIdentityManifest{
		Version:               databaseIdentityManifestVersion,
		CreatedAt:             "2026-07-19T12:00:00Z",
		Target:                DatabaseIdentityTarget{SystemIdentifier: "123456", DatabaseOID: 42, DatabaseName: "aistack"},
		RegistrySchemaExisted: true,
		RegistrySchemaOwner:   "health_user",
		RegistrySchemaACL:     []DatabaseIdentityACL{{Grantor: "health_user", Grantee: "health_user", Privilege: "USAGE"}},
		CatalogObjects: []DatabaseIdentityCatalogObject{
			{Kind: "TABLE", Name: "users", Owner: "health_user", ACL: []DatabaseIdentityACL{{Grantor: "health_user", Grantee: "health_user", Privilege: "SELECT"}}},
		},
		FixedRoles: []DatabaseIdentityRoleState{
			{Name: DatabaseAdminRole, Config: []string{}},
			{Name: DatabaseRegistryRole, Config: []string{}},
		},
	}
	base, _ = sealDatabaseIdentityManifest(base)
	if err := validateDatabaseIdentityManifest(base); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	cases := map[string]DatabaseIdentityManifest{}
	x := base
	x.RegistrySchemaOwner = "health_user;DROP ROLE health_admin"
	cases["owner semicolon"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "TABLE; DROP SCHEMA public", Name: "users", Owner: "health_user"}}
	cases["invalid owner kind"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "TABLE", Name: "users;drop", Owner: "health_user"}}
	cases["object semicolon"] = x
	x = base
	x.CatalogObjects = append(append([]DatabaseIdentityCatalogObject{}, base.CatalogObjects...), base.CatalogObjects[0])
	cases["duplicate owner"] = x
	x = base
	x.DefaultACLs = []DatabaseIdentityDefaultACL{{Owner: "health_user", ObjectType: "x"}}
	cases["invalid object type"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "TABLE", Name: "users", Owner: "health_user", ACL: []DatabaseIdentityACL{{Grantor: "health_user", Grantee: "health_user", Privilege: "SELECT;DROP"}}}}
	cases["invalid privilege"] = x
	x = base
	x.RegistrySchemaACL = []DatabaseIdentityACL{{Grantor: "health_user", Grantee: "health_user", Privilege: "USAGE"}, {Grantor: "health_user", Grantee: "health_user", Privilege: "USAGE"}}
	cases["duplicate grant"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "FUNCTION", Name: "unsafe", IdentityArgs: "int); DROP SCHEMA public; --", Owner: "health_user"}}
	cases["hostile routine identity"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "TABLE", Name: "users", Owner: "PUBLIC"}}
	cases["public object owner"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "TABLE", Name: "users", Owner: "health_user"}, {Kind: "VIEW", Name: "users", Owner: "health_user"}}
	cases["contradictory logical object kinds"] = x
	x = base
	x.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "TABLE", Name: "users", Owner: "health_user", ACL: []DatabaseIdentityACL{{Grantor: "health_user", Grantee: DatabaseAdminRole, Privilege: "SELECT"}, {Grantor: "health_user", Grantee: DatabaseAdminRole, Privilege: "SELECT", Grantable: true}}}}
	cases["contradictory grantability"] = x
	x = base
	x.DefaultACLs = []DatabaseIdentityDefaultACL{{Owner: "health_user", Schema: "health_registry", ObjectType: "n"}}
	cases["schema scoped schema default ACL"] = x
	x = base
	x.Target.SystemIdentifier = ""
	cases["missing target system identifier"] = x
	x = base
	x.Target.DatabaseOID = 0
	cases["missing target database oid"] = x
	x = base
	x.FixedRoles = []DatabaseIdentityRoleState{{Name: DatabaseAdminRole}, {Name: DatabaseAdminRole}}
	cases["duplicate fixed role"] = x
	x = base
	x.Memberships = []DatabaseIdentityMembership{
		{Granted: legacyDatabaseRole, Member: DatabaseAdminRole, Grantor: "postgres"},
		{Granted: legacyDatabaseRole, Member: DatabaseAdminRole, Grantor: "postgres"},
	}
	cases["duplicate membership"] = x
	x = base
	x.Memberships = []DatabaseIdentityMembership{{Granted: "PUBLIC", Member: DatabaseAdminRole, Grantor: "postgres", SetOption: true}}
	cases["public membership principal"] = x
	x = base
	x.Memberships = []DatabaseIdentityMembership{{Granted: DatabaseAdminRole, Member: DatabaseAdminRole, Grantor: "postgres", SetOption: true}}
	cases["self membership"] = x
	x = base
	x.Memberships = []DatabaseIdentityMembership{{Granted: DatabaseRegistryRole, Member: DatabaseAdminRole, Grantor: "postgres", SetOption: true}}
	cases["arbitrary fixed membership"] = x
	x = base
	x.Memberships = []DatabaseIdentityMembership{{Granted: legacyDatabaseRole, Member: DatabaseAdminRole, Grantor: "postgres", AdminOption: true, SetOption: true}}
	cases["unsafe legacy bridge options"] = x
	x = base
	x.FixedRoles = []DatabaseIdentityRoleState{{Name: DatabaseAdminRole, ConnLimit: -2}, {Name: DatabaseRegistryRole}}
	cases["invalid connection limit"] = x
	x = base
	x.FixedRoles = []DatabaseIdentityRoleState{{Name: DatabaseAdminRole, ConnLimit: 2147483648}, {Name: DatabaseRegistryRole}}
	cases["connection limit above postgres int32"] = x
	x = base
	x.DefaultACLs = []DatabaseIdentityDefaultACL{
		{Owner: legacyDatabaseRole, Schema: "health_registry", ObjectType: "r"},
		{Owner: legacyDatabaseRole, Schema: "health_registry", ObjectType: "r"},
	}
	cases["duplicate default ACL"] = x
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, _ = sealDatabaseIdentityManifest(manifest)
			if err := validateDatabaseIdentityManifest(manifest); err == nil {
				t.Fatal("hostile manifest accepted")
			}
			// A nil bootstrap pool proves invalid input is rejected before Begin.
			if _, err := (&DatabaseIdentityBootstrap{}).Rollback(context.Background(), manifest); err == nil {
				t.Fatal("rollback accepted hostile manifest")
			}
		})
	}
	v1 := base
	v1.Version = 1
	if err := validateDatabaseIdentityManifest(v1); err == nil {
		t.Fatal("v1 manifest accepted")
	}
}

func TestValidateDatabaseIdentityManifestAcceptsPostgres17MaintainPrivilege(t *testing.T) {
	base := DatabaseIdentityManifest{
		Version:               databaseIdentityManifestVersion,
		CreatedAt:             "2026-07-20T00:00:00Z",
		Target:                DatabaseIdentityTarget{SystemIdentifier: "123456", DatabaseOID: 42, DatabaseName: "aistack"},
		RegistrySchemaExisted: true,
		RegistrySchemaOwner:   legacyDatabaseRole,
		FixedRoles: []DatabaseIdentityRoleState{
			{Name: DatabaseAdminRole, Config: []string{}},
			{Name: DatabaseRegistryRole, Config: []string{}},
		},
	}
	acl := []DatabaseIdentityACL{{Grantor: legacyDatabaseRole, Grantee: legacyDatabaseRole, Privilege: "MAINTAIN"}}
	var normalizedDefaultHasMaintain bool
	for _, entry := range defaultEffectiveACL(legacyDatabaseRole, "r") {
		normalizedDefaultHasMaintain = normalizedDefaultHasMaintain || entry.Privilege == "MAINTAIN"
	}
	if !normalizedDefaultHasMaintain {
		t.Fatal("normalized default table ACL omits MAINTAIN")
	}
	for _, kind := range []string{"TABLE", "PARTITIONED TABLE", "VIEW", "MATERIALIZED VIEW", "FOREIGN TABLE"} {
		t.Run(kind, func(t *testing.T) {
			manifest := base
			manifest.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: kind, Name: "relation", Owner: legacyDatabaseRole, ACL: acl}}
			manifest, _ = sealDatabaseIdentityManifest(manifest)
			if err := validateDatabaseIdentityManifest(manifest); err != nil {
				t.Fatalf("MAINTAIN rejected for %s: %v", kind, err)
			}
		})
	}
	t.Run("default tables", func(t *testing.T) {
		manifest := base
		manifest.DefaultACLs = []DatabaseIdentityDefaultACL{{Owner: legacyDatabaseRole, ObjectType: "r", ACL: acl}}
		manifest, _ = sealDatabaseIdentityManifest(manifest)
		if err := validateDatabaseIdentityManifest(manifest); err != nil {
			t.Fatalf("MAINTAIN rejected for default table ACL: %v", err)
		}
	})
	t.Run("sequence rejected", func(t *testing.T) {
		manifest := base
		manifest.CatalogObjects = []DatabaseIdentityCatalogObject{{Kind: "SEQUENCE", Name: "relation", Owner: legacyDatabaseRole, ACL: acl}}
		manifest, _ = sealDatabaseIdentityManifest(manifest)
		if err := validateDatabaseIdentityManifest(manifest); err == nil {
			t.Fatal("MAINTAIN accepted for sequence")
		}
	})
}

func TestCanonicalAdminMembership(t *testing.T) {
	f := false
	role := "health_t_11111111111141118111111111111111"
	if !canonicalAdminMembership(MembershipRecord{Role: role, Member: DatabaseAdminRole, AdminOption: true, InheritOption: &f, SetOption: &f}) {
		t.Fatal("canonical PostgreSQL 16 membership rejected")
	}
	tt := true
	for _, changed := range []MembershipRecord{
		{Role: role, Member: DatabaseAdminRole, AdminOption: false, InheritOption: &f, SetOption: &f},
		{Role: role, Member: DatabaseAdminRole, AdminOption: true, InheritOption: &tt, SetOption: &f},
		{Role: role, Member: DatabaseAdminRole, AdminOption: true, InheritOption: &f, SetOption: &tt},
	} {
		if canonicalAdminMembership(changed) {
			t.Fatalf("altered membership accepted: %+v", changed)
		}
	}
}
