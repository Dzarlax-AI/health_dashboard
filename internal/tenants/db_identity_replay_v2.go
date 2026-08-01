package tenants

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/registry"
)

type DatabaseIdentityRollbackResult struct {
	RetainedArtifacts []string `json:"retained_artifacts"`
}

func expectedPrivateObjectACL(kind string, includeLegacy bool, relationDefaultACL []DatabaseIdentityACL) []DatabaseIdentityACL {
	objectType := "r"
	if kind == "SEQUENCE" {
		objectType = "S"
	} else if kind == "FUNCTION" || kind == "PROCEDURE" {
		objectType = "f"
	} else if kind == "TYPE" || kind == "DOMAIN" {
		objectType = "T"
	}
	out := defaultEffectiveACL(DatabaseRegistryRole, objectType)
	if objectType == "r" {
		out = append([]DatabaseIdentityACL(nil), relationDefaultACL...)
	}
	if objectType == "f" || objectType == "T" {
		out = withoutPublic(out)
	}
	if includeLegacy {
		privileges := []string{"SELECT", "INSERT", "UPDATE", "DELETE"}
		if objectType == "S" {
			privileges = []string{"USAGE", "SELECT", "UPDATE"}
		}
		if objectType == "r" || objectType == "S" {
			for _, privilege := range privileges {
				out = append(out, DatabaseIdentityACL{Grantor: DatabaseRegistryRole, Grantee: legacyDatabaseRole, Privilege: privilege})
			}
		}
	}
	return out
}

func verifyExactBootstrappedState(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	if err := verifyBootstrapFixedState(ctx, tx); err != nil {
		return err
	}
	current, err := snapshotDatabaseIdentity(ctx, tx)
	if err != nil {
		return err
	}
	if len(current.UnsupportedObjects) != 0 {
		return errors.New("database identity finalize found registry catalog additions")
	}
	relationDefaultACL, err := systemDefaultEffectiveACL(ctx, tx, DatabaseRegistryRole, "r")
	if err != nil {
		return err
	}
	expectedObjects := map[string]DatabaseIdentityCatalogObject{}
	for _, object := range manifest.CatalogObjects {
		expectedObjects[object.Kind+"\x00"+object.Name+"\x00"+object.IdentityArgs] = object
	}
	var legacyExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='health_user')`).Scan(&legacyExists); err != nil {
		return err
	}
	seenExpected := map[string]bool{}
	bootstrapACLModel, finalizedACLModel := true, true
	for _, object := range current.CatalogObjects {
		key := object.Kind + "\x00" + object.Name + "\x00" + object.IdentityArgs
		before, captured := expectedObjects[key]
		if !captured {
			if !registry.IsSchemaContractRelation(object.Kind, object.Name) || object.Owner != DatabaseRegistryRole || object.SecurityDefiner || len(object.RoleConfig) != 0 || !sameEffectiveACL(object.ACL, expectedPrivateObjectACL(object.Kind, false, relationDefaultACL)) {
				return errors.New("database identity finalize found unknown registry catalog additions")
			}
			continue
		}
		seenExpected[key] = true
		if object.Owner != DatabaseRegistryRole || object.SecurityDefiner != before.SecurityDefiner || !sameStrings(object.RoleConfig, before.RoleConfig) {
			return fmt.Errorf("database identity finalize found registry object drift for %s %s", object.Kind, object.Name)
		}
		bootstrapACLModel = bootstrapACLModel && sameEffectiveACL(object.ACL, expectedPrivateObjectACL(object.Kind, legacyExists, relationDefaultACL))
		finalizedACLModel = finalizedACLModel && sameEffectiveACL(object.ACL, expectedFinalizedObjectACL(object, relationDefaultACL))
	}
	if len(seenExpected) != len(expectedObjects) {
		return errors.New("database identity finalize found missing registry objects")
	}
	if err = registry.VerifySchemaContract(ctx, tx); err != nil {
		return err
	}
	expectedSchemaACL := defaultEffectiveACL(DatabaseRegistryRole, "n")
	if legacyExists {
		expectedSchemaACL = append(expectedSchemaACL, DatabaseIdentityACL{Grantor: DatabaseRegistryRole, Grantee: legacyDatabaseRole, Privilege: "USAGE"})
	}
	finalizedSchemaACL := defaultEffectiveACL(DatabaseRegistryRole, "n")
	bootstrapACLModel = bootstrapACLModel && sameEffectiveACL(current.RegistrySchemaACL, expectedSchemaACL)
	finalizedACLModel = finalizedACLModel && sameEffectiveACL(current.RegistrySchemaACL, finalizedSchemaACL)
	if current.RegistrySchemaOwner != DatabaseRegistryRole || (!bootstrapACLModel && !finalizedACLModel) {
		return errors.New("database identity finalize found registry schema ACL drift")
	}
	expectedDatabase := []DatabaseIdentityACL{
		{Grantee: DatabaseAdminRole, Privilege: "CONNECT"},
		{Grantee: DatabaseAdminRole, Privilege: "CREATE", Grantable: true},
		{Grantee: DatabaseRegistryRole, Privilege: "CONNECT"},
	}
	if !sameEffectiveACL(current.DatabaseGrants, expectedDatabase) {
		return errors.New("database identity finalize found fixed database grant drift")
	}
	if err = verifyExactBootstrappedMemberships(ctx, tx, manifest, legacyExists); err != nil {
		return err
	}
	if err = verifyBootstrappedDefaultACLs(manifest, current); err != nil {
		return err
	}
	if err = verifyRegistryTenantDenials(ctx, tx); err != nil {
		return err
	}
	return nil
}

func verifyExactBootstrappedMemberships(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest, legacyExists bool) error {
	current, err := currentFixedMemberships(ctx, tx)
	if err != nil {
		return err
	}
	var currentBridge, other []DatabaseIdentityMembership
	for _, membership := range current {
		if membership.Granted == legacyDatabaseRole && membership.Member == DatabaseAdminRole {
			currentBridge = append(currentBridge, membership)
		} else {
			other = append(other, membership)
		}
	}
	var finalizedBridge []DatabaseIdentityMembership
	var canonicalBridge []DatabaseIdentityMembership
	if legacyExists {
		var grantor string
		if err = tx.QueryRow(ctx, `SELECT session_user`).Scan(&grantor); err != nil {
			return err
		}
		canonicalBridge = []DatabaseIdentityMembership{{Granted: legacyDatabaseRole, Member: DatabaseAdminRole, Grantor: grantor, SetOption: true}}
	}
	if !sameMemberships(currentBridge, canonicalBridge) && !sameMemberships(currentBridge, finalizedBridge) {
		return errors.New("database identity finalize found legacy bridge drift")
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT db_role FROM health_registry.users WHERE db_role IS NOT NULL ORDER BY db_role`)
	if err != nil {
		return err
	}
	expectedRoles := map[string]bool{}
	for rows.Next() {
		var role string
		if err = rows.Scan(&role); err != nil {
			rows.Close()
			return err
		}
		if !canonicalTenantRolePattern.MatchString(role) {
			rows.Close()
			return errors.New("database identity finalize found invalid registered tenant role")
		}
		expectedRoles[role] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(other) != len(expectedRoles) {
		return errors.New("database identity finalize found fixed membership drift")
	}
	seenRoles := map[string]bool{}
	for _, membership := range other {
		if !expectedRoles[membership.Granted] || seenRoles[membership.Granted] || membership.Member != DatabaseAdminRole || !membership.AdminOption || membership.InheritOption || membership.SetOption {
			return errors.New("database identity finalize found fixed membership drift")
		}
		seenRoles[membership.Granted] = true
	}
	return nil
}

