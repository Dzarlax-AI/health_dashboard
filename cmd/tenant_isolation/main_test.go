package main

import (
	"bytes"
	"errors"
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
