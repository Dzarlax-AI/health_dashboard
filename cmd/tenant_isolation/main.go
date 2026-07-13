package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"

	"health-receiver/internal/tenants"
)

type mode string

const (
	modeInventory mode = "inventory"
	modeDryRun    mode = "dry-run"
	modeApply     mode = "apply"
	modeVerify    mode = "verify"
	modeRotate    mode = "rotate"
	modeRollback  mode = "rollback"
)

type options struct {
	mode                                  mode
	schema                                string
	all, confirm                          bool
	credentialVersion, expectedOldVersion int
	image, manifest, primarySchema        string
}

var schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func parseOptions(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("tenant_isolation", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var m string
	fs.StringVar(&m, "mode", "", "inventory|dry-run|apply|verify|rotate|rollback")
	fs.StringVar(&o.schema, "schema", "", "canonical active registry schema")
	fs.BoolVar(&o.all, "all", false, "process every active tenant, primary last")
	fs.BoolVar(&o.confirm, "confirm", false, "confirm mutation")
	fs.IntVar(&o.credentialVersion, "credential-version", 0, "positive target credential version")
	fs.IntVar(&o.expectedOldVersion, "expected-old-version", 0, "positive expected current version for rotation")
	fs.StringVar(&o.image, "image", "", "immutable pre-change image digest")
	fs.StringVar(&o.manifest, "manifest", "", "rollback manifest path")
	fs.StringVar(&o.primarySchema, "primary-schema", "", "canonical active schema to process last with --all")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("positional arguments are not accepted")
	}
	o.mode = mode(m)
	switch o.mode {
	case modeInventory, modeDryRun, modeApply, modeVerify, modeRotate, modeRollback:
	default:
		return o, errors.New("invalid --mode")
	}
	if (o.schema == "") == (!o.all) {
		return o, errors.New("exactly one of --schema or --all is required")
	}
	if o.schema != "" && !schemaPattern.MatchString(o.schema) {
		return o, errors.New("schema must be a canonical lowercase identifier")
	}
	if o.all && (o.primarySchema == "" || !schemaPattern.MatchString(o.primarySchema)) {
		return o, errors.New("--all requires a valid --primary-schema")
	}
	if !o.all && o.primarySchema != "" {
		return o, errors.New("--primary-schema is valid only with --all")
	}
	mutating := o.mode == modeApply || o.mode == modeRotate || o.mode == modeRollback
	if mutating && !o.confirm {
		return o, errors.New("mutation mode requires --confirm")
	}
	if o.mode == modeApply && (o.credentialVersion <= 0 || o.image == "" || o.manifest == "") {
		return o, errors.New("apply requires positive --credential-version, --image, and --manifest")
	}
	if o.mode == modeRotate && (o.credentialVersion <= 0 || o.expectedOldVersion <= 0) {
		return o, errors.New("rotate requires positive target and expected old credential versions")
	}
	if o.mode == modeRollback && o.manifest == "" {
		return o, errors.New("rollback requires --manifest")
	}
	if o.mode == modeDryRun && ((o.image == "") != (o.manifest == "")) {
		return o, errors.New("dry-run manifest output requires both --image and --manifest")
	}
	return o, nil
}
func publicError(action, _ string) error {
	return errors.New(action + " failed (connection details redacted)")
}

type safeCauseError struct {
	action string
	cause  error
}

