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
	"strings"
	"time"

	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

type mode string

const (
	modeInventory             mode = "inventory"
	modeDryRun                mode = "dry-run"
	modeApply                 mode = "apply"
	modeVerify                mode = "verify"
	modeRotate                mode = "rotate"
	modeRollback              mode = "rollback"
	modeMigrateContract       mode = "migrate-contract"
	modeAudit                 mode = "audit"
	modeBootstrapDBIdentities mode = "bootstrap-db-identities"
	modeFinalizeDBIdentities  mode = "finalize-db-identities"
	modeRollbackDBIdentities  mode = "rollback-db-identities"
	modeVerifyDBIdentities    mode = "verify-db-identities"
)

var (
	ErrAuditFailed      = errors.New("tenant fleet audit failed")
	ErrAuditOperational = errors.New("tenant fleet audit operation failed")
	ErrMigrationFailed  = errors.New("tenant contract migration failed")
	ErrJSONOutputFailed = errors.New("write sanitized JSON output failed")
)

type options struct {
	mode                                  mode
	schema                                string
	all, confirm                          bool
	credentialVersion, expectedOldVersion int
	image, manifest, primarySchema        string
	allowLegacyBridge                     bool
}

var schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func parseOptions(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("tenant_isolation", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var m string
	fs.StringVar(&m, "mode", "", "inventory|dry-run|apply|verify|rotate|rollback|migrate-contract|audit|bootstrap-db-identities|finalize-db-identities|rollback-db-identities|verify-db-identities")
	fs.StringVar(&o.schema, "schema", "", "canonical active registry schema")
	fs.BoolVar(&o.all, "all", false, "process every active tenant, primary last")
	fs.BoolVar(&o.confirm, "confirm", false, "confirm mutation")
	fs.IntVar(&o.credentialVersion, "credential-version", 0, "positive target credential version")
	fs.IntVar(&o.expectedOldVersion, "expected-old-version", 0, "positive expected current version for rotation")
	fs.StringVar(&o.image, "image", "", "immutable pre-change image digest")
	fs.StringVar(&o.manifest, "manifest", "", "durable rollback manifest path")
	fs.StringVar(&o.primarySchema, "primary-schema", "", "canonical active schema to process last with --all")
	fs.BoolVar(&o.allowLegacyBridge, "allow-legacy-bridge", false, "allow the declared pre-finalize health_user bridge")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("positional arguments are not accepted")
	}
	o.mode = mode(m)
	switch o.mode {
	case modeInventory, modeDryRun, modeApply, modeVerify, modeRotate, modeRollback, modeMigrateContract, modeAudit, modeBootstrapDBIdentities, modeFinalizeDBIdentities, modeRollbackDBIdentities, modeVerifyDBIdentities:
	default:
		return o, errors.New("invalid --mode")
	}
	identityVerify := o.mode == modeVerifyDBIdentities
	identityMode := o.mode == modeBootstrapDBIdentities || o.mode == modeFinalizeDBIdentities || o.mode == modeRollbackDBIdentities
	fleetOnly := o.mode == modeMigrateContract || o.mode == modeAudit
	if identityMode || identityVerify {
		if o.schema != "" || o.all || o.primarySchema != "" {
			return o, errors.New("database identity modes reject tenant targets")
		}
	} else if fleetOnly {
		if !o.all || o.schema != "" {
			return o, errors.New("fleet mode requires --all and rejects --schema")
		}
	} else if (o.schema == "") == (!o.all) {
		return o, errors.New("exactly one of --schema or --all is required")
	}
	if o.allowLegacyBridge && !identityVerify {
		return o, errors.New("--allow-legacy-bridge is valid only with verify-db-identities")
	}
	if identityVerify && (o.confirm || o.manifest != "" || o.image != "" || o.credentialVersion != 0 || o.expectedOldVersion != 0) {
		return o, errors.New("verify-db-identities rejects mutation flags")
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
	mutating := o.mode == modeApply || o.mode == modeRotate || o.mode == modeRollback || o.mode == modeMigrateContract || identityMode
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
	if (o.mode == modeBootstrapDBIdentities || o.mode == modeFinalizeDBIdentities || o.mode == modeRollbackDBIdentities) && o.manifest == "" {
		return o, errors.New("database identity bootstrap, finalize, and rollback require --manifest")
	}
	if o.mode == modeDryRun && ((o.image == "") != (o.manifest == "")) {
		return o, errors.New("dry-run manifest output requires both --image and --manifest")
	}
	if fleetOnly && (o.credentialVersion != 0 || o.expectedOldVersion != 0 || o.image != "" || o.manifest != "") {
		return o, errors.New("fleet contract modes reject unrelated mutation flags")
	}
	if o.mode == modeAudit && o.confirm {
		return o, errors.New("audit rejects mutation confirmation")
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
	if o.mode == modeBootstrapDBIdentities || o.mode == modeFinalizeDBIdentities || o.mode == modeRollbackDBIdentities {
		return runDatabaseIdentityMode(ctx, o, out)
	}
	if o.mode == modeVerifyDBIdentities {
		return runFixedIdentityVerify(ctx, o, out)
	}
	fleetMode := o.mode == modeMigrateContract || o.mode == modeAudit
	cfg, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil {
		if fleetMode {
			return writeFleetSetupFailure(o, out, "configuration", err)
		}
		return err
	}
	if !cfg.Enabled {
		err = errors.New("tenant database isolation configuration must be enabled")
		if fleetMode {
			return writeFleetSetupFailure(o, out, "configuration", err)
		}
		return err
	}
	m, err := tenants.NewMigratorWithRegistryLock(ctx, cfg.AdminDSN, cfg.RegistryDSN, cfg.TenantDSNBase, cfg.Credentials)
	if err != nil {
		if fleetMode {
			return writeFleetSetupFailure(o, out, "open migration administrator", err)
		}
		return safeCauseError{"open migration administrator", err}
	}
	defer m.Close()
	if fleetMode {
		return runFleetMode(ctx, o, out, m)
	}
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

func runFixedIdentityVerify(ctx context.Context, o options, out io.Writer) error {
	adminDSN := strings.TrimSpace(os.Getenv("ADMIN_DATABASE_URL"))
	registryDSN := strings.TrimSpace(os.Getenv("REGISTRY_DATABASE_URL"))
	if adminDSN == "" || registryDSN == "" {
		return errors.New("fixed identity verification configuration is incomplete (values redacted)")
	}
	v, err := tenants.NewFixedIdentityVerifier(ctx, adminDSN, registryDSN)
	if err != nil {
		return err
	}
	defer v.Close()
	result, verifyErr := v.Verify(ctx, o.allowLegacyBridge)
	if outputErr := writeCompactJSON(out, result); outputErr != nil {
		return outputErr
	}
	if verifyErr != nil {
		return safeCauseError{action: "fixed database identity verification", cause: verifyErr}
	}
	if result.Status != tenants.AuditStatusPass {
		return errors.New("fixed database identity verification failed")
	}
	return nil
}

func runDatabaseIdentityMode(ctx context.Context, o options, out io.Writer) error {
	cfg, err := tenants.ParseDatabaseIdentityConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	b, err := tenants.OpenDatabaseIdentityBootstrap(ctx, cfg.BootstrapDSN)
	if err != nil {
		return err
	}
	defer b.Close()
	switch o.mode {
	case modeBootstrapDBIdentities:
		if cfg.AdminPassword == "" || cfg.RegistryPassword == "" {
			return errors.New("fixed database identity credentials are not configured (values redacted)")
		}
		var manifest tenants.DatabaseIdentityManifest
		if _, statErr := os.Lstat(o.manifest); errors.Is(statErr, os.ErrNotExist) {
			snapshot, snapshotErr := b.Snapshot(ctx)
			if snapshotErr != nil {
				return safeCauseError{action: "capture database identity rollback metadata", cause: snapshotErr}
			}
			if snapshotErr = tenants.WriteDatabaseIdentityManifest(o.manifest, snapshot); snapshotErr != nil {
				return safeCauseError{action: "write database identity rollback metadata", cause: snapshotErr}
			}
			manifest = snapshot
		} else if statErr != nil {
			return safeCauseError{action: "inspect database identity rollback metadata", cause: statErr}
		} else {
			var readErr error
			manifest, readErr = tenants.ReadDatabaseIdentityManifest(o.manifest)
			if readErr != nil {
				return safeCauseError{action: "validate database identity rollback metadata", cause: readErr}
			}
		}
		err = b.Bootstrap(ctx, manifest, cfg.AdminPassword, cfg.RegistryPassword)
	case modeFinalizeDBIdentities:
		manifest, readErr := tenants.ReadDatabaseIdentityManifest(o.manifest)
		if readErr != nil {
			return safeCauseError{action: "read database identity rollback metadata", cause: readErr}
		}
		err = b.Finalize(ctx, manifest)
	case modeRollbackDBIdentities:
		manifest, readErr := tenants.ReadDatabaseIdentityManifest(o.manifest)
		if readErr != nil {
			return safeCauseError{action: "read database identity rollback metadata", cause: readErr}
		}
		var rollbackResult tenants.DatabaseIdentityRollbackResult
		rollbackResult, err = b.Rollback(ctx, manifest)
		if err == nil && len(rollbackResult.RetainedArtifacts) > 0 {
			if outputErr := writeCompactJSON(out, rollbackResult); outputErr != nil {
				return outputErr
			}
		}
	}
	if err != nil {
		return safeCauseError{action: "database identity operation", cause: err}
	}
	_, err = fmt.Fprintln(out, "database identity operation completed")
	return err
}

type fleetMigrator interface {
	AuditFleet(context.Context) (tenants.AuditResult, error)
	AcquireFleetMigrationLock(context.Context) (*tenants.FleetMigrationLock, error)
	PrepareContractMigrationFleet(context.Context) (tenants.ContractMigrationFleet, error)
	ValidateContractMigrationFleet(context.Context, string) (tenants.ContractMigrationFleet, error)
	MigrateTenantContract(context.Context, tenants.TenantInventory, []string) error
}

type auditJSON struct {
	tenants.AuditResult
	Error string `json:"error,omitempty"`
}

type migrationJSON struct {
	Status                 string `json:"status"`
	TargetContractVersion  int    `json:"target_contract_version"`
	TargetContractChecksum string `json:"target_contract_checksum"`
	Attempted              int    `json:"attempted"`
	Completed              int    `json:"completed"`
	FailedTenantRef        string `json:"failed_tenant_ref,omitempty"`
	ElapsedMS              int64  `json:"elapsed_ms"`
}

func writeFleetSetupFailure(o options, out io.Writer, action string, cause error) error {
	if o.mode == modeAudit {
		payload := auditJSON{
			AuditResult: stableAuditResult(tenants.AuditResult{
				Status:                 tenants.AuditStatusFail,
				TargetContractVersion:  storage.SchemaContractVersion,
				TargetContractChecksum: storage.SchemaContractChecksum(),
			}),
			Error: "audit_operational_error",
		}
		return writeOutcomeJSON(out, payload, safeCauseError{action: "tenant fleet audit " + action, cause: errors.Join(ErrAuditOperational, cause)})
	}
	payload := migrationJSON{
		Status:                 tenants.AuditStatusFail,
		TargetContractVersion:  storage.SchemaContractVersion,
		TargetContractChecksum: storage.SchemaContractChecksum(),
	}
	return writeOutcomeJSON(out, payload, safeCauseError{action: "tenant contract migration " + action, cause: errors.Join(ErrMigrationFailed, cause)})
}

func stableAuditResult(result tenants.AuditResult) tenants.AuditResult {
	if result.Counts.RegistryByState == nil {
		result.Counts.RegistryByState = map[string]int{}
	}
	if result.Findings == nil {
		result.Findings = []tenants.AuditFinding{}
	}
	return result
}

func writeOutcomeJSON(out io.Writer, value any, outcome error) error {
	if outputErr := writeCompactJSON(out, value); outputErr != nil {
		if outcome != nil {
			return errors.Join(outcome, outputErr)
		}
		return outputErr
	}
	return outcome
}

func writeCompactJSON(out io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return safeCauseError{action: "write sanitized JSON output", cause: errors.Join(ErrJSONOutputFailed, err)}
	}
	encoded = append(encoded, '\n')
	n, err := out.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return safeCauseError{action: "write sanitized JSON output", cause: errors.Join(ErrJSONOutputFailed, err)}
	}
	return nil
}

func runFleetMode(ctx context.Context, o options, out io.Writer, m fleetMigrator) error {
	if o.mode == modeAudit {
		result, auditErr := m.AuditFleet(ctx)
		payload := auditJSON{AuditResult: stableAuditResult(result)}
		var outcome error
		if auditErr != nil {
			payload.Status = tenants.AuditStatusFail
			payload.Error = "audit_operational_error"
			outcome = safeCauseError{action: "tenant fleet audit", cause: errors.Join(ErrAuditOperational, auditErr)}
		} else if result.Status != tenants.AuditStatusPass {
			outcome = ErrAuditFailed
		}
		return writeOutcomeJSON(out, payload, outcome)
	}
	if o.mode != modeMigrateContract {
		return errors.New("unsupported fleet mode")
	}
	started := time.Now()
	summary := migrationJSON{
		Status:                 tenants.AuditStatusFail,
		TargetContractVersion:  storage.SchemaContractVersion,
		TargetContractChecksum: storage.SchemaContractChecksum(),
	}
	lock, err := m.AcquireFleetMigrationLock(ctx)
	if err != nil {
		summary.ElapsedMS = time.Since(started).Milliseconds()
		return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration lock", cause: errors.Join(ErrMigrationFailed, err)})
	}
	defer func() { _ = lock.Release() }()
	fleet, err := m.PrepareContractMigrationFleet(ctx)
	if err != nil {
		summary.ElapsedMS = time.Since(started).Milliseconds()
		return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration preflight", cause: errors.Join(ErrMigrationFailed, err)})
	}
	inventories, err := reorderInventoriesPrimaryLast(fleet.Inventories, o.primarySchema)
	if err != nil {
		summary.ElapsedMS = time.Since(started).Milliseconds()
		return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration ordering", cause: errors.Join(ErrMigrationFailed, err)})
	}
	for _, inventory := range inventories {
		validated, validateErr := m.ValidateContractMigrationFleet(ctx, fleet.Digest)
		if validateErr != nil {
			summary.ElapsedMS = time.Since(started).Milliseconds()
			return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration fleet validation", cause: errors.Join(ErrMigrationFailed, validateErr)})
		}
		fresh, freshErr := inventoryForSchema(validated.Inventories, inventory.Schema)
		if freshErr != nil {
			summary.ElapsedMS = time.Since(started).Milliseconds()
			return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration refreshed inventory", cause: errors.Join(ErrMigrationFailed, freshErr)})
		}
		summary.Attempted++
		if err = m.MigrateTenantContract(ctx, fresh, validated.PeerSchemas); err != nil {
			summary.FailedTenantRef = tenants.TenantReference(fresh)
			summary.ElapsedMS = time.Since(started).Milliseconds()
			return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration", cause: errors.Join(ErrMigrationFailed, err)})
		}
		summary.Completed++
	}
	if _, err = m.ValidateContractMigrationFleet(ctx, fleet.Digest); err != nil {
		summary.ElapsedMS = time.Since(started).Milliseconds()
		return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration final fleet validation", cause: errors.Join(ErrMigrationFailed, err)})
	}
	finalAudit, auditErr := m.AuditFleet(ctx)
	if auditErr != nil || finalAudit.Status != tenants.AuditStatusPass {
		summary.ElapsedMS = time.Since(started).Milliseconds()
		if auditErr != nil {
			return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration final audit", cause: errors.Join(ErrMigrationFailed, ErrAuditOperational, auditErr)})
		}
		return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration final audit", cause: errors.Join(ErrMigrationFailed, ErrAuditFailed)})
	}
	if err = lock.Release(); err != nil {
		summary.ElapsedMS = time.Since(started).Milliseconds()
		return writeOutcomeJSON(out, summary, safeCauseError{action: "tenant contract migration lock release", cause: errors.Join(ErrMigrationFailed, err)})
	}
	summary.Status = tenants.AuditStatusPass
	summary.ElapsedMS = time.Since(started).Milliseconds()
	if outputErr := writeCompactJSON(out, summary); outputErr != nil {
		return outputErr
	}
	return nil
}

func inventoryForSchema(inventories []tenants.TenantInventory, schema string) (tenants.TenantInventory, error) {
	var found tenants.TenantInventory
	count := 0
	for _, inventory := range inventories {
		if inventory.Schema == schema {
			found = inventory
			count++
		}
	}
	if count != 1 {
		return tenants.TenantInventory{}, errors.New("refreshed migration fleet does not contain exactly one target")
	}
	return found, nil
}

func reorderInventoriesPrimaryLast(in []tenants.TenantInventory, primary string) ([]tenants.TenantInventory, error) {
	bySchema := make(map[string]tenants.TenantInventory, len(in))
	schemas := make([]string, 0, len(in))
	for _, inventory := range in {
		if _, exists := bySchema[inventory.Schema]; exists {
			return nil, errors.New("contract migration fleet contains a duplicate canonical tenant")
		}
		bySchema[inventory.Schema] = inventory
		schemas = append(schemas, inventory.Schema)
	}
	ordered, err := reorderPrimaryLast(schemas, primary)
	if err != nil {
		return nil, err
	}
	out := make([]tenants.TenantInventory, 0, len(ordered))
	for _, schema := range ordered {
		out = append(out, bySchema[schema])
	}
	return out, nil
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