func expectedFinalizedObjectACL(object DatabaseIdentityCatalogObject, relationDefaultACL []DatabaseIdentityACL) []DatabaseIdentityACL {
	return expectedPrivateObjectACL(object.Kind, false, relationDefaultACL)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func verifyBootstrappedDefaultACLs(manifest, current DatabaseIdentityManifest) error {
	expected := map[string][]DatabaseIdentityACL{}
	for _, d := range manifest.DefaultACLs {
		key := d.Owner + "\x00" + d.Schema + "\x00" + d.ObjectType
		if d.Schema == "" {
			expected[key] = d.ACL
		} else {
			expected[key] = []DatabaseIdentityACL{}
		}
	}
	for _, objectType := range []string{"f", "T"} {
		expected[DatabaseRegistryRole+"\x00\x00"+objectType] = withoutPublic(defaultEffectiveACL(DatabaseRegistryRole, objectType))
	}
	for _, d := range current.DefaultACLs {
		key := d.Owner + "\x00" + d.Schema + "\x00" + d.ObjectType
		want, ok := expected[key]
		if !ok || !sameEffectiveACL(d.ACL, want) {
			return errors.New("database identity finalize found default ACL drift")
		}
		delete(expected, key)
	}
	for key, want := range expected {
		if len(want) != 0 {
			_ = key
			return errors.New("database identity finalize is missing expected default ACL state")
		}
	}
	return nil
}

func withoutPublic(values []DatabaseIdentityACL) []DatabaseIdentityACL {
	var out []DatabaseIdentityACL
	for _, acl := range values {
		if acl.Grantee != "PUBLIC" {
			out = append(out, acl)
		}
	}
	return out
}

func verifyRegistryTenantDenials(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT schema_name FROM health_registry.users WHERE schema_name IS NOT NULL ORDER BY schema_name`)
	if err != nil {
		return err
	}
	var schemas []string
	for rows.Next() {
		var schema string
		if err = rows.Scan(&schema); err != nil {
			rows.Close()
			return err
		}
		schemas = append(schemas, schema)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, schema := range schemas {
		var leaked bool
		if err = tx.QueryRow(ctx, `SELECT has_schema_privilege('health_registry',$1,'USAGE') OR has_schema_privilege('health_registry',$1,'CREATE') OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry') OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace CROSS JOIN LATERAL aclexplode(p.proacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry') OR EXISTS(SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace CROSS JOIN LATERAL aclexplode(t.typacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry') OR EXISTS(SELECT 1 FROM pg_default_acl d JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname=$1 AND g.rolname='health_registry')`, schema).Scan(&leaked); err != nil {
			return err
		}
		if leaked {
			return errors.New("database identity finalize found registry-to-tenant authority")
		}
	}
	return nil
}

