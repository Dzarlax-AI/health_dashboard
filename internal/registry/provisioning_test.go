package registry

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRegistrySourceDoesNotLogGeneratedAPIKey(t *testing.T) {
	source, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	for _, forbidden := range []string{"generated: %s", "generated: " + `" + apiKey`, "generated API key: %s"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("registry.go contains generated API-key logging pattern %q", forbidden)
		}
	}
}

func TestProvisioningConstraintCatalogLookupIsTableScoped(t *testing.T) {
	source, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	want := "conrelid = 'health_registry.users'::regclass"
	if !strings.Contains(string(source), want) {
		t.Fatalf("users provisioning-state constraint lookup must include %q", want)
	}
}

func TestProvisioningTransitionMatrix(t *testing.T) {
	allowed := map[ProvisioningState][]ProvisioningState{
		ProvisioningStatePending:      {ProvisioningStateProvisioning, ProvisioningStateFailed},
		ProvisioningStateProvisioning: {ProvisioningStateActive, ProvisioningStateFailed},
		ProvisioningStateFailed:       {ProvisioningStateProvisioning},
	}
	states := []ProvisioningState{ProvisioningStatePending, ProvisioningStateProvisioning, ProvisioningStateActive, ProvisioningStateFailed}

	for _, from := range states {
		for _, to := range states {
			want := false
			for _, candidate := range allowed[from] {
				want = want || candidate == to
			}
			if got := CanTransitionProvisioning(from, to); got != want {
				t.Errorf("CanTransitionProvisioning(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestProvisioningStateValid(t *testing.T) {
	for _, state := range []ProvisioningState{ProvisioningStatePending, ProvisioningStateProvisioning, ProvisioningStateActive, ProvisioningStateFailed} {
		if !state.Valid() {
			t.Errorf("state %q should be valid", state)
		}
	}
	if ProvisioningState("unknown").Valid() {
		t.Fatal("unknown state should be invalid")
	}
}

func TestValidateUserProvisioningMetadata(t *testing.T) {
	id := uuid.New()
	valid := User{TenantID: id, DBRole: tenantDBRole(id), DBCredentialVersion: 1}
	if err := validateUserProvisioningMetadata(&valid); err != nil {
		t.Fatalf("valid metadata: %v", err)
	}
	for name, user := range map[string]User{
		"missing tenant ID": {DBRole: tenantDBRole(id), DBCredentialVersion: 1},
		"mismatched role":   {TenantID: id, DBRole: tenantDBRole(uuid.New()), DBCredentialVersion: 1},
		"missing role":      {TenantID: id, DBCredentialVersion: 1},
		"invalid version":   {TenantID: id, DBRole: tenantDBRole(id)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateUserProvisioningMetadata(&user); err == nil {
				t.Fatal("invalid metadata was accepted")
			}
		})
	}
}

func TestSchemaContractMetadataValidation(t *testing.T) {
	valid := SchemaContractMetadata{Version: 1, Checksum: strings.Repeat("a", 64)}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid schema contract metadata: %v", err)
	}
	for name, metadata := range map[string]SchemaContractMetadata{
		"zero version":     {Checksum: valid.Checksum},
		"empty checksum":   {Version: 1},
		"short checksum":   {Version: 1, Checksum: "abc"},
		"non-hex checksum": {Version: 1, Checksum: strings.Repeat("g", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := metadata.Validate(); err == nil {
				t.Fatal("invalid contract metadata was accepted")
			}
		})
	}
}
