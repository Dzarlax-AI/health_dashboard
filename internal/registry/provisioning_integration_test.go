package registry

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestActivateProvisionedAdvancesContractMetadataWithCAS(t *testing.T) {
	r, ctx := newEmptyTestRegistry(t)
	u, op, err := r.ReserveUser(ctx, CreateUserReq{Username: "contract", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AdvanceProvisioning(ctx, op.OperationID, ProvisioningStatePending, ProvisioningStateProvisioning, ""); err != nil {
		t.Fatal(err)
	}
	contract := SchemaContractMetadata{Version: 1, Checksum: strings.Repeat("a", 64)}
	if _, err := r.pool.Exec(ctx, `UPDATE health_registry.users SET schema_contract_version=99,schema_contract_checksum=$2 WHERE username=$1`, u.Username, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	op.State = ProvisioningStateProvisioning
	if err := r.ActivateProvisioned(ctx, op, contract); !errors.Is(err, ErrProvisioningStateConflict) {
		t.Fatalf("activation with stale contract metadata error = %v", err)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE health_registry.users SET schema_contract_version=NULL,schema_contract_checksum=NULL WHERE username=$1`, u.Username); err != nil {
		t.Fatal(err)
	}
	if err := r.ActivateProvisioned(ctx, op, contract); err != nil {
		t.Fatalf("activate provisioned contract: %v", err)
	}
	active, err := r.GetByUsername(ctx, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	if active.SchemaContractVersion != contract.Version || active.SchemaContractChecksum != contract.Checksum || !active.DBIsolationReady {
		t.Fatalf("active contract metadata = %+v", active)
	}
}

func TestInactiveTenantIsRejectedByAuthLookups(t *testing.T) {
	r, ctx := newEmptyTestRegistry(t)
	var constraintCount int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conname = 'registry_users_provisioning_state_check'
		  AND conrelid = 'health_registry.users'::regclass
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("query users provisioning-state constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("users provisioning-state constraints = %d, want 1 on health_registry.users", constraintCount)
	}
	user, err := r.CreateLegacyUser(ctx, CreateUserReq{
		Username: "alpha",
		Password: "secret",
		Email:    "alpha@example.test",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := r.EnsureSchema(ctx); err != nil {
			t.Fatalf("idempotent EnsureSchema #%d: %v", i+1, err)
		}
	}
	afterMigration, err := r.GetByUsername(ctx, user.Username)
	if err != nil {
		t.Fatalf("GetByUsername after repeated migration: %v", err)
	}
	if afterMigration.TenantID != user.TenantID || afterMigration.DBRole != user.DBRole || afterMigration.DBCredentialVersion != user.DBCredentialVersion {
		t.Fatalf("repeated migration changed immutable metadata: before=%#v after=%#v", user, afterMigration)
	}
	if afterMigration.SchemaContractVersion != 0 || afterMigration.SchemaContractChecksum != "" {
		t.Fatalf("legacy active user was silently marked current: %#v", afterMigration)
	}
	token, err := r.CreateSession(ctx, user.Username, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	for _, state := range []ProvisioningState{ProvisioningStatePending, ProvisioningStateFailed} {
		if _, err := r.pool.Exec(ctx, `UPDATE health_registry.users SET provisioning_state=$2 WHERE username=$1`, user.Username, state); err != nil {
			t.Fatalf("set state %q: %v", state, err)
		}
		lookups := []struct {
			name string
			call func() (*User, error)
		}{
			{"username", func() (*User, error) { return r.GetByUsername(ctx, user.Username) }},
			{"api key", func() (*User, error) { return r.GetByAPIKey(ctx, user.APIKey) }},
			{"email", func() (*User, error) { return r.GetByEmail(ctx, user.Email) }},
			{"schema", func() (*User, error) { return r.GetBySchema(ctx, user.SchemaName) }},
		}
		for _, lookup := range lookups {
			if _, err := lookup.call(); !errors.Is(err, ErrUserNotFound) {
				t.Errorf("%s lookup in state %q returned %v, want ErrUserNotFound", lookup.name, state, err)
			}
		}
		if _, err := r.GetSessionUser(ctx, token); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("session lookup in state %q returned %v, want ErrSessionNotFound", state, err)
		}
		if _, err := r.CreateSession(ctx, user.Username, time.Hour); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("session creation in state %q returned %v, want ErrUserNotFound", state, err)
		}
	}

	users, err := r.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].ProvisioningState != ProvisioningStateFailed {
		t.Fatalf("administrative ListUsers = %#v, %v", users, err)
	}
	active, err := r.ListActiveUsers(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("ListActiveUsers = %#v, %v", active, err)
	}
	if r.IsEmpty(ctx) {
		t.Fatal("registry with a failed reservation must keep setup closed")
	}
	if r.HasActiveUsers(ctx) {
		t.Fatal("registry with only failed users must have no active users")
	}
	if _, _, err := r.ReserveFirstUser(ctx, CreateUserReq{Username: "bravo", Password: "secret"}); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("pending/failed reservation must keep setup closed, got %v", err)
	}

	if _, err := r.pool.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='active', db_credential_version=0 WHERE username=$1`, user.Username); err != nil {
		t.Fatalf("set invalid active metadata: %v", err)
	}
	if _, err := r.GetByUsername(ctx, user.Username); err == nil || errors.Is(err, ErrUserNotFound) {
		t.Fatalf("active user with invalid metadata returned %v, want explicit metadata error", err)
	}
	if err := r.EnsureSchema(ctx); err == nil {
		t.Fatal("startup migration accepted invalid active metadata")
	}
	if _, err := r.pool.Exec(ctx, `UPDATE health_registry.users SET db_credential_version=1 WHERE username=$1`, user.Username); err != nil {
		t.Fatalf("restore credential version: %v", err)
	}
}

func TestProvisioningTransitionCompareAndSet(t *testing.T) {
	r, ctx := newEmptyTestRegistry(t)
	tenantID := uuid.New()
	op := ProvisioningOperation{
		OperationID: uuid.New(),
		TenantID:    tenantID,
		Username:    "alpha",
		SchemaName:  "health_alpha",
		DBRole:      tenantDBRole(tenantID),
		State:       ProvisioningStatePending,
	}
	if err := r.CreateProvisioningOperation(ctx, op); err != nil {
		t.Fatalf("CreateProvisioningOperation: %v", err)
	}
	transitions := [][2]ProvisioningState{
		{ProvisioningStatePending, ProvisioningStateProvisioning},
		{ProvisioningStateProvisioning, ProvisioningStateFailed},
		{ProvisioningStateFailed, ProvisioningStateProvisioning},
		{ProvisioningStateProvisioning, ProvisioningStateActive},
	}
	for _, transition := range transitions {
		if err := r.TransitionProvisioningOperation(ctx, op.OperationID, transition[0], transition[1], ""); err != nil {
			t.Fatalf("transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}
	if err := r.TransitionProvisioningOperation(ctx, op.OperationID, ProvisioningStatePending, ProvisioningStateFailed, "stale writer"); !errors.Is(err, ErrProvisioningStateConflict) {
		t.Fatalf("stale compare-and-set returned %v, want ErrProvisioningStateConflict", err)
	}
}