func removeLegacyRegistryAuthority(ctx context.Context, tx pgx.Tx) error {
	var legacyExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='health_user')`).Scan(&legacyExists); err != nil || !legacyExists {
		return err
	}
	currentBridge, err := currentLegacyBridgeMemberships(ctx, tx)
	if err != nil {
		return err
	}
	for _, membership := range currentBridge {
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{membership.Grantor}.Sanitize()); err != nil {
			return err
		}
		statement := "REVOKE " + pgx.Identifier{membership.Granted}.Sanitize() + " FROM " + pgx.Identifier{membership.Member}.Sanitize() + " GRANTED BY " + pgx.Identifier{membership.Grantor}.Sanitize()
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`REVOKE ALL ON ALL TABLES IN SCHEMA health_registry FROM health_user`,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA health_registry FROM health_user`,
		`REVOKE ALL ON ALL FUNCTIONS IN SCHEMA health_registry FROM health_user`,
		`DO $types$ DECLARE x record; BEGIN FOR x IN SELECT t.typname FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='health_registry' AND t.typelem=0 LOOP EXECUTE format('REVOKE ALL ON TYPE %I.%I FROM health_user','health_registry',x.typname); END LOOP; END $types$`,
		`REVOKE ALL ON SCHEMA health_registry FROM health_user`,
	} {
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT owner.rolname,d.defaclobjtype::text FROM pg_default_acl d JOIN pg_roles owner ON owner.oid=d.defaclrole JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE n.nspname='health_registry' AND grantee.rolname='health_user' ORDER BY 1,2`)
	if err != nil {
		return err
	}
	var defaults []DatabaseIdentityDefaultACL
	for rows.Next() {
		var d DatabaseIdentityDefaultACL
		if err = rows.Scan(&d.Owner, &d.ObjectType); err != nil {
			rows.Close()
			return err
		}
		defaults = append(defaults, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, d := range defaults {
		plural, _ := defaultACLClass(d.ObjectType)
		if plural == "" {
			return errors.New("legacy registry default ACL has unsupported object type")
		}
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{d.Owner}.Sanitize()); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "ALTER DEFAULT PRIVILEGES IN SCHEMA health_registry REVOKE ALL ON "+plural+" FROM health_user"); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
	}
	return nil
}

func verifyExactFinalizedState(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	if err := verifyExactBootstrappedState(ctx, tx, manifest); err != nil {
		return err
	}
	var legacyExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='health_user')`).Scan(&legacyExists); err != nil || !legacyExists {
		return err
	}
	var bridge, schemaACL, relationACL, routineACL, typeACL, defaultACL int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE granted.rolname='health_user' AND member.rolname='health_admin'),
		(SELECT count(*) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname='health_user'),
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname='health_user'),
		(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace CROSS JOIN LATERAL aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname='health_user'),
		(SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace CROSS JOIN LATERAL aclexplode(coalesce(t.typacl,acldefault('T',t.typowner))) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND t.typelem=0 AND g.rolname='health_user'),
		(SELECT count(*) FROM pg_default_acl d JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname='health_user')`).Scan(&bridge, &schemaACL, &relationACL, &routineACL, &typeACL, &defaultACL); err != nil {
		return err
	}
	if bridge+schemaACL+relationACL+routineACL+typeACL+defaultACL != 0 {
		return errors.New("database identity finalize left legacy registry authority")
	}
	return nil
}

func currentLegacyBridgeMemberships(ctx context.Context, tx pgx.Tx) ([]DatabaseIdentityMembership, error) {
	rows, err := tx.Query(ctx, `SELECT granted.rolname,member.rolname,grantor.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor WHERE granted.rolname='health_user' AND member.rolname='health_admin' ORDER BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseIdentityMembership
	for rows.Next() {
		var membership DatabaseIdentityMembership
		if err = rows.Scan(&membership.Granted, &membership.Member, &membership.Grantor, &membership.AdminOption, &membership.InheritOption, &membership.SetOption); err != nil {
			return nil, err
		}
		out = append(out, membership)
	}
	return out, rows.Err()
}

func replaceLegacyBridgeWithCanonical(ctx context.Context, tx pgx.Tx) error {
	current, err := currentLegacyBridgeMemberships(ctx, tx)
	if err != nil {
		return err
	}
	for _, membership := range current {
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{membership.Grantor}.Sanitize()); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "REVOKE health_user FROM health_admin GRANTED BY "+pgx.Identifier{membership.Grantor}.Sanitize()); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `GRANT health_user TO health_admin WITH ADMIN FALSE, INHERIT FALSE, SET TRUE`)
	return err
}

func restoreLegacySchemaACL(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	if _, err := tx.Exec(ctx, `REVOKE ALL ON SCHEMA health_registry FROM health_user`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE health_registry`); err != nil {
		return err
	}
	for _, acl := range dedupeEffectiveACL(manifest.RegistrySchemaACL) {
		if acl.Grantee != legacyDatabaseRole {
			continue
		}
		statement := "GRANT " + acl.Privilege + " ON SCHEMA health_registry TO health_user"
		if acl.Grantable {
			statement += " WITH GRANT OPTION"
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `RESET ROLE`); err != nil {
		return err
	}
	actual, err := queryIdentityACL(ctx, tx, `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE n.nspname='health_registry'`)
	if err != nil {
		return err
	}
	var actualLegacy, wantedLegacy []DatabaseIdentityACL
	for _, acl := range actual {
		if acl.Grantee == legacyDatabaseRole {
			actualLegacy = append(actualLegacy, acl)
		}
	}
	for _, acl := range manifest.RegistrySchemaACL {
		if acl.Grantee == legacyDatabaseRole {
			wantedLegacy = append(wantedLegacy, acl)
		}
	}
	if !sameEffectiveACL(actualLegacy, wantedLegacy) {
		return errors.New("database identity finalize did not restore legacy schema privileges")
	}
	return nil
}

func restoreLegacyObjectACL(ctx context.Context, tx pgx.Tx, object DatabaseIdentityCatalogObject) error {
	_, target, err := catalogObjectSQL(ctx, tx, object, "TARGET", "")
	if err != nil {
		return err
	}
	class := objectGrantClass(object.Kind)
	if _, err = tx.Exec(ctx, "REVOKE ALL ON "+class+" "+target+" FROM health_user"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE health_registry`); err != nil {
		return err
	}
	for _, acl := range dedupeEffectiveACL(object.ACL) {
		if acl.Grantee != legacyDatabaseRole {
			continue
		}
		statement := "GRANT " + acl.Privilege + " ON " + class + " " + target + " TO health_user"
		if acl.Grantable {
			statement += " WITH GRANT OPTION"
		}
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `RESET ROLE`); err != nil {
		return err
	}
	actual, err := currentObjectACL(ctx, tx, object)
	if err != nil {
		return err
	}
	var actualLegacy, wantedLegacy []DatabaseIdentityACL
	for _, acl := range actual {
		if acl.Grantee == legacyDatabaseRole {
			actualLegacy = append(actualLegacy, acl)
		}
	}
	for _, acl := range object.ACL {
		if acl.Grantee == legacyDatabaseRole {
			wantedLegacy = append(wantedLegacy, acl)
		}
	}
	if !sameEffectiveACL(actualLegacy, wantedLegacy) {
		return errors.New("database identity finalize did not restore legacy object privileges")
	}
	return nil
}

