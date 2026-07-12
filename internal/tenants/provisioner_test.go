package tenants

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"strings"
	"testing"
)

func TestProvisionerAdoptionChecksPrecedeCredentialMutation(t *testing.T) {
	b, err := os.ReadFile("provisioner.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	marker := strings.Index(s, "assertRoleMarker(ctx, spec)")
	password := strings.Index(s, "setRolePassword(ctx, spec.DBRole")
	if marker < 0 || password < 0 || marker > password {
		t.Fatal("duplicate role marker must be verified before password mutation")
	}
	for _, want := range []string{"rolreplication", "rolbypassrls", "pg_auth_members", "assertSchemaMarker(ctx, spec)", "assertRegistryOperation(ctx, spec,"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing catalog safety check %q", want)
		}
	}
}

func TestManagerHasNoProvisioningOrDestructiveAuthority(t *testing.T) {
	b, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, forbidden := range []string{"pgxpool", "CREATE SCHEMA", "DROP SCHEMA", "DeprovisionUserSchema", "ProvisionUserSchema"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("manager retains forbidden authority %q", forbidden)
		}
	}
}

func TestFixtureCleanupIsInternallyGuarded(t *testing.T) {
	b, _ := os.ReadFile("provisioner.go")
	s := string(b)
	for _, want := range []string{`os.Getenv("HEALTH_DB_TESTS")`, "health_test_metadata", "disposable_database"} {
		if !strings.Contains(s, want) {
			t.Fatalf("fixture cleanup missing guard %q", want)
		}
	}
}

func TestCredentialRotationChecksOwnershipBeforeMutation(t *testing.T) {
	b, _ := os.ReadFile("provisioner.go")
	s := string(b)
	start := strings.Index(s, "func (p *AdminProvisioner) RotateCredential")
	end := strings.Index(s[start:], "func (p *AdminProvisioner) Reconcile")
	if start < 0 || end < 0 {
		t.Fatal("rotation implementation missing")
	}
	body := s[start : start+end]
	for _, forbidden := range []string{"setRolePassword", "ALTER ROLE", "p.admin", "assertRoleMarker", "assertSchemaMarker"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Task 3 rotation must perform no database/catalog operation %q", forbidden)
		}
	}
	if !strings.Contains(body, "ErrCredentialRotationDeferred") {
		t.Fatal("valid rotation must return typed deferred error")
	}
}

func TestCredentialRotationIsDeferredWithoutDatabaseAccess(t *testing.T) {
	id := uuid.New()
	spec := TenantSpec{TenantID: id, OperationID: uuid.New(), SchemaName: "health_rotation", DBRole: TenantRoleName(id), CredentialVersion: 1}
	p := &AdminProvisioner{}
	if err := p.RotateCredential(context.Background(), spec, 2); !errors.Is(err, ErrCredentialRotationDeferred) {
		t.Fatalf("RotateCredential error=%v", err)
	}
}

func TestFixtureCleanupAllowsActiveOnlyBehindGuards(t *testing.T) {
	b, _ := os.ReadFile("provisioner.go")
	s := string(b)
	if !strings.Contains(s, "ProvisioningStateActive") {
		t.Fatal("guarded fixture cleanup must accept active disposable fixtures")
	}
}
