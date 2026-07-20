package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"health-receiver/internal/tenants"
)

func TestParseOptionsRejectsUnsafeArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing target", []string{"--mode", "inventory"}},
		{"conflicting targets", []string{"--mode", "inventory", "--schema", "health_a", "--all"}},
		{"free form schema", []string{"--mode", "inventory", "--schema", "Health A"}},
		{"unknown mode", []string{"--mode", "destroy", "--schema", "health_a"}},
		{"apply without confirmation", []string{"--mode", "apply", "--schema", "health_a", "--credential-version", "1", "--image", "sha256:abc", "--manifest", "m.json"}},
		{"nonpositive version", []string{"--mode", "rotate", "--schema", "health_a", "--credential-version", "0", "--confirm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseOptions(tt.args); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseOptionsDatabaseIdentityModes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"--mode", "bootstrap-db-identities", "--confirm", "--manifest", "/tmp/identity.json"}, true},
		{[]string{"--mode", "bootstrap-db-identities", "--manifest", "/tmp/identity.json"}, false},
		{[]string{"--mode", "bootstrap-db-identities", "--confirm"}, false},
		{[]string{"--mode", "finalize-db-identities", "--confirm", "--manifest", "/tmp/identity.json"}, true},
		{[]string{"--mode", "finalize-db-identities", "--confirm"}, false},
		{[]string{"--mode", "rollback-db-identities", "--confirm", "--manifest", "/tmp/identity.json"}, true},
		{[]string{"--mode", "rollback-db-identities", "--confirm"}, false},
		{[]string{"--mode", "verify-db-identities"}, true},
		{[]string{"--mode", "verify-db-identities", "--allow-legacy-bridge"}, true},
		{[]string{"--mode", "verify-db-identities", "--confirm"}, false},
		{[]string{"--mode", "bootstrap-db-identities", "--confirm", "--manifest", "/tmp/identity.json", "--schema", "health_a"}, false},
	} {
		_, err := parseOptions(tc.args)
		if (err == nil) != tc.ok {
			t.Fatalf("parseOptions(%q) error=%v, want ok=%v", tc.args, err, tc.ok)
		}
	}
}

func cliInventory(schema string, id uuid.UUID) tenants.TenantInventory {
	return tenants.TenantInventory{Schema: schema, TenantID: id, Role: tenants.TenantRoleName(id), CredentialVersion: 1, Registry: tenants.RegistryMetadata{Username: schema, TenantID: id, Schema: schema, Role: tenants.TenantRoleName(id), CredentialVersion: 1, State: "active"}}
}

func TestRenderDryRunPreservesCanonicalAllOrderAndWritesUniqueManifests(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "rollback.json")
	o := options{mode: modeDryRun, all: true, image: "repo/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", manifest: base}
	a := cliInventory("health_canary", uuid.MustParse("11111111-1111-4111-8111-111111111111"))
	b := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	var out bytes.Buffer
	if err := renderReadOnly(o, []tenants.TenantInventory{a, b}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Index(text, "health_canary") > strings.Index(text, "health_primary") {
		t.Fatal("canonical input order was not preserved")
	}
	for _, schema := range []string{a.Schema, b.Schema} {
		path := tenants.ManifestPath(base, schema, true)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("manifest %s: %v", path, err)
		}
	}
}

func TestPrimaryOrderingIgnoresAdminStatusAndUsesExplicitSchema(t *testing.T) {
	got, err := reorderPrimaryLast([]string{"admin_a", "admin_primary", "admin_b"}, "admin_primary")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"admin_a", "admin_b", "admin_primary"}
	for n := range want {
		if got[n] != want[n] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if _, err = reorderPrimaryLast([]string{"a", "b"}, "missing"); err == nil {
		t.Fatal("expected noncanonical primary rejection")
	}
}

func TestParseAllRequiresExplicitPrimarySchema(t *testing.T) {
	if _, err := parseOptions([]string{"--mode", "inventory", "--all"}); err == nil {
		t.Fatal("expected missing primary rejection")
	}
	o, err := parseOptions([]string{"--mode", "inventory", "--all", "--primary-schema", "health_primary"})
	if err != nil {
		t.Fatal(err)
	}
	if o.primarySchema != "health_primary" {
		t.Fatal("primary lost")
	}
}