func secureBootstrapRegistryACLs(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	for _, object := range manifest.CatalogObjects {
		_, target, err := catalogObjectSQL(ctx, tx, object, "TARGET", "")
		if err != nil {
			return err
		}
		grantees, err := currentObjectACLGrantees(ctx, tx, object)
		if err != nil {
			return err
		}
		for _, grantee := range sortedPrincipals(grantees) {
			if grantee == DatabaseRegistryRole {
				continue
			}
			if _, err = tx.Exec(ctx, "REVOKE ALL ON "+objectGrantClass(object.Kind)+" "+target+" FROM "+identityPrincipalSQL(grantee)); err != nil {
				return err
			}
		}
	}
	grantees := map[string]bool{"PUBLIC": true, legacyDatabaseRole: true, DatabaseAdminRole: true}
	for _, acl := range manifest.RegistrySchemaACL {
		grantees[acl.Grantee] = true
	}
	for _, grantee := range sortedPrincipals(grantees) {
		if grantee == DatabaseRegistryRole {
			continue
		}
		if grantee != "PUBLIC" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, grantee).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				continue
			}
		}
		if _, err := tx.Exec(ctx, "REVOKE ALL ON SCHEMA health_registry FROM "+identityPrincipalSQL(grantee)); err != nil {
			return err
		}
	}
	for _, d := range manifest.DefaultACLs {
		if d.Schema != "health_registry" {
			continue
		}
		plural, _ := defaultACLClass(d.ObjectType)
		if plural == "" {
			return errors.New("unsupported schema default ACL during bootstrap")
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{d.Owner}.Sanitize()); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, acl := range d.ACL {
			seen[acl.Grantee] = true
		}
		for _, grantee := range sortedPrincipals(seen) {
			if _, err := tx.Exec(ctx, "ALTER DEFAULT PRIVILEGES IN SCHEMA health_registry REVOKE ALL ON "+plural+" FROM "+identityPrincipalSQL(grantee)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
	}
	return nil
}

// ACL fidelity is defined as normalized effective privileges keyed by
// object/grantee/privilege/grant-option. Grantor is retained as audit evidence
// and reproduced when PostgreSQL permits it safely, but a harmless grantor
// difference never justifies catalog writes or makes an otherwise exact
// effective rollback fail.
func restoreRegistryCatalog(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	var currentSchemaOwner string
	if err := tx.QueryRow(ctx, `SELECT owner.rolname FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname='health_registry'`).Scan(&currentSchemaOwner); err != nil {
		return err
	}
	if currentSchemaOwner != manifest.RegistrySchemaOwner && currentSchemaOwner != DatabaseRegistryRole {
		return errors.New("registry schema ownership drift makes rollback ambiguous")
	}
	if _, err := tx.Exec(ctx, "ALTER SCHEMA health_registry OWNER TO "+pgx.Identifier{manifest.RegistrySchemaOwner}.Sanitize()); err != nil {
		return err
	}
	if err := restoreSchemaACL(ctx, tx, manifest); err != nil {
		return err
	}
	for _, object := range manifest.CatalogObjects {
		currentOwner, err := currentCatalogObjectOwner(ctx, tx, object)
		if err != nil {
			return err
		}
		if currentOwner != object.Owner && currentOwner != DatabaseRegistryRole {
			return errors.New("registry object ownership drift makes rollback ambiguous")
		}
		statement, _, err := catalogObjectSQL(ctx, tx, object, "ALTER_OWNER", object.Owner)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
		if err = restoreObjectACL(ctx, tx, object); err != nil {
			return err
		}
	}
	return restoreDefaultACLs(ctx, tx, manifest)
}

func currentCatalogObjectOwner(ctx context.Context, tx pgx.Tx, object DatabaseIdentityCatalogObject) (string, error) {
	var owner string
	switch object.Kind {
	case "FUNCTION", "PROCEDURE":
		identity := routineIdentityInput(object)
		err := tx.QueryRow(ctx, `SELECT owner.rolname FROM pg_proc p JOIN pg_roles owner ON owner.oid=p.proowner WHERE p.oid=to_regprocedure($1)`, identity).Scan(&owner)
		return owner, err
	case "TYPE", "DOMAIN":
		err := tx.QueryRow(ctx, `SELECT owner.rolname FROM pg_type t JOIN pg_roles owner ON owner.oid=t.typowner WHERE t.oid=to_regtype($1)`, "health_registry."+pgx.Identifier{object.Name}.Sanitize()).Scan(&owner)
		return owner, err
	default:
		err := tx.QueryRow(ctx, `SELECT owner.rolname FROM pg_class c JOIN pg_roles owner ON owner.oid=c.relowner WHERE c.oid=to_regclass($1)`, "health_registry."+pgx.Identifier{object.Name}.Sanitize()).Scan(&owner)
		return owner, err
	}
}

func catalogObjectSQL(ctx context.Context, tx pgx.Tx, object DatabaseIdentityCatalogObject, operation, argument string) (string, string, error) {
	class := objectGrantClass(object.Kind)
	if class == "" {
		return "", "", errors.New("unsupported catalog object kind")
	}
	if object.Kind == "FUNCTION" || object.Kind == "PROCEDURE" {
		var target string
		if err := tx.QueryRow(ctx, `SELECT to_regprocedure($1)::text`, routineIdentityInput(object)).Scan(&target); err != nil {
			return "", "", err
		}
		if operation == "ALTER_OWNER" {
			return "ALTER " + object.Kind + " " + target + " OWNER TO " + pgx.Identifier{argument}.Sanitize(), target, nil
		}
		return "", target, nil
	}
	target := pgx.Identifier{"health_registry", object.Name}.Sanitize()
	if operation == "ALTER_OWNER" {
		alterClass := object.Kind
		if alterClass == "PARTITIONED TABLE" {
			alterClass = "TABLE"
		}
		return "ALTER " + alterClass + " " + target + " OWNER TO " + pgx.Identifier{argument}.Sanitize(), target, nil
	}
	return "", target, nil
}

func routineIdentityInput(object DatabaseIdentityCatalogObject) string {
	return "health_registry." + pgx.Identifier{object.Name}.Sanitize() + "(" + object.IdentityArgs + ")"
}

func objectGrantClass(kind string) string {
	switch kind {
	case "TABLE", "PARTITIONED TABLE", "VIEW", "MATERIALIZED VIEW", "FOREIGN TABLE":
		return "TABLE"
	case "SEQUENCE", "FUNCTION", "PROCEDURE", "TYPE", "DOMAIN":
		return kind
	default:
		return ""
	}
}

func restoreObjectACL(ctx context.Context, tx pgx.Tx, object DatabaseIdentityCatalogObject) error {
	_, target, err := catalogObjectSQL(ctx, tx, object, "TARGET", "")
	if err != nil {
		return err
	}
	class := objectGrantClass(object.Kind)
	grantees, err := currentObjectACLGrantees(ctx, tx, object)
	if err != nil {
		return err
	}
	for _, acl := range object.ACL {
		grantees[acl.Grantee] = true
	}
	for _, grantee := range sortedPrincipals(grantees) {
		if _, err = tx.Exec(ctx, "REVOKE ALL ON "+class+" "+target+" FROM "+identityPrincipalSQL(grantee)); err != nil {
			return err
		}
	}
	if err = replayACLAsOwner(ctx, tx, object.Owner, class, target, object.ACL); err != nil {
		return err
	}
	actual, err := currentObjectACL(ctx, tx, object)
	if err != nil {
		return err
	}
	if !sameEffectiveACL(actual, object.ACL) {
		return errors.New("object ACL replay did not restore normalized effective privileges")
	}
	return nil
}

func currentObjectACL(ctx context.Context, tx pgx.Tx, object DatabaseIdentityCatalogObject) ([]DatabaseIdentityACL, error) {
	switch object.Kind {
	case "FUNCTION", "PROCEDURE":
		return queryIdentityACL(ctx, tx, `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_proc p CROSS JOIN LATERAL aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE p.oid=to_regprocedure($1)`, routineIdentityInput(object))
	case "TYPE", "DOMAIN":
		return queryIdentityACL(ctx, tx, `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_type t CROSS JOIN LATERAL aclexplode(coalesce(t.typacl,acldefault('T',t.typowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE t.oid=to_regtype($1)`, "health_registry."+pgx.Identifier{object.Name}.Sanitize())
	default:
		return queryIdentityACL(ctx, tx, `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_class c CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE c.oid=to_regclass($1)`, "health_registry."+pgx.Identifier{object.Name}.Sanitize())
	}
}

func currentObjectACLGrantees(ctx context.Context, tx pgx.Tx, object DatabaseIdentityCatalogObject) (map[string]bool, error) {
	var rows pgx.Rows
	var err error
	switch object.Kind {
	case "FUNCTION", "PROCEDURE":
		rows, err = tx.Query(ctx, `SELECT DISTINCT coalesce(grantee.rolname,'PUBLIC') FROM pg_proc p CROSS JOIN LATERAL aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE p.oid=to_regprocedure($1)`, routineIdentityInput(object))
	case "TYPE", "DOMAIN":
		rows, err = tx.Query(ctx, `SELECT DISTINCT coalesce(grantee.rolname,'PUBLIC') FROM pg_type t CROSS JOIN LATERAL aclexplode(coalesce(t.typacl,acldefault('T',t.typowner))) a LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE t.oid=to_regtype($1)`, "health_registry."+pgx.Identifier{object.Name}.Sanitize())
	default:
		rows, err = tx.Query(ctx, `SELECT DISTINCT coalesce(grantee.rolname,'PUBLIC') FROM pg_class c CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE c.oid=to_regclass($1)`, "health_registry."+pgx.Identifier{object.Name}.Sanitize())
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{"PUBLIC": true, DatabaseRegistryRole: true, legacyDatabaseRole: true, object.Owner: true}
	for rows.Next() {
		var grantee string
		if err = rows.Scan(&grantee); err != nil {
			return nil, err
		}
		out[grantee] = true
	}
	return out, rows.Err()
}

func restoreSchemaACL(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT coalesce(grantee.rolname,'PUBLIC') FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE n.nspname='health_registry'`)
	if err != nil {
		return err
	}
	grantees := map[string]bool{"PUBLIC": true, DatabaseRegistryRole: true, legacyDatabaseRole: true, manifest.RegistrySchemaOwner: true}
	for rows.Next() {
		var grantee string
		if err = rows.Scan(&grantee); err != nil {
			rows.Close()
			return err
		}
		grantees[grantee] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, acl := range manifest.RegistrySchemaACL {
		grantees[acl.Grantee] = true
	}
	for _, grantee := range sortedPrincipals(grantees) {
		if _, err = tx.Exec(ctx, "REVOKE ALL ON SCHEMA health_registry FROM "+identityPrincipalSQL(grantee)); err != nil {
			return err
		}
	}
	return replayACLAsOwner(ctx, tx, manifest.RegistrySchemaOwner, "SCHEMA", "health_registry", manifest.RegistrySchemaACL)
}

func replayACLAsOwner(ctx context.Context, tx pgx.Tx, owner, class, target string, acl []DatabaseIdentityACL) error {
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{owner}.Sanitize()); err != nil {
		return err
	}
	defer tx.Exec(ctx, "RESET ROLE")
	type effectiveKey struct {
		grantee, privilege string
		grantable          bool
	}
	seen := map[effectiveKey]bool{}
	for _, item := range acl {
		key := effectiveKey{item.Grantee, item.Privilege, item.Grantable}
		if seen[key] {
			continue
		}
		seen[key] = true
		statement := "GRANT " + item.Privilege + " ON " + class + " " + target + " TO " + identityPrincipalSQL(item.Grantee)
		if item.Grantable {
			statement += " WITH GRANT OPTION"
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "RESET ROLE")
	return err
}

func identityPrincipalSQL(value string) string {
	if value == "PUBLIC" {
		return "PUBLIC"
	}
	return pgx.Identifier{value}.Sanitize()
}

func sortedPrincipals(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func restoreDefaultACLs(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	// The workflow only creates health_registry's global FUNCTION/TYPE rows.
	// Captured schema/global rows for all relevant owners are replayed below.
	keys := map[string]DatabaseIdentityDefaultACL{}
	for _, d := range manifest.DefaultACLs {
		keys[d.Owner+"\x00"+d.Schema+"\x00"+d.ObjectType] = d
	}
	for _, objectType := range []string{"f", "T"} {
		key := DatabaseRegistryRole + "\x00\x00" + objectType
		if _, ok := keys[key]; !ok {
			keys[key] = DatabaseIdentityDefaultACL{Owner: DatabaseRegistryRole, ObjectType: objectType, ACL: defaultEffectiveACL(DatabaseRegistryRole, objectType)}
		}
	}
	owners := map[string]bool{DatabaseAdminRole: true, DatabaseRegistryRole: true, legacyDatabaseRole: true}
	for _, d := range manifest.DefaultACLs {
		owners[d.Owner] = true
	}
	ownerNames := make([]string, 0, len(owners))
	for owner := range owners {
		ownerNames = append(ownerNames, owner)
	}
	sort.Strings(ownerNames)
	rows, err := tx.Query(ctx, `
		SELECT owner.rolname,coalesce(n.nspname,''),d.defaclobjtype::text
		FROM pg_default_acl d JOIN pg_roles owner ON owner.oid=d.defaclrole LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace
		WHERE n.nspname='health_registry' OR (d.defaclnamespace=0 AND owner.rolname=ANY($1))
		ORDER BY 1,2,3`, ownerNames)
	if err != nil {
		return err
	}
	var currentKeys []DatabaseIdentityDefaultACL
	for rows.Next() {
		var d DatabaseIdentityDefaultACL
		if err = rows.Scan(&d.Owner, &d.Schema, &d.ObjectType); err != nil {
			rows.Close()
			return err
		}
		currentKeys = append(currentKeys, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	currentACLs := map[string][]DatabaseIdentityACL{}
	for _, d := range currentKeys {
		key := d.Owner + "\x00" + d.Schema + "\x00" + d.ObjectType
		if _, ok := keys[key]; !ok {
			return errors.New("unexpected default ACL key makes rollback ambiguous")
		}
		currentACLs[key], err = queryIdentityACL(ctx, tx, `
			SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable
			FROM pg_default_acl d JOIN pg_roles owner ON owner.oid=d.defaclrole LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace
			CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee
			WHERE owner.rolname=$1 AND coalesce(n.nspname,'')=$2 AND d.defaclobjtype::text=$3`, d.Owner, d.Schema, d.ObjectType)
		if err != nil {
			return err
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		d := keys[key]
		plural, class := defaultACLClass(d.ObjectType)
		if plural == "" {
			return errors.New("unsupported default ACL kind")
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{d.Owner}.Sanitize()); err != nil {
			return err
		}
		prefix := "ALTER DEFAULT PRIVILEGES"
		if d.Schema != "" {
			prefix += " IN SCHEMA " + pgx.Identifier{d.Schema}.Sanitize()
		}
		grantees := map[string]bool{"PUBLIC": true, d.Owner: true, DatabaseRegistryRole: true}
		for _, acl := range d.ACL {
			grantees[acl.Grantee] = true
		}
		for _, acl := range currentACLs[key] {
			grantees[acl.Grantee] = true
		}
		for _, grantee := range sortedPrincipals(grantees) {
			if grantee != "PUBLIC" {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, grantee).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					continue
				}
			}
			if _, err := tx.Exec(ctx, prefix+" REVOKE ALL ON "+plural+" FROM "+identityPrincipalSQL(grantee)); err != nil {
				return err
			}
		}
		for _, acl := range dedupeEffectiveACL(d.ACL) {
			statement := prefix + " GRANT " + acl.Privilege + " ON " + plural + " TO " + identityPrincipalSQL(acl.Grantee)
			if acl.Grantable {
				statement += " WITH GRANT OPTION"
			}
			if _, err := tx.Exec(ctx, statement); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
		actual, err := currentDefaultACL(ctx, tx, d)
		if err != nil {
			return err
		}
		if !sameEffectiveACL(actual, d.ACL) {
			return errors.New("default ACL replay did not restore normalized effective privileges")
		}
		_ = class // class documents validation parity with object ACL classes.
	}
	return nil
}

func currentDefaultACL(ctx context.Context, tx pgx.Tx, d DatabaseIdentityDefaultACL) ([]DatabaseIdentityACL, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_default_acl x JOIN pg_roles owner ON owner.oid=x.defaclrole LEFT JOIN pg_namespace n ON n.oid=x.defaclnamespace WHERE owner.rolname=$1 AND coalesce(n.nspname,'')=$2 AND x.defaclobjtype::text=$3)`, d.Owner, d.Schema, d.ObjectType).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		if d.Schema == "" {
			return systemDefaultEffectiveACL(ctx, tx, d.Owner, d.ObjectType)
		}
		return []DatabaseIdentityACL{}, nil
	}
	return queryIdentityACL(ctx, tx, `
		SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable
		FROM pg_default_acl x JOIN pg_roles owner ON owner.oid=x.defaclrole LEFT JOIN pg_namespace n ON n.oid=x.defaclnamespace
		CROSS JOIN LATERAL aclexplode(x.defaclacl) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee
		WHERE owner.rolname=$1 AND coalesce(n.nspname,'')=$2 AND x.defaclobjtype::text=$3`, d.Owner, d.Schema, d.ObjectType)
}

