package registry

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type SchemaContractQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

var registryContractColumns = map[string][]string{
	"users": {
		"username:text:not-null", "schema_name:text:not-null", "api_key:text:not-null", "password_hash:text:not-null", "email:text:nullable", "is_admin:boolean:not-null", "created_at:timestamp with time zone:not-null", "tenant_id:uuid:not-null", "db_role:text:not-null", "db_credential_version:integer:not-null", "db_isolation_ready:boolean:not-null", "schema_contract_version:integer:nullable", "schema_contract_checksum:text:nullable", "provisioning_state:text:not-null",
	},
	"global_settings":                {"key:text:not-null", "value:text:not-null", "updated_at:timestamp with time zone:not-null"},
	"sessions":                       {"id_hash:text:not-null", "username:text:not-null", "created_at:timestamp with time zone:not-null", "expires_at:timestamp with time zone:not-null", "last_seen_at:timestamp with time zone:nullable"},
	"tenant_provisioning_operations": {"operation_id:uuid:not-null", "tenant_id:uuid:not-null", "username:text:not-null", "schema_name:text:not-null", "db_role:text:not-null", "credential_version:integer:not-null", "state:text:not-null", "error:text:nullable", "created_at:timestamp with time zone:not-null", "updated_at:timestamp with time zone:not-null"},
}

var registryContractConstraints = map[string][]string{
	"users": {
		"registry_users_provisioning_state_check:CHECK (provisioning_state = ANY (ARRAY['pending'::text, 'provisioning'::text, 'active'::text, 'failed'::text]))",
		"users_api_key_key:UNIQUE (api_key)", "users_email_key:UNIQUE (email)", "users_pkey:PRIMARY KEY (username)", "users_schema_name_key:UNIQUE (schema_name)",
	},
	"global_settings": {"global_settings_pkey:PRIMARY KEY (key)"},
	"sessions": {
		"sessions_pkey:PRIMARY KEY (id_hash)",
		"sessions_username_fkey:FOREIGN KEY (username) REFERENCES health_registry.users(username) ON DELETE CASCADE",
	},
	"tenant_provisioning_operations": {
		"tenant_provisioning_operations_pkey:PRIMARY KEY (operation_id)",
		"tenant_provisioning_operations_state_check:CHECK (state = ANY (ARRAY['pending'::text, 'provisioning'::text, 'active'::text, 'failed'::text]))",
	},
}

var registryContractIndexes = map[string][]string{
	"users": {
		"idx_registry_users_db_role:true:CREATE UNIQUE INDEX idx_registry_users_db_role ON health_registry.users USING btree (db_role)",
		"idx_registry_users_provisioning_state:false:CREATE INDEX idx_registry_users_provisioning_state ON health_registry.users USING btree (provisioning_state)",
		"idx_registry_users_tenant_id:true:CREATE UNIQUE INDEX idx_registry_users_tenant_id ON health_registry.users USING btree (tenant_id)",
		"users_api_key_key:true:CREATE UNIQUE INDEX users_api_key_key ON health_registry.users USING btree (api_key)",
		"users_email_key:true:CREATE UNIQUE INDEX users_email_key ON health_registry.users USING btree (email)",
		"users_pkey:true:CREATE UNIQUE INDEX users_pkey ON health_registry.users USING btree (username)",
		"users_schema_name_key:true:CREATE UNIQUE INDEX users_schema_name_key ON health_registry.users USING btree (schema_name)",
	},
	"global_settings": {"global_settings_pkey:true:CREATE UNIQUE INDEX global_settings_pkey ON health_registry.global_settings USING btree (key)"},
	"sessions": {
		"idx_registry_sessions_expires:false:CREATE INDEX idx_registry_sessions_expires ON health_registry.sessions USING btree (expires_at)",
		"idx_registry_sessions_user:false:CREATE INDEX idx_registry_sessions_user ON health_registry.sessions USING btree (username)",
		"sessions_pkey:true:CREATE UNIQUE INDEX sessions_pkey ON health_registry.sessions USING btree (id_hash)",
	},
	"tenant_provisioning_operations": {
		"idx_registry_provisioning_state:false:CREATE INDEX idx_registry_provisioning_state ON health_registry.tenant_provisioning_operations USING btree (state, updated_at)",
		"idx_registry_provisioning_tenant:false:CREATE INDEX idx_registry_provisioning_tenant ON health_registry.tenant_provisioning_operations USING btree (tenant_id, updated_at DESC)",
		"tenant_provisioning_operations_pkey:true:CREATE UNIQUE INDEX tenant_provisioning_operations_pkey ON health_registry.tenant_provisioning_operations USING btree (operation_id)",
	},
}

func IsSchemaContractRelation(kind, name string) bool {
	_, ok := registryContractColumns[name]
	return kind == "TABLE" && ok
}

// VerifySchemaContract is the read-only authoritative shape check for the
// registry relations created and migrated by EnsureSchema.
func VerifySchemaContract(ctx context.Context, query SchemaContractQuerier) error {
	for table, wantColumns := range registryContractColumns {
		columns, err := collectSchemaContractRows(ctx, query, `SELECT a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||CASE WHEN a.attnotnull THEN 'not-null' ELSE 'nullable' END FROM pg_attribute a WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, "health_registry."+table)
		if err != nil || !equalStrings(columns, wantColumns) {
			return fmt.Errorf("registry schema contract columns drift for %s", table)
		}
		constraints, err := collectSchemaContractRows(ctx, query, `SELECT conname||':'||pg_get_constraintdef(oid,true) FROM pg_constraint WHERE conrelid=to_regclass($1) ORDER BY conname`, "health_registry."+table)
		if err != nil || !equalSortedStrings(constraints, registryContractConstraints[table]) {
			return fmt.Errorf("registry schema contract constraints drift for %s: got=%v", table, constraints)
		}
		indexes, err := collectSchemaContractRows(ctx, query, `SELECT idx.relname||':'||i.indisunique::text||':'||pg_get_indexdef(i.indexrelid) FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid WHERE i.indrelid=to_regclass($1) ORDER BY idx.relname`, "health_registry."+table)
		if err != nil || !equalSortedStrings(indexes, registryContractIndexes[table]) {
			return fmt.Errorf("registry schema contract indexes drift for %s", table)
		}
	}
	return nil
}

func collectSchemaContractRows(ctx context.Context, query SchemaContractQuerier, sql string, args ...any) ([]string, error) {
	rows, err := query.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalSortedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return equalStrings(a, b)
}