func TestParseOptionsAcceptsExplicitRollback(t *testing.T) {
	o, err := parseOptions([]string{"--mode", "rollback", "--schema", "health_a", "--confirm", "--manifest", "before.json"})
	if err != nil {
		t.Fatal(err)
	}
	if o.mode != modeRollback || o.schema != "health_a" {
		t.Fatalf("unexpected options: %#v", o)
	}
}

func TestRedactedErrorDoesNotExposeConnections(t *testing.T) {
	err := publicError("open admin database", "postgres://secret:password@host/db")
	if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestSafeCauseErrorPreservesCauseWithoutRenderingIt(t *testing.T) {
	cause := errors.New("postgres://user:password@host/db")
	err := safeCauseError{action: "open migration administrator", cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("cause was discarded")
	}
	if strings.Contains(err.Error(), "postgres://") || strings.Contains(err.Error(), "password") {
		t.Fatalf("cause leaked: %v", err)
	}
}

func TestParseFleetModesRequireAllPrimaryAndConfirmation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{"audit", []string{"--mode", "audit", "--all", "--primary-schema", "health_primary"}, true},
		{"audit schema", []string{"--mode", "audit", "--schema", "health_a"}, false},
		{"audit confirm", []string{"--mode", "audit", "--all", "--primary-schema", "health_primary", "--confirm"}, false},
		{"audit mutation flag", []string{"--mode", "audit", "--all", "--primary-schema", "health_primary", "--credential-version", "2"}, false},
		{"migrate", []string{"--mode", "migrate-contract", "--all", "--primary-schema", "health_primary", "--confirm"}, true},
		{"migrate schema", []string{"--mode", "migrate-contract", "--schema", "health_a", "--confirm"}, false},
		{"migrate no confirm", []string{"--mode", "migrate-contract", "--all", "--primary-schema", "health_primary"}, false},
		{"migrate unrelated mutation flag", []string{"--mode", "migrate-contract", "--all", "--primary-schema", "health_primary", "--confirm", "--manifest", "x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOptions(tt.args)
			if (err == nil) != tt.ok {
				t.Fatalf("parse error = %v, want ok=%t", err, tt.ok)
			}
		})
	}
}

type fakeFleetMigrator struct {
	auditResult    tenants.AuditResult
	auditErr       error
	fleet          tenants.ContractMigrationFleet
	preflightErr   error
	migrateErrAt   string
	migrated       []string
	migratedRoles  []string
	validateCalls  int
	validateErrAt  int
	validatedFleet *tenants.ContractMigrationFleet
}

func (f *fakeFleetMigrator) AuditFleet(context.Context) (tenants.AuditResult, error) {
	if f.auditResult.Status == "" && f.auditErr == nil {
		return tenants.AuditResult{Status: tenants.AuditStatusPass}, nil
	}
	return f.auditResult, f.auditErr
}
func (f *fakeFleetMigrator) AcquireFleetMigrationLock(context.Context) (*tenants.FleetMigrationLock, error) {
	return &tenants.FleetMigrationLock{}, nil
}
func (f *fakeFleetMigrator) PrepareContractMigrationFleet(context.Context) (tenants.ContractMigrationFleet, error) {
	return f.fleet, f.preflightErr
}
func (f *fakeFleetMigrator) MigrateTenantContract(_ context.Context, i tenants.TenantInventory, _ []string) error {
	f.migrated = append(f.migrated, i.Schema)
	f.migratedRoles = append(f.migratedRoles, i.Role)
	if i.Schema == f.migrateErrAt {
		return errors.New("postgres://raw_user:password@db/private")
	}
	return nil
}
func (f *fakeFleetMigrator) ValidateContractMigrationFleet(_ context.Context, _ string) (tenants.ContractMigrationFleet, error) {
	f.validateCalls++
	if f.validateCalls == f.validateErrAt {
		return tenants.ContractMigrationFleet{}, tenants.ErrContractMigrationFleetUnstable
	}
	if f.validatedFleet != nil {
		return *f.validatedFleet, nil
	}
	return f.fleet, nil
}