func systemDefaultEffectiveACL(ctx context.Context, tx pgx.Tx, owner, objectType string) ([]DatabaseIdentityACL, error) {
	return queryIdentityACL(ctx, tx, `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable
		FROM pg_roles owner
		CROSS JOIN LATERAL aclexplode(acldefault($2::"char",owner.oid)) a
		JOIN pg_roles grantor ON grantor.oid=a.grantor
		LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee
		WHERE owner.rolname=$1`, owner, objectType)
}

func sameEffectiveACL(a, b []DatabaseIdentityACL) bool {
	toSet := func(values []DatabaseIdentityACL) map[string]bool {
		out := map[string]bool{}
		for _, acl := range dedupeEffectiveACL(values) {
			out[acl.Grantee+"\x00"+acl.Privilege+"\x00"+fmt.Sprint(acl.Grantable)] = true
		}
		return out
	}
	left, right := toSet(a), toSet(b)
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if !right[key] {
			return false
		}
	}
	return true
}

func defaultACLClass(objectType string) (plural, class string) {
	switch objectType {
	case "r":
		return "TABLES", "TABLE"
	case "S":
		return "SEQUENCES", "SEQUENCE"
	case "f":
		return "FUNCTIONS", "FUNCTION"
	case "T":
		return "TYPES", "TYPE"
	case "n":
		return "SCHEMAS", "SCHEMA"
	default:
		return "", ""
	}
}

