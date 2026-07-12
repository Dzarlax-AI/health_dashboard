package tenants

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTenantCredentialDerivationIsStableAndSeparated(t *testing.T) {
	d := CredentialDeriver{Current: SecretVersion{Version: 3, Secret: []byte("test-master-secret-with-32-bytes-min")}}
	idA := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	idB := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	a1, err := d.Derive(idA, "health_t_11111111111111111111", 3)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := d.Derive(idA, "health_t_11111111111111111111", 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Derive(idB, "health_t_22222222222222222222", 3)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 || a1 == b {
		t.Fatalf("unstable or shared credential")
	}
	if strings.ContainsAny(a1, ":/@ ") {
		t.Fatalf("DSN-unsafe credential %q", a1)
	}
}

func TestTenantRoleNameUsesImmutableUUID(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	if got, want := TenantRoleName(id), "health_t_11111111111141118111111111111111"; got != want {
		t.Fatalf("TenantRoleName() = %q, want %q", got, want)
	}
}

func TestTenantRoleNameDoesNotCollideAfterFormerPrefix(t *testing.T) {
	a := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	b := uuid.MustParse("11111111-1111-4111-8111-222222222222")
	if TenantRoleName(a) == TenantRoleName(b) {
		t.Fatalf("distinct UUIDs produced the same role %q", TenantRoleName(a))
	}
}

func TestTenantCredentialDerivationBindsVersion(t *testing.T) {
	secret := []byte("same-master-secret-at-least-32-bytes")
	d := CredentialDeriver{
		Current:  SecretVersion{Version: 4, Secret: secret},
		Previous: &SecretVersion{Version: 3, Secret: secret},
	}
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	role := TenantRoleName(id)
	current, err := d.Derive(id, role, 4)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := d.Derive(id, role, 3)
	if err != nil {
		t.Fatal(err)
	}
	if current == previous {
		t.Fatal("credential version was not bound into derivation")
	}
}

func TestCredentialDeriverValidation(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	valid := SecretVersion{Version: 3, Secret: []byte("test-master-secret-with-32-bytes-min")}
	tests := []struct {
		name    string
		deriver CredentialDeriver
		role    string
		version int
	}{
		{"short secret", CredentialDeriver{Current: SecretVersion{Version: 3, Secret: []byte("short")}}, "health_t_11111111111141118111", 3},
		{"zero version", CredentialDeriver{Current: SecretVersion{Secret: valid.Secret}}, "health_t_11111111111141118111", 0},
		{"duplicate versions", CredentialDeriver{Current: valid, Previous: &SecretVersion{Version: 3, Secret: valid.Secret}}, "health_t_11111111111141118111", 3},
		{"invalid role", CredentialDeriver{Current: valid}, "health_t_BAD", 3},
		{"unknown version", CredentialDeriver{Current: valid}, "health_t_11111111111141118111", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.deriver.Derive(id, tt.role, tt.version); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseTenantIsolationConfig(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString([]byte("current-master-secret-at-least-32-bytes"))
	previous := base64.RawURLEncoding.EncodeToString([]byte("previous-master-secret-at-least-32-bytes"))
	env := map[string]string{
		"TENANT_DB_ISOLATION_ENABLED":              "true",
		"ADMIN_DATABASE_URL":                       "postgres://admin@example/db",
		"REGISTRY_DATABASE_URL":                    "postgres://registry@example/db",
		"TENANT_DATABASE_URL_BASE":                 "postgres://example/db",
		"TENANT_DB_MASTER_SECRET":                  secret,
		"TENANT_DB_MASTER_SECRET_VERSION":          "4",
		"TENANT_DB_PREVIOUS_MASTER_SECRET":         previous,
		"TENANT_DB_PREVIOUS_MASTER_SECRET_VERSION": "3",
	}
	cfg, err := ParseTenantIsolationConfig(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.AdminDSN == "" || cfg.RegistryDSN == "" || cfg.TenantDSNBase == "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Credentials.Current.Version != 4 || cfg.Credentials.Previous == nil || cfg.Credentials.Previous.Version != 3 {
		t.Fatalf("unexpected credential versions: %+v", cfg.Credentials)
	}
}

func TestParseTenantIsolationConfigDisabledPreservesLegacyStartup(t *testing.T) {
	cfg, err := ParseTenantIsolationConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatal("isolation unexpectedly enabled")
	}
}

func TestParseTenantIsolationConfigRejectsInvalidEnabledConfig(t *testing.T) {
	validSecret := base64.RawURLEncoding.EncodeToString([]byte("current-master-secret-at-least-32-bytes"))
	shortSecret := base64.RawURLEncoding.EncodeToString([]byte("too-short"))
	valid := map[string]string{
		"TENANT_DB_ISOLATION_ENABLED":     "true",
		"ADMIN_DATABASE_URL":              "postgres://admin@example/db",
		"REGISTRY_DATABASE_URL":           "postgres://registry@example/db",
		"TENANT_DATABASE_URL_BASE":        "postgres://example/db",
		"TENANT_DB_MASTER_SECRET":         validSecret,
		"TENANT_DB_MASTER_SECRET_VERSION": "4",
	}
	tests := []struct {
		name   string
		change map[string]string
	}{
		{"invalid enabled flag", map[string]string{"TENANT_DB_ISOLATION_ENABLED": "maybe"}},
		{"missing enabled values", map[string]string{"ADMIN_DATABASE_URL": ""}},
		{"invalid base64", map[string]string{"TENANT_DB_MASTER_SECRET": "not-base64"}},
		{"decoded secret shorter than 32 bytes", map[string]string{"TENANT_DB_MASTER_SECRET": shortSecret}},
		{"zero current version", map[string]string{"TENANT_DB_MASTER_SECRET_VERSION": "0"}},
		{"negative current version", map[string]string{"TENANT_DB_MASTER_SECRET_VERSION": "-1"}},
		{"previous secret without version", map[string]string{"TENANT_DB_PREVIOUS_MASTER_SECRET": validSecret}},
		{"previous version without secret", map[string]string{"TENANT_DB_PREVIOUS_MASTER_SECRET_VERSION": "3"}},
		{"duplicate versions", map[string]string{"TENANT_DB_PREVIOUS_MASTER_SECRET": validSecret, "TENANT_DB_PREVIOUS_MASTER_SECRET_VERSION": "4"}},
		{"tenant base URL credentials", map[string]string{"TENANT_DATABASE_URL_BASE": "postgres://shared@db.example/aistack"}},
		{"tenant base keyword credentials", map[string]string{"TENANT_DATABASE_URL_BASE": "host=db.example dbname=aistack password=shared"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := mapsClone(valid)
			for key, value := range tt.change {
				if value == "" {
					delete(env, key)
				} else {
					env[key] = value
				}
			}
			if _, err := ParseTenantIsolationConfig(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err == nil {
				t.Fatalf("expected error for env %#v", env)
			}
		})
	}
}

func TestTenantDatabaseURLBaseValidation(t *testing.T) {
	t.Setenv("PGUSER", "ambient-user-must-not-make-base-explicit")
	t.Setenv("PGPASSWORD", "ambient-password-must-not-make-base-explicit")
	t.Setenv("PGSERVICE", "ambient-service-must-not-affect-base")
	t.Setenv("PGSERVICEFILE", "/nonexistent/ambient-service-file")
	t.Setenv("PGSSLCERT", "/nonexistent/ambient-client.crt")
	t.Setenv("PGSSLKEY", "/nonexistent/ambient-client.key")
	tests := []struct {
		name    string
		base    string
		wantErr bool
	}{
		{"URL base", "postgres://db.example/aistack?sslmode=require", false},
		{"postgresql URL base", "postgresql://db.example/aistack", false},
		{"keyword base", "host=db.example port=5432 dbname=aistack sslmode=require", false},
		{"URL username", "postgres://health_tenant@db.example/aistack", true},
		{"URL username and password", "postgres://health_tenant:secret@db.example/aistack", true},
		{"URL query user", "postgres://db.example/aistack?user=health_tenant", true},
		{"URL query password", "postgres://db.example/aistack?password=secret", true},
		{"URL encoded credential value", "postgres://db.example/aistack?password=%73ecret", true},
		{"URL encoded credential key", "postgres://db.example/aistack?%75ser=shared", true},
		{"URL encoded userinfo", "postgres://sh%61red@db.example/aistack", true},
		{"URL mixed-case scheme", "Postgres://db.example/aistack", true},
		{"URL mixed-case credential key", "postgres://db.example/aistack?UsEr=shared", true},
		{"URL mixed-case safe key", "postgres://db.example/aistack?SSLMODE=require", true},
		{"URL query passfile", "postgres://db.example/aistack?passfile=/run/secrets/pgpass", true},
		{"URL query TLS credential", "postgres://db.example/aistack?sslkey=/run/secrets/client.key", true},
		{"URL duplicate parameter", "postgres://db.example/aistack?sslmode=require&sslmode=verify-full", true},
		{"URL case-insensitive duplicate parameter", "postgres://db.example/aistack?sslmode=require&SSLMODE=verify-full", true},
		{"URL fragment", "postgres://db.example/aistack#fragment", true},
		{"URL malformed query escape", "postgres://db.example/aistack?sslmode=%zz", true},
		{"URL empty query key", "postgres://db.example/aistack?=value", true},
		{"keyword user", "host=db.example dbname=aistack user=health_tenant", true},
		{"keyword mixed-case credential key", "host=db.example dbname=aistack UsEr=health_tenant", true},
		{"keyword mixed-case safe key", "Host=db.example dbname=aistack", true},
		{"keyword password", "host=db.example dbname=aistack password=secret", true},
		{"keyword duplicate", "host=db.example dbname=aistack host=other.example", true},
		{"keyword escaped whitespace and equals", `host=db\ example dbname=ai\=stack`, false},
		{"keyword quoted adjacent token", `host='db.example'junk dbname=aistack`, true},
		{"keyword empty key", `=db.example dbname=aistack`, true},
		{"non-ASCII whitespace", "host=db.example\u00a0dbname=aistack", true},
		{"Unicode lookalike", "h\u043est=db.example dbname=aistack", true},
		{"URL missing host", "postgres:///aistack", true},
		{"URL missing database", "postgres://db.example", true},
		{"keyword missing host", "dbname=aistack sslmode=require", true},
		{"keyword missing database", "host=db.example sslmode=require", true},
		{"malformed URL", "postgres://%zz", true},
		{"malformed keyword", "host='unterminated", true},
		{"unsupported URL scheme", "mysql://db.example/aistack", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTenantDSNBase(tt.base)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTenantDSNBase(%q) error = %v, wantErr %v", tt.base, err, tt.wantErr)
			}
		})
	}
}

func mapsClone(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