func TestRunMigrateContractUsesFreshlyValidatedInventory(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	fresh := primary
	fresh.Role = "fresh_validated_role"
	validated := tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{fresh}, PeerSchemas: []string{primary.Schema}, Digest: "digest"}
	f := &fakeFleetMigrator{
		fleet:          tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{primary}, PeerSchemas: []string{primary.Schema}, Digest: "digest"},
		validatedFleet: &validated,
	}
	var out bytes.Buffer
	if err := runFleetMode(context.Background(), options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}, &out, f); err != nil {
		t.Fatal(err)
	}
	if len(f.migratedRoles) != 1 || f.migratedRoles[0] != fresh.Role {
		t.Fatalf("migration used stale inventory roles: %v", f.migratedRoles)
	}
}

func TestRunAuditWritesOneJSONAndReturnsLogicalSentinel(t *testing.T) {
	f := &fakeFleetMigrator{auditResult: tenants.AuditResult{Status: tenants.AuditStatusFail, Findings: []tenants.AuditFinding{{Code: "schema_contract_mismatch", Scope: "contract", TenantRef: "safe-ref"}}}}
	var out bytes.Buffer
	err := runFleetMode(context.Background(), options{mode: modeAudit, all: true, primarySchema: "health_primary"}, &out, f)
	if !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("error = %v, want ErrAuditFailed", err)
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(out.String()))
	if decoder.Decode(&decoded) != nil || decoder.Decode(&decoded) != io.EOF || strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
		t.Fatalf("expected one compact JSON object, got %q", out.String())
	}
	assertStableAuditCollections(t, decoded)
	for _, secret := range []string{"health_primary", "postgres://", "password", "raw_user"} {
		if strings.Contains(out.String()+err.Error(), secret) {
			t.Fatalf("secret %q leaked in %q / %v", secret, out.String(), err)
		}
	}
}

func TestRunAuditOperationalFailureWritesSanitizedJSON(t *testing.T) {
	f := &fakeFleetMigrator{
		auditResult: tenants.AuditResult{Status: tenants.AuditStatusFail},
		auditErr:    errors.New("postgres://raw_user:password@db/private health_secret tenant_role_secret"),
	}
	var out bytes.Buffer
	err := runFleetMode(context.Background(), options{mode: modeAudit, all: true, primarySchema: "health_primary"}, &out, f)
	if err == nil || !strings.Contains(out.String(), `"error":"audit_operational_error"`) {
		t.Fatalf("output/error = %q / %v", out.String(), err)
	}
	var decoded map[string]any
	if json.Unmarshal(out.Bytes(), &decoded) != nil {
		t.Fatalf("invalid operational JSON: %q", out.String())
	}
	assertStableAuditCollections(t, decoded)
	for _, secret := range []string{"health_secret", "tenant_role_secret", "postgres://", "password", "raw_user"} {
		if strings.Contains(out.String()+err.Error(), secret) {
			t.Fatalf("secret %q leaked in %q / %v", secret, out.String(), err)
		}
	}
}

func TestRunAuditPassUsesStableJSONShape(t *testing.T) {
	f := &fakeFleetMigrator{auditResult: tenants.AuditResult{Status: tenants.AuditStatusPass}}
	var out bytes.Buffer
	if err := runFleetMode(context.Background(), options{mode: modeAudit}, &out, f); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(out.Bytes(), &decoded) != nil {
		t.Fatalf("invalid pass JSON: %q", out.String())
	}
	assertStableAuditCollections(t, decoded)
}

func assertStableAuditCollections(t *testing.T, parsed map[string]any) {
	t.Helper()
	counts, ok := parsed["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts shape = %#v", parsed["counts"])
	}
	if _, ok = counts["registry_by_state"].(map[string]any); !ok {
		t.Fatalf("registry_by_state shape = %#v", counts["registry_by_state"])
	}
	if _, ok = parsed["findings"].([]any); !ok {
		t.Fatalf("findings shape = %#v", parsed["findings"])
	}
}