func defaultEffectiveACL(owner, objectType string) []DatabaseIdentityACL {
	var privileges []string
	switch objectType {
	case "r":
		// PostgreSQL 17 added MAINTAIN to the relation privilege class. This
		// normalized superset is used for manifest validation/replay; live
		// comparisons use systemDefaultEffectiveACL so PostgreSQL 16 remains exact.
		privileges = []string{"DELETE", "INSERT", "MAINTAIN", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"}
	case "S":
		privileges = []string{"SELECT", "UPDATE", "USAGE"}
	case "f":
		privileges = []string{"EXECUTE"}
	case "T":
		privileges = []string{"USAGE"}
	case "n":
		privileges = []string{"CREATE", "USAGE"}
	}
	out := make([]DatabaseIdentityACL, 0, len(privileges)+1)
	for _, privilege := range privileges {
		out = append(out, DatabaseIdentityACL{Grantor: owner, Grantee: owner, Privilege: privilege})
	}
	if objectType == "f" {
		out = append(out, DatabaseIdentityACL{Grantor: owner, Grantee: "PUBLIC", Privilege: "EXECUTE"})
	}
	if objectType == "T" {
		out = append(out, DatabaseIdentityACL{Grantor: owner, Grantee: "PUBLIC", Privilege: "USAGE"})
	}
	return out
}

func dedupeEffectiveACL(values []DatabaseIdentityACL) []DatabaseIdentityACL {
	seen := map[string]bool{}
	var out []DatabaseIdentityACL
	for _, acl := range values {
		key := strings.Join([]string{acl.Grantee, acl.Privilege, fmt.Sprint(acl.Grantable)}, "\x00")
		if !seen[key] {
			seen[key] = true
			out = append(out, acl)
		}
	}
	return out
}

func restoreFixedIdentityState(ctx context.Context, tx pgx.Tx, manifest DatabaseIdentityManifest) error {
	rows, err := tx.Query(ctx, `
		SELECT granted.rolname,member.rolname,grantor.rolname
		FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor
		WHERE granted.rolname IN ('health_admin','health_registry','health_user') OR member.rolname IN ('health_admin','health_registry')`)
	if err != nil {
		return err
	}
	var current []DatabaseIdentityMembership
	for rows.Next() {
		var x DatabaseIdentityMembership
		if err = rows.Scan(&x.Granted, &x.Member, &x.Grantor); err != nil {
			rows.Close()
			return err
		}
		current = append(current, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, membership := range current {
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{membership.Grantor}.Sanitize()); err != nil {
			return err
		}
		statement := "REVOKE " + pgx.Identifier{membership.Granted}.Sanitize() + " FROM " + pgx.Identifier{membership.Member}.Sanitize() + " GRANTED BY " + pgx.Identifier{membership.Grantor}.Sanitize()
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
	}
	for _, membership := range manifest.Memberships {
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{membership.Grantor}.Sanitize()); err != nil {
			return err
		}
		statement := "GRANT " + pgx.Identifier{membership.Granted}.Sanitize() + " TO " + pgx.Identifier{membership.Member}.Sanitize() +
			fmt.Sprintf(" WITH ADMIN %t, INHERIT %t, SET %t GRANTED BY %s", membership.AdminOption, membership.InheritOption, membership.SetOption, pgx.Identifier{membership.Grantor}.Sanitize())
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "RESET ROLE"); err != nil {
			return err
		}
	}
	actualMemberships, err := currentFixedMemberships(ctx, tx)
	if err != nil {
		return err
	}
	if !sameMemberships(actualMemberships, manifest.Memberships) {
		return errors.New("membership replay did not restore exact grantor and options")
	}

	var database string
	if err = tx.QueryRow(ctx, `SELECT format('%I',current_database())`).Scan(&database); err != nil {
		return err
	}
	for _, role := range []string{DatabaseAdminRole, DatabaseRegistryRole} {
		if _, err = tx.Exec(ctx, "REVOKE ALL ON DATABASE "+database+" FROM "+pgx.Identifier{role}.Sanitize()); err != nil {
			return err
		}
	}
	for _, acl := range dedupeEffectiveACL(manifest.DatabaseGrants) {
		statement := "GRANT " + acl.Privilege + " ON DATABASE " + database + " TO " + pgx.Identifier{acl.Grantee}.Sanitize()
		if acl.Grantable {
			statement += " WITH GRANT OPTION"
		}
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	for _, role := range manifest.FixedRoles {
		if !role.Existed {
			if _, err = tx.Exec(ctx, "ALTER ROLE "+pgx.Identifier{role.Name}.Sanitize()+" NOLOGIN NOCREATEROLE NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT CONNECTION LIMIT 0"); err != nil {
				return err
			}
			var commentSQL string
			if err = tx.QueryRow(ctx, `SELECT format('COMMENT ON ROLE %I IS %L',$1::text,$2::text)`, role.Name, databaseIdentityMarker+":dormant-no-password-restore").Scan(&commentSQL); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, commentSQL); err != nil {
				return err
			}
			continue
		}
		statement := "ALTER ROLE " + pgx.Identifier{role.Name}.Sanitize() + " " + boolRoleToken(role.Login, "LOGIN", "NOLOGIN") + " " + boolRoleToken(role.CreateRole, "CREATEROLE", "NOCREATEROLE") + " " + boolRoleToken(role.Superuser, "SUPERUSER", "NOSUPERUSER") + " " + boolRoleToken(role.CreateDB, "CREATEDB", "NOCREATEDB") + " " + boolRoleToken(role.Replication, "REPLICATION", "NOREPLICATION") + " " + boolRoleToken(role.BypassRLS, "BYPASSRLS", "NOBYPASSRLS") + " " + boolRoleToken(role.Inherit, "INHERIT", "NOINHERIT") + fmt.Sprintf(" CONNECTION LIMIT %d", role.ConnLimit)
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
		if role.ValidUntil == nil {
			if _, err = tx.Exec(ctx, "ALTER ROLE "+pgx.Identifier{role.Name}.Sanitize()+" VALID UNTIL 'infinity'"); err != nil {
				return err
			}
		} else {
			var validSQL string
			if err = tx.QueryRow(ctx, `SELECT format('ALTER ROLE %I VALID UNTIL %L',$1::text,$2::text)`, role.Name, *role.ValidUntil).Scan(&validSQL); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, validSQL); err != nil {
				return err
			}
		}
		var commentSQL string
		if role.Marker == nil {
			commentSQL = "COMMENT ON ROLE " + pgx.Identifier{role.Name}.Sanitize() + " IS NULL"
		} else if err = tx.QueryRow(ctx, `SELECT format('COMMENT ON ROLE %I IS %L',$1::text,$2::text)`, role.Name, *role.Marker).Scan(&commentSQL); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, commentSQL); err != nil {
			return err
		}
	}
	return nil
}

