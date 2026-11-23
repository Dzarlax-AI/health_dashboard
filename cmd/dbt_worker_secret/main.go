// Command dbt_worker_secret prepares one tenant credential on the operator host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func main() {
	schema := flag.String("schema", "", "registered tenant schema")
	id := flag.String("tenant-id", "", "registered tenant UUID")
	out := flag.String("out", "", "new secret file in a private operator directory")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := prepare(ctx, *schema, *id, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(ctx context.Context, schema, id, out string) error {
	tenantID, err := uuid.Parse(id)
	if err != nil || tenantID == uuid.Nil || schema == "" || !filepath.IsAbs(out) {
		return errors.New("specify schema, tenant UUID and absolute output path")
	}
	cfg, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil || !cfg.Enabled {
		return errors.New("invalid isolated installation configuration")
	}
	base, err := url.Parse(cfg.TenantDSNBase)
	if err != nil || base.Host == "" || base.User != nil {
		return errors.New("worker provisioning requires a credential-free PostgreSQL URL")
	}
	for key := range base.Query() {
		if key != "sslmode" && key != "sslrootcert" {
			return errors.New("unsupported connection parameter for dbt worker")
		}
	}
	reg, err := registry.NewWithExpectedIdentity(ctx, cfg.RegistryDSN, tenants.DatabaseRegistryRole)
	if err != nil {
		return errors.New("unable to verify registry identity")
	}
	defer reg.Close()
	user, err := reg.GetBySchema(ctx, schema)
	if err != nil || user == nil || user.TenantID != tenantID || user.DBRole != tenants.TenantRoleName(tenantID) ||
		user.ProvisioningState != registry.ProvisioningStateActive || !user.DBIsolationReady ||
		user.SchemaContractVersion != storage.SchemaContractVersion || user.SchemaContractChecksum != storage.SchemaContractChecksum() {
		return errors.New("tenant registration does not match the requested active contract")
	}
	password, err := cfg.Credentials.Derive(tenantID, user.DBRole, user.DBCredentialVersion)
	if err != nil {
		return errors.New("unable to derive registered credential version")
	}
	db, closeTenant, err := tenants.OpenReadOnlyTenant(ctx, cfg, schema)
	if err != nil {
		return errors.New("tenant contract verification failed")
	}
	defer closeTenant()
	if err := db.AssertIdentity(ctx, user.DBRole, schema); err != nil {
		return errors.New("tenant identity verification failed")
	}
	base.User = url.UserPassword(user.DBRole, password)
	return writeSecret(out, base.String())
}

func writeSecret(out, value string) error {
	f, err := os.CreateTemp(filepath.Dir(out), ".worker-secret-*")
	if err != nil {
		return errors.New("unable to create private temporary file")
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(value + "\n"); err == nil {
		err = f.Chmod(0400)
	}
	if closeErr := f.Close(); err != nil || closeErr != nil {
		return errors.New("unable to write private file")
	}
	if err := os.Link(f.Name(), out); err != nil {
		return errors.New("unable to publish new file (destination must be unused)")
	}
	return nil
}
