package tenants

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type databaseIdentityQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func snapshotRegistryCatalog(ctx context.Context, pool databaseIdentityQuerier, m *DatabaseIdentityManifest) error {
	if err := pool.QueryRow(ctx, `SELECT owner.rolname,n.nspacl IS NULL FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname='health_registry'`).Scan(&m.RegistrySchemaOwner, &m.RegistrySchemaACLNull); err != nil {
		return err
	}
	acl, err := queryIdentityACL(ctx, pool, `
		SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable
		FROM pg_namespace n
		CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a
		JOIN pg_roles grantor ON grantor.oid=a.grantor
		LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee
		WHERE n.nspname='health_registry'`)
	if err != nil {
		return err
	}
	m.RegistrySchemaACL = acl

	type objectRef struct {
		oid    uint32
		object DatabaseIdentityCatalogObject
		aclSQL string
	}
	var objects []objectRef
	rows, err := pool.Query(ctx, `
		SELECT c.oid,
		 CASE c.relkind WHEN 'r' THEN 'TABLE' WHEN 'p' THEN 'PARTITIONED TABLE'
		 WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW'
		 WHEN 'S' THEN 'SEQUENCE' WHEN 'f' THEN 'FOREIGN TABLE' END,
		 c.relname,owner.rolname,c.relacl IS NULL
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles owner ON owner.oid=c.relowner
		WHERE n.nspname='health_registry' AND c.relkind IN ('r','p','v','m','S','f')
		ORDER BY c.relname`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x objectRef
		if err = rows.Scan(&x.oid, &x.object.Kind, &x.object.Name, &x.object.Owner, &x.object.ACLWasNull); err != nil {
			rows.Close()
			return err
		}
		x.aclSQL = `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_class c CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE c.oid=$1`
		objects = append(objects, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	routineRows, err := pool.Query(ctx, `
		SELECT p.oid,CASE p.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,p.proname,
		 pg_get_function_identity_arguments(p.oid),owner.rolname,p.proacl IS NULL,p.prosecdef,coalesce(p.proconfig,'{}'::text[])
		FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles owner ON owner.oid=p.proowner
		WHERE n.nspname='health_registry' AND p.prokind IN ('f','p') ORDER BY p.proname,p.oid`)
	if err != nil {
		return err
	}
	for routineRows.Next() {
		var x objectRef
		if err = routineRows.Scan(&x.oid, &x.object.Kind, &x.object.Name, &x.object.IdentityArgs, &x.object.Owner, &x.object.ACLWasNull, &x.object.SecurityDefiner, &x.object.RoleConfig); err != nil {
			routineRows.Close()
			return err
		}
		x.aclSQL = `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_proc p CROSS JOIN LATERAL aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE p.oid=$1`
		objects = append(objects, x)
	}
	if err = routineRows.Err(); err != nil {
		routineRows.Close()
		return err
	}
	routineRows.Close()

	typeRows, err := pool.Query(ctx, `
		SELECT t.oid,CASE WHEN t.typtype='d' THEN 'DOMAIN' ELSE 'TYPE' END,t.typname,owner.rolname,t.typacl IS NULL
		FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace JOIN pg_roles owner ON owner.oid=t.typowner
		WHERE n.nspname='health_registry' AND t.typrelid=0 AND t.typelem=0 AND t.typtype IN ('b','d','e','r','m')
		ORDER BY t.typname`)
	if err != nil {
		return err
	}
	for typeRows.Next() {
		var x objectRef
		if err = typeRows.Scan(&x.oid, &x.object.Kind, &x.object.Name, &x.object.Owner, &x.object.ACLWasNull); err != nil {
			typeRows.Close()
			return err
		}
		x.aclSQL = `SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable FROM pg_type t CROSS JOIN LATERAL aclexplode(coalesce(t.typacl,acldefault('T',t.typowner))) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE t.oid=$1`
		objects = append(objects, x)
	}
	if err = typeRows.Err(); err != nil {
		typeRows.Close()
		return err
	}
	typeRows.Close()

	for _, ref := range objects {
		ref.object.ACL, err = queryIdentityACL(ctx, pool, ref.aclSQL, ref.oid)
		if err != nil {
			return err
		}
		m.CatalogObjects = append(m.CatalogObjects, ref.object)
	}
	if err = snapshotUnsupportedRegistryObjects(ctx, pool, m); err != nil {
		return err
	}
	return snapshotDefaultACLs(ctx, pool, m)
}

func snapshotUnsupportedRegistryObjects(ctx context.Context, pool databaseIdentityQuerier, m *DatabaseIdentityManifest) error {
	rows, err := pool.Query(ctx, `
		SELECT kind||':'||name FROM (
		 SELECT 'relation('||c.relkind::text||')' kind,c.relname name FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='health_registry' AND c.relkind NOT IN ('r','p','v','m','S','f','i','I','c','t')
		 UNION ALL SELECT 'routine('||p.prokind::text||')',p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='health_registry' AND p.prokind NOT IN ('f','p')
		 UNION ALL SELECT 'collation',c.collname FROM pg_collation c JOIN pg_namespace n ON n.oid=c.collnamespace WHERE n.nspname='health_registry'
		 UNION ALL SELECT 'conversion',c.conname FROM pg_conversion c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='health_registry'
		 UNION ALL SELECT 'operator',o.oprname FROM pg_operator o JOIN pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname='health_registry'
		 UNION ALL SELECT 'operator_class',o.opcname FROM pg_opclass o JOIN pg_namespace n ON n.oid=o.opcnamespace WHERE n.nspname='health_registry'
		 UNION ALL SELECT 'operator_family',o.opfname FROM pg_opfamily o JOIN pg_namespace n ON n.oid=o.opfnamespace WHERE n.nspname='health_registry'
		) x(kind,name) ORDER BY 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return err
		}
		m.UnsupportedObjects = append(m.UnsupportedObjects, value)
	}
	return rows.Err()
}

func snapshotDefaultACLs(ctx context.Context, pool databaseIdentityQuerier, m *DatabaseIdentityManifest) error {
	owners := map[string]bool{legacyDatabaseRole: true, DatabaseAdminRole: true, DatabaseRegistryRole: true, m.RegistrySchemaOwner: true}
	for _, object := range m.CatalogObjects {
		owners[object.Owner] = true
	}
	ownerNames := make([]string, 0, len(owners))
	for owner := range owners {
		if owner != "" {
			ownerNames = append(ownerNames, owner)
		}
	}
	rows, err := pool.Query(ctx, `
		SELECT owner.rolname,coalesce(n.nspname,''),d.defaclobjtype::text
		FROM pg_default_acl d JOIN pg_roles owner ON owner.oid=d.defaclrole LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace
		WHERE n.nspname='health_registry' OR (d.defaclnamespace=0 AND owner.rolname=ANY($1))
		ORDER BY 1,2,3`, ownerNames)
	if err != nil {
		return err
	}
	var defaults []DatabaseIdentityDefaultACL
	for rows.Next() {
		var d DatabaseIdentityDefaultACL
		if err = rows.Scan(&d.Owner, &d.Schema, &d.ObjectType); err != nil {
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
		d.ACL, err = queryIdentityACL(ctx, pool, `
			SELECT grantor.rolname,coalesce(grantee.rolname,'PUBLIC'),a.privilege_type,a.is_grantable
			FROM pg_default_acl d JOIN pg_roles owner ON owner.oid=d.defaclrole LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace
			CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles grantor ON grantor.oid=a.grantor LEFT JOIN pg_roles grantee ON grantee.oid=a.grantee
			WHERE owner.rolname=$1 AND coalesce(n.nspname,'')=$2 AND d.defaclobjtype::text=$3`, d.Owner, d.Schema, d.ObjectType)
		if err != nil {
			return err
		}
		m.DefaultACLs = append(m.DefaultACLs, d)
	}
	return nil
}

func snapshotFixedIdentityState(ctx context.Context, pool databaseIdentityQuerier, m *DatabaseIdentityManifest) error {
	for _, name := range []string{DatabaseAdminRole, DatabaseRegistryRole} {
		r := DatabaseIdentityRoleState{Name: name, Config: []string{}}
		var validUntil pgtype.Timestamptz
		err := pool.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls,rolinherit,rolconnlimit,rolvaliduntil,coalesce(rolconfig,'{}'::text[]),shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, name).Scan(&r.Login, &r.Superuser, &r.CreateRole, &r.CreateDB, &r.Replication, &r.BypassRLS, &r.Inherit, &r.ConnLimit, &validUntil, &r.Config, &r.Marker)
		if err == nil {
			r.Existed = true
			r.ValidUntil, err = databaseIdentityValidUntil(validUntil)
			if err != nil {
				return err
			}
		} else if err != pgx.ErrNoRows {
			return err
		}
		m.FixedRoles = append(m.FixedRoles, r)
	}
	rows, err := pool.Query(ctx, `
		SELECT granted.rolname,member.rolname,grantor.rolname,m.admin_option,m.inherit_option,m.set_option
		FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor
		WHERE granted.rolname IN ('health_admin','health_registry','health_user') OR member.rolname IN ('health_admin','health_registry')
		ORDER BY 1,2,3`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x DatabaseIdentityMembership
		if err = rows.Scan(&x.Granted, &x.Member, &x.Grantor, &x.AdminOption, &x.InheritOption, &x.SetOption); err != nil {
			rows.Close()
			return err
		}
		m.Memberships = append(m.Memberships, x)
		if x.Granted == legacyDatabaseRole && x.Member == DatabaseAdminRole {
			m.LegacyBridgeExisted = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	m.DatabaseGrants, err = queryIdentityACL(ctx, pool, `
		SELECT grantor.rolname,grantee.rolname,a.privilege_type,a.is_grantable
		FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a
		JOIN pg_roles grantor ON grantor.oid=a.grantor JOIN pg_roles grantee ON grantee.oid=a.grantee
		WHERE d.datname=current_database() AND grantee.rolname IN ('health_admin','health_registry')`)
	return err
}

func databaseIdentityValidUntil(value pgtype.Timestamptz) (*string, error) {
	if !value.Valid || value.InfinityModifier == pgtype.Infinity {
		return nil, nil
	}
	if value.InfinityModifier != pgtype.Finite {
		return nil, errors.New("role valid-until cannot be negative infinity")
	}
	formatted := value.Time.UTC().Format(time.RFC3339Nano)
	return &formatted, nil
}

func queryIdentityACL(ctx context.Context, pool databaseIdentityQuerier, sql string, args ...any) ([]DatabaseIdentityACL, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseIdentityACL
	for rows.Next() {
		var a DatabaseIdentityACL
		if err = rows.Scan(&a.Grantor, &a.Grantee, &a.Privilege, &a.Grantable); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Grantee+"\x00"+a.Privilege+"\x00"+a.Grantor < b.Grantee+"\x00"+b.Privilege+"\x00"+b.Grantor
	})
	return out, nil
}