func currentFixedMemberships(ctx context.Context, tx pgx.Tx) ([]DatabaseIdentityMembership, error) {
	rows, err := tx.Query(ctx, `SELECT granted.rolname,member.rolname,grantor.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor WHERE granted.rolname IN ('health_admin','health_registry','health_user') OR member.rolname IN ('health_admin','health_registry') ORDER BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseIdentityMembership
	for rows.Next() {
		var membership DatabaseIdentityMembership
		if err = rows.Scan(&membership.Granted, &membership.Member, &membership.Grantor, &membership.AdminOption, &membership.InheritOption, &membership.SetOption); err != nil {
			return nil, err
		}
		out = append(out, membership)
	}
	return out, rows.Err()
}

func sameMemberships(a, b []DatabaseIdentityMembership) bool {
	key := func(m DatabaseIdentityMembership) string {
		return strings.Join([]string{m.Granted, m.Member, m.Grantor, fmt.Sprint(m.AdminOption), fmt.Sprint(m.InheritOption), fmt.Sprint(m.SetOption)}, "\x00")
	}
	left, right := map[string]bool{}, map[string]bool{}
	for _, membership := range a {
		left[key(membership)] = true
	}
	for _, membership := range b {
		right[key(membership)] = true
	}
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if !right[value] {
			return false
		}
	}
	return true
}

func preflightMembershipReplay(ctx context.Context, tx pgx.Tx, memberships []DatabaseIdentityMembership) error {
	for _, membership := range memberships {
		var rolesExist, grantorCanGrant, sessionCanSet bool
		if err := tx.QueryRow(ctx, `SELECT
			NOT EXISTS(SELECT 1 FROM unnest($1::text[]) wanted(name) WHERE NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=wanted.name)),
			(SELECT r.rolsuper OR r.rolname=$2 OR EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE granted.rolname=$2 AND member.rolname=$3 AND m.admin_option) FROM pg_roles r WHERE r.rolname=$3),
			(SELECT s.rolsuper OR pg_has_role(session_user,$3,'SET') FROM pg_roles s WHERE s.rolname=session_user)`, []string{membership.Granted, membership.Member, membership.Grantor}, membership.Granted, membership.Grantor).Scan(&rolesExist, &grantorCanGrant, &sessionCanSet); err != nil {
			return err
		}
		if !rolesExist || !grantorCanGrant || !sessionCanSet {
			return errors.New("membership grantor cannot be reproduced safely")
		}
	}
	return nil
}

func boolRoleToken(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}
