package tenants

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFixedMembershipAllowlistIsExact(t *testing.T) {
	tenant := "health_t_11111111111141118111111111111111"
	allowed := map[string]bool{tenant: true}
	if !fixedMembershipAllowed(tenant, DatabaseAdminRole, true, false, false, false, allowed) {
		t.Fatal("canonical tenant membership rejected")
	}
	orphan := "health_t_22222222222242228222222222222222"
	if fixedMembershipAllowed(orphan, DatabaseAdminRole, true, false, false, false, allowed) {
		t.Fatal("canonical-looking unregistered tenant membership accepted")
	}
	if fixedMembershipAllowed(legacyDatabaseRole, DatabaseAdminRole, false, false, true, false, allowed) {
		t.Fatal("undeclared legacy bridge accepted")
	}
	if !fixedMembershipAllowed(legacyDatabaseRole, DatabaseAdminRole, false, false, true, true, allowed) {
		t.Fatal("declared legacy bridge rejected")
	}
	for _, x := range []struct {
		g, m    string
		a, i, s bool
	}{{tenant, DatabaseAdminRole, false, false, false}, {tenant, DatabaseAdminRole, true, true, false}, {tenant, DatabaseAdminRole, true, false, true}, {DatabaseAdminRole, "other", true, false, false}, {DatabaseRegistryRole, DatabaseAdminRole, true, false, false}} {
		if fixedMembershipAllowed(x.g, x.m, x.a, x.i, x.s, true, allowed) {
			t.Fatalf("unexpected membership accepted: %+v", x)
		}
	}
}

func TestSecureFixedPoolConfigClearsHooksAndRejectsRoleChangingStartup(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://health_admin:secret@localhost/health")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "attacker"
	cfg.AfterConnect = func(context.Context, *pgx.Conn) error { return nil }
	if err = secureFixedPoolConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AfterConnect != nil {
		t.Fatal("AfterConnect hook survived fixed identity hardening")
	}
	if _, exists := cfg.ConnConfig.RuntimeParams["search_path"]; exists {
		t.Fatal("search_path survived fixed identity hardening")
	}
	for _, key := range []string{"role", "session_authorization", "options"} {
		bad, parseErr := pgxpool.ParseConfig("postgres://health_admin:secret@localhost/health")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		bad.ConnConfig.RuntimeParams[key] = "health_registry"
		if secureFixedPoolConfig(bad) == nil {
			t.Fatalf("startup parameter %s accepted", key)
		}
	}
}

func TestValidRegisteredTenantIdentityIsFailClosed(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	role := TenantRoleName(id)
	if !validRegisteredTenantIdentity("health_valid", &id, &role) {
		t.Fatal("valid registry identity rejected")
	}
	wrongRole := TenantRoleName(uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	for name, x := range map[string]struct {
		schema string
		id     *uuid.UUID
		role   *string
	}{
		"missing tenant id": {"health_valid", nil, &role},
		"missing role":      {"health_valid", &id, nil},
		"wrong role":        {"health_valid", &id, &wrongRole},
		"invalid schema":    {"not-valid!", &id, &role},
	} {
		if validRegisteredTenantIdentity(x.schema, x.id, x.role) {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestNormalizeFixedIdentityResultStableAndFailClosed(t *testing.T) {
	r := normalizeFixedIdentityResult(FixedIdentityResult{Findings: []FixedIdentityFinding{{"z", "role"}, {"a", "acl"}}})
	if r.Status != AuditStatusFail || r.Findings[0].Code != "a" {
		t.Fatalf("result=%+v", r)
	}
	if got := normalizeFixedIdentityResult(FixedIdentityResult{}); got.Status != AuditStatusPass || got.Findings == nil {
		t.Fatalf("empty result=%+v", got)
	}
}