func (e safeCauseError) Error() string { return e.action + " failed (details redacted)" }
func (e safeCauseError) Unwrap() error { return e.cause }
func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tenant isolation:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	cfg, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return errors.New("tenant database isolation configuration must be enabled")
	}
	m, err := tenants.NewMigrator(ctx, cfg.AdminDSN, cfg.TenantDSNBase, cfg.Credentials)
	if err != nil {
		return safeCauseError{"open migration administrator", err}
	}
	defer m.Close()
	schemas := []string{o.schema}
	if o.all {
		schemas, err = m.CanonicalSchemas(ctx)
		if err != nil {
			return safeCauseError{"load canonical active tenant schemas", err}
		}
		schemas, err = reorderPrimaryLast(schemas, o.primarySchema)
		if err != nil {
			return err
		}
	}
	if o.mode == modeRollback {
		for _, schema := range schemas {
			manifest, readErr := tenants.ReadRollbackManifest(tenants.ManifestPath(o.manifest, schema, o.all))
			if readErr != nil {
				return readErr
			}
			if manifest.Inventory.Schema != schema {
				return errors.New("rollback manifest schema does not match canonical target")
			}
			if restoreErr := m.RestoreTenant(ctx, manifest.Inventory); restoreErr != nil {
				return fmt.Errorf("restore tenant %s: %w", schema, restoreErr)
			}
			fmt.Fprintf(out, "restored tenant %s; deploy image %s before legacy restart\n", schema, manifest.ImageReference)
		}
		return nil
	}
	var inventories []tenants.TenantInventory
	for _, schema := range schemas {
		i, err := m.Inventory(ctx, schema)
		if err != nil {
			return err
		}
		inventories = append(inventories, i)
	}
	if o.mode == modeApply {
		if err := prepareApplyManifests(o, inventories); err != nil {
			return err
		}
		canonical, err := m.CanonicalSchemas(ctx)
		if err != nil {
			return safeCauseError{"load canonical schemas for denial probes", err}
		}
		for _, inventory := range inventories {
			other := firstOtherSchema(canonical, inventory.Schema)
			if err := m.ApplyRestrictedTenant(ctx, inventory, other); err != nil {
				return fmt.Errorf("apply tenant %s: %w", inventory.Schema, err)
			}
			fmt.Fprintf(out, "applied and verified restricted tenant %s\n", inventory.Schema)
		}
		return nil
	}
	if o.mode == modeRotate {
		canonical, err := m.CanonicalSchemas(ctx)
		if err != nil {
			return safeCauseError{"load canonical schemas for denial probes", err}
		}
		for _, inventory := range inventories {
			if err := m.RotateTenantCredential(ctx, inventory, o.expectedOldVersion, o.credentialVersion, firstOtherSchema(canonical, inventory.Schema)); err != nil {
				return fmt.Errorf("rotate tenant %s: %w", inventory.Schema, err)
			}
			fmt.Fprintf(out, "rotated and verified restricted tenant %s to credential version %d\n", inventory.Schema, o.credentialVersion)
		}
		return nil
	}
	if o.mode == modeVerify {
		canonical, err := m.CanonicalSchemas(ctx)
		if err != nil {
			return safeCauseError{"load canonical schemas for denial probes", err}
		}
		for _, inventory := range inventories {
			other := firstOtherSchema(canonical, inventory.Schema)
			if err := m.VerifyRestrictedTenant(ctx, inventory, other); err != nil {
				return fmt.Errorf("verify tenant %s: %w", inventory.Schema, err)
			}
			fmt.Fprintf(out, "verified restricted tenant %s\n", inventory.Schema)
		}
		return nil
	}
	return renderReadOnly(o, inventories, out)
}

func firstOtherSchema(schemas []string, current string) string {
	for _, schema := range schemas {
		if schema != current {
			return schema
		}
	}
	return ""
}

func prepareApplyManifests(o options, inventories []tenants.TenantInventory) error {
	missing := map[string]tenants.RollbackManifest{}
	for _, inventory := range inventories {
		path := tenants.ManifestPath(o.manifest, inventory.Schema, o.all)
		expected, err := tenants.NewRollbackManifest(inventory, o.image)
		if err != nil {
			return err
		}
		existing, err := tenants.ReadRollbackManifest(path)
		if err == nil {
			if inventory.Registry.IsolationReady {
				continue
			}
			if existing.ImageReference != expected.ImageReference || !reflect.DeepEqual(existing.Inventory, inventory) {
				return fmt.Errorf("existing rollback manifest does not match live pre-cutover inventory for %s", inventory.Schema)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if inventory.Registry.IsolationReady {
			return fmt.Errorf("rollback manifest is missing for already-isolated tenant %s", inventory.Schema)
		}
		missing[path] = expected
	}
	return tenants.WriteRollbackManifestSet(missing)
}
func reorderPrimaryLast(schemas []string, primary string) ([]string, error) {
	found := 0
	ordered := make([]string, 0, len(schemas))
	for _, s := range schemas {
		if s == primary {
			found++
			continue
		}
		ordered = append(ordered, s)
	}
	if found != 1 {
		return nil, errors.New("--primary-schema must match exactly one canonical active tenant")
	}
	return append(ordered, primary), nil
}

func renderReadOnly(o options, inventories []tenants.TenantInventory, out io.Writer) error {
	if o.mode == modeDryRun {
		var encoded bytes.Buffer
		manifests := map[string]tenants.RollbackManifest{}
		for _, i := range inventories {
			p, e := tenants.BuildMigrationPlan(i)
			if e != nil {
				return e
			}
			if e = json.NewEncoder(&encoded).Encode(p); e != nil {
				return e
			}
			if o.manifest != "" {
				m, e := tenants.NewRollbackManifest(i, o.image)
				if e != nil {
					return e
				}
				manifests[tenants.ManifestPath(o.manifest, i.Schema, o.all)] = m
			}
		}
		if len(manifests) > 0 {
			if err := tenants.WriteRollbackManifestSet(manifests); err != nil {
				return err
			}
		}
		_, err := io.Copy(out, &encoded)
		return err
	}
	for _, i := range inventories {
		switch o.mode {
		case modeInventory:
			if err := json.NewEncoder(out).Encode(i); err != nil {
				return err
			}
		}
	}
	return nil
}