func TestRunMigrateContractPrimaryLastAndRedactedFailureSummary(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	other := cliInventory("health_secret", uuid.MustParse("11111111-1111-4111-8111-111111111111"))
	f := &fakeFleetMigrator{
		fleet:        tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{primary, other}, PeerSchemas: []string{primary.Schema, other.Schema}, Digest: "structural-digest"},
		migrateErrAt: primary.Schema,
	}
	var out bytes.Buffer
	err := runFleetMode(context.Background(), options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}, &out, f)
	if err == nil {
		t.Fatal("expected migration failure")
	}
	if got, want := strings.Join(f.migrated, ","), "health_secret,health_primary"; got != want {
		t.Fatalf("migration order = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), `"status":"fail"`) || !strings.Contains(out.String(), `"attempted":2`) || !strings.Contains(out.String(), `"completed":1`) || !strings.Contains(out.String(), `"failed_tenant_ref":"`) {
		t.Fatalf("unexpected summary: %q", out.String())
	}
	for _, secret := range []string{primary.Schema, other.Schema, primary.Role, other.Role, primary.TenantID.String(), other.TenantID.String(), "postgres://", "password", "raw_user"} {
		if strings.Contains(out.String()+err.Error(), secret) {
			t.Fatalf("secret %q leaked in %q / %v", secret, out.String(), err)
		}
	}
}

func TestRunMigrateContractFailsClosedWhenFleetChangesMidRun(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	other := cliInventory("health_other", uuid.MustParse("11111111-1111-4111-8111-111111111111"))
	f := &fakeFleetMigrator{
		fleet:         tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{other, primary}, PeerSchemas: []string{other.Schema, primary.Schema}, Digest: "structural-digest"},
		validateErrAt: 2,
	}
	var out bytes.Buffer
	err := runFleetMode(context.Background(), options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}, &out, f)
	if err == nil || !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("error = %v, want migration sentinel", err)
	}
	if got := strings.Join(f.migrated, ","); got != other.Schema {
		t.Fatalf("migrated after fleet drift: %q", got)
	}
	if !strings.Contains(out.String(), `"attempted":1`) || !strings.Contains(out.String(), `"completed":1`) || strings.Contains(out.String(), primary.Schema) || strings.Contains(out.String(), other.Schema) {
		t.Fatalf("unexpected drift summary: %q", out.String())
	}
}

func TestRunMigrateContractValidatesBeforeEveryTenantAndAfterFinal(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	other := cliInventory("health_other", uuid.MustParse("11111111-1111-4111-8111-111111111111"))
	f := &fakeFleetMigrator{fleet: tenants.ContractMigrationFleet{
		Inventories: []tenants.TenantInventory{other, primary},
		PeerSchemas: []string{other.Schema, primary.Schema},
		Digest:      "structural-digest",
	}}
	var out bytes.Buffer
	err := runFleetMode(context.Background(), options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}, &out, f)
	if err != nil {
		t.Fatal(err)
	}
	if f.validateCalls != 3 {
		t.Fatalf("validation calls = %d, want two pre-tenant plus final", f.validateCalls)
	}
	if !strings.Contains(out.String(), `"status":"pass"`) || !strings.Contains(out.String(), `"completed":2`) {
		t.Fatalf("unexpected summary: %q", out.String())
	}
}

func TestRunMigrateContractRejectsWrongPrimaryAndFinalAuditFailure(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	other := cliInventory("health_other", uuid.MustParse("11111111-1111-4111-8111-111111111111"))
	fleet := tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{other, primary}, PeerSchemas: []string{other.Schema, primary.Schema}, Digest: "digest"}
	for name, configure := range map[string]func(*fakeFleetMigrator, *options){
		"missing rollout primary": func(_ *fakeFleetMigrator, o *options) { o.primarySchema = "health_missing" },
		"final audit": func(f *fakeFleetMigrator, _ *options) {
			f.auditResult = tenants.AuditResult{Status: tenants.AuditStatusFail}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeFleetMigrator{fleet: fleet}
			o := options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}
			configure(f, &o)
			var out bytes.Buffer
			err := runFleetMode(context.Background(), o, &out, f)
			if !errors.Is(err, ErrMigrationFailed) || !strings.Contains(out.String(), `"status":"fail"`) {
				t.Fatalf("output/error = %q / %v", out.String(), err)
			}
			if name == "final audit" && (!errors.Is(err, ErrAuditFailed) || errors.Is(err, ErrAuditOperational)) {
				t.Fatalf("status-only final audit error = %v, want ErrAuditFailed only", err)
			}
		})
	}
}

func TestRunMigrateContractFinalAuditPreservesOperationalCause(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	auditCause := errors.New("postgres://raw_user:password@db/private")
	f := &fakeFleetMigrator{
		fleet:       tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{primary}, PeerSchemas: []string{primary.Schema}, Digest: "digest"},
		auditResult: tenants.AuditResult{Status: tenants.AuditStatusFail},
		auditErr:    auditCause,
	}
	var out bytes.Buffer
	err := runFleetMode(context.Background(), options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}, &out, f)
	for _, want := range []error{ErrMigrationFailed, ErrAuditOperational, auditCause} {
		if !errors.Is(err, want) {
			t.Fatalf("final audit error %v lost %v", err, want)
		}
	}
	if errors.Is(err, ErrAuditFailed) {
		t.Fatalf("operational failure incorrectly retained status-only sentinel: %v", err)
	}
	for _, secret := range []string{"postgres://", "password", "raw_user", primary.Schema} {
		if strings.Contains(err.Error()+out.String(), secret) {
			t.Fatalf("secret %q leaked in %q / %v", secret, out.String(), err)
		}
	}
}

type secretFailWriter struct{}

func (secretFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("/private/postgres://raw_user:password@db")
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestCompactJSONWriteFailureIsTypedAndRedacted(t *testing.T) {
	err := writeCompactJSON(secretFailWriter{}, map[string]string{"status": "fail"})
	if !errors.Is(err, ErrJSONOutputFailed) {
		t.Fatalf("error = %v, want typed JSON output error", err)
	}
	for _, secret := range []string{"postgres://", "password", "raw_user", "/private"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("writer detail leaked: %v", err)
		}
	}
}

func TestCompactJSONRejectsShortWriteAndEncodingFailure(t *testing.T) {
	if err := writeCompactJSON(shortWriter{}, map[string]string{"status": "fail"}); !errors.Is(err, io.ErrShortWrite) || !errors.Is(err, ErrJSONOutputFailed) {
		t.Fatalf("short write error = %v", err)
	}
	if err := writeCompactJSON(io.Discard, make(chan int)); !errors.Is(err, ErrJSONOutputFailed) {
		t.Fatalf("marshal error = %v", err)
	}
}

func TestOutcomeAndOutputSentinelsAreBothPreserved(t *testing.T) {
	f := &fakeFleetMigrator{auditResult: tenants.AuditResult{Status: tenants.AuditStatusFail}}
	err := runFleetMode(context.Background(), options{mode: modeAudit}, secretFailWriter{}, f)
	if !errors.Is(err, ErrAuditFailed) || !errors.Is(err, ErrJSONOutputFailed) {
		t.Fatalf("audit output error lost sentinel: %v", err)
	}
	err = writeFleetSetupFailure(options{mode: modeMigrateContract}, secretFailWriter{}, "configuration", errors.New("fixture"))
	if !errors.Is(err, ErrMigrationFailed) || !errors.Is(err, ErrJSONOutputFailed) {
		t.Fatalf("migration output error lost sentinel: %v", err)
	}
}

func TestSuccessfulMigrationFinalOutputFailureIsOnlyOutputFailure(t *testing.T) {
	primary := cliInventory("health_primary", uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	f := &fakeFleetMigrator{fleet: tenants.ContractMigrationFleet{Inventories: []tenants.TenantInventory{primary}, PeerSchemas: []string{primary.Schema}, Digest: "digest"}}
	err := runFleetMode(context.Background(), options{mode: modeMigrateContract, all: true, confirm: true, primarySchema: primary.Schema}, secretFailWriter{}, f)
	if !errors.Is(err, ErrJSONOutputFailed) || errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("successful migration output error = %v", err)
	}
}

func TestFleetSetupFailureStillWritesOneSanitizedJSON(t *testing.T) {
	for _, o := range []options{{mode: modeAudit}, {mode: modeMigrateContract}} {
		var out bytes.Buffer
		err := writeFleetSetupFailure(o, &out, "open", errors.New("postgres://raw_user:password@db health_secret"))
		if err == nil || strings.Count(strings.TrimSpace(out.String()), "\n") != 0 || !strings.Contains(out.String(), `"status":"fail"`) {
			t.Fatalf("output/error = %q / %v", out.String(), err)
		}
		for _, secret := range []string{"postgres://", "password", "raw_user", "health_secret"} {
			if strings.Contains(out.String()+err.Error(), secret) {
				t.Fatalf("setup detail %q leaked in %q / %v", secret, out.String(), err)
			}
		}
		if o.mode == modeAudit {
			var parsed map[string]any
			if json.Unmarshal(out.Bytes(), &parsed) != nil {
				t.Fatalf("invalid setup JSON: %q", out.String())
			}
			assertStableAuditCollections(t, parsed)
		}
	}
}
