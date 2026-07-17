package storage

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSchemaContractManifestIsDeterministic(t *testing.T) {
	first := SchemaContractChecksum()
	second := SchemaContractChecksum()
	if first != second {
		t.Fatalf("schema contract checksum changed between calls: %q != %q", first, second)
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(first) {
		t.Fatalf("schema contract checksum is not lowercase SHA-256: %q", first)
	}
	if want := "962b237cf8b54bd857aa123b5cf4e764b274d4b4c19ede00244971e455d2f45e"; first != want {
		t.Fatalf("schema contract checksum = %q, want %q; bump SchemaContractVersion when intentionally changing the manifest", first, want)
	}
	if SchemaContractVersion <= 0 {
		t.Fatalf("schema contract version must be positive: %d", SchemaContractVersion)
	}
}

type contractTestRow struct {
	tenant, operation uuid.UUID
	version           *int
	checksum          *string
	err               error
}

func (r contractTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 4 {
		return errors.New("unexpected scan shape")
	}
	*(dest[0].(*uuid.UUID)) = r.tenant
	*(dest[1].(*uuid.UUID)) = r.operation
	*(dest[2].(**int)) = r.version
	*(dest[3].(**string)) = r.checksum
	return nil
}

type ambiguousContractCatalog struct {
	tenant, operation uuid.UUID
	initialVersion    *int
	initialChecksum   *string
	missingInitially  bool
	queryCalls        int
	failMutation      bool
	cancel            context.CancelFunc
	detachedReread    bool
	landedWrong       bool
}

func (c *ambiguousContractCatalog) Exec(ctx context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if c.failMutation && (strings.HasPrefix(sql, "UPDATE ") || strings.HasPrefix(sql, "INSERT INTO ")) && strings.Contains(sql, "schema_contract_") {
		c.failMutation = false
		if c.cancel != nil {
			c.cancel()
		}
		return pgconn.CommandTag{}, errors.New("ambiguous transport failure")
	}
	return pgconn.NewCommandTag("ALTER TABLE"), nil
}

func (c *ambiguousContractCatalog) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	c.queryCalls++
	if c.queryCalls == 1 {
		if c.missingInitially {
			return contractTestRow{err: pgx.ErrNoRows}
		}
		return contractTestRow{tenant: c.tenant, operation: c.operation, version: c.initialVersion, checksum: c.initialChecksum}
	}
	c.detachedReread = ctx.Err() == nil
	version, checksum := SchemaContractVersion, SchemaContractChecksum()
	if c.landedWrong {
		checksum = strings.Repeat("f", 64)
	}
	return contractTestRow{tenant: c.tenant, operation: c.operation, version: &version, checksum: &checksum}
}

func TestMigrateTenantIdentityMarkerAcceptsExactLandedAmbiguousMutation(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "insert"}[missing], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			id, op := uuid.New(), uuid.New()
			catalog := &ambiguousContractCatalog{tenant: id, operation: op, missingInitially: missing, failMutation: true, cancel: cancel}
			if err := MigrateTenantIdentityMarker(ctx, catalog, "health_fixture", id, op, SchemaContractState{}); err != nil {
				t.Fatalf("ambiguous exact landed migration: %v", err)
			}
			if !catalog.detachedReread {
				t.Fatal("ambiguous migration was not confirmed with a detached context")
			}
		})
	}
}

func TestMigrateTenantIdentityMarkerRejectsNonExactAmbiguousOutcome(t *testing.T) {
	id, op := uuid.New(), uuid.New()
	catalog := &ambiguousContractCatalog{tenant: id, operation: op, failMutation: true, landedWrong: true}
	if err := MigrateTenantIdentityMarker(t.Context(), catalog, "health_fixture", id, op, SchemaContractState{}); err == nil {
		t.Fatal("accepted ambiguous marker mutation without exact landed target")
	}
}

func TestSchemaContractManifestDeclaresProvisioningVerifierObjects(t *testing.T) {
	manifest := SchemaContractManifest()
	for _, table := range []string{"health_records", "metric_points", "auth_sessions"} {
		if !containsString(manifest.Tables, table) {
			t.Errorf("manifest does not declare table %q", table)
		}
	}
	for _, index := range []string{"idx_auth_sessions_expires", "idx_points_quality_metric", "uq_source_epochs_kind_start"} {
		if !containsString(manifest.Indexes, index) {
			t.Errorf("manifest does not declare index %q", index)
		}
	}
	definitions := make(map[string]IndexDefinition, len(manifest.IndexDefinitions))
	for _, definition := range manifest.IndexDefinitions {
		definitions[definition.Name] = definition
	}
	if len(definitions) != len(manifest.Indexes) {
		t.Fatalf("index definitions=%d, index names=%d", len(definitions), len(manifest.Indexes))
	}
	for _, name := range manifest.Indexes {
		if _, ok := definitions[name]; !ok {
			t.Errorf("manifest has no definition for index %q", name)
		}
	}
	quality := definitions["idx_points_quality_metric"]
	if quality.Table != "metric_points" || quality.AccessMethod != "btree" || quality.Predicate == "" || len(quality.Keys) != 2 {
		t.Errorf("quality index definition is incomplete: %+v", quality)
	}
	wantColumns := map[string]string{
		"health_records":    "processing_status",
		"metric_points":     "source_snapshot_at",
		"daily_scores":      "sleep_unspecified",
		"naive_baselines":   "reason",
		"chip_calibrations": "status",
	}
	for table, column := range wantColumns {
		if !containsString(manifest.Columns[table], column) {
			t.Errorf("manifest does not declare column %s.%s", table, column)
		}
	}
	if containsString(manifest.Tables, TenantIdentityTable) {
		t.Fatal("permanent identity marker must remain separate from the application schema manifest")
	}
	wantDefinitions := map[string]string{
		"naive_baselines.reason":   "text:true",
		"chip_calibrations.status": "text:false",
		"chip_calibrations.cutoff": "real:true",
	}
	for _, definition := range manifest.ColumnDefinitions {
		key := definition.Table + "." + definition.Column
		if want, ok := wantDefinitions[key]; ok {
			got := definition.DataType + ":" + strconv.FormatBool(definition.Nullable)
			if got != want {
				t.Errorf("definition %s = %s, want %s", key, got, want)
			}
			delete(wantDefinitions, key)
		}
	}
	if len(wantDefinitions) != 0 {
		t.Fatalf("missing readiness column definitions: %v", wantDefinitions)
	}
	arrayTypes := map[string]string{}
	for _, definition := range manifest.ColumnDefinitions {
		if definition.DataType == "ARRAY" {
			arrayTypes[definition.Table+"."+definition.Column] = definition.UDTName
		}
	}
	for _, column := range []string{"daily_scores.stress_flags", "energy_snapshots.flags"} {
		if arrayTypes[column] != "_text" {
			t.Errorf("array element type %s = %q, want _text", column, arrayTypes[column])
		}
	}
	if len(manifest.RequiredRows) != 1 || manifest.RequiredRows[0].Table != "source_epochs" || manifest.RequiredRows[0].Values["epoch_id"] != InitialSourceEpoch || manifest.RequiredRows[0].Values["start_date"] != "2014-01-01" || manifest.RequiredRows[0].Values["confirmed"] != "true" {
		t.Fatalf("bootstrap row invariant missing from manifest: %+v", manifest.RequiredRows)
	}
}

func TestSchemaContractManifestReturnsDefensiveCopies(t *testing.T) {
	first := SchemaContractManifest()
	first.Tables[0] = "mutated"
	first.Columns["health_records"][0] = "mutated"
	first.IndexDefinitions[0].Keys[0] = "mutated"
	first.ColumnDefinitions[0].Column = "mutated"
	first.Constraints[0].Columns[0] = "mutated"
	first.RequiredRows[0].Values["epoch_id"] = "mutated"
	second := SchemaContractManifest()
	if containsString(second.Tables, "mutated") || containsString(second.Columns["health_records"], "mutated") || second.IndexDefinitions[0].Keys[0] == "mutated" || second.ColumnDefinitions[0].Column == "mutated" || second.Constraints[0].Columns[0] == "mutated" || second.RequiredRows[0].Values["epoch_id"] == "mutated" {
		t.Fatal("schema contract manifest exposed mutable package state")
	}
}

func TestSchemaContractChecksumIncludesDefinitionsAndBootstrapRows(t *testing.T) {
	baseline := SchemaContractChecksum()
	originalIndex := schemaContract.IndexDefinitions[0]
	originalDefinition := schemaContract.ColumnDefinitions[0]
	originalConstraint := schemaContract.Constraints[0]
	originalRow := schemaContract.RequiredRows[0]
	originalValues := make(map[string]string, len(originalRow.Values))
	for key, value := range originalRow.Values {
		originalValues[key] = value
	}
	t.Cleanup(func() {
		schemaContract.IndexDefinitions[0] = originalIndex
		schemaContract.ColumnDefinitions[0] = originalDefinition
		schemaContract.Constraints[0] = originalConstraint
		originalRow.Values = originalValues
		schemaContract.RequiredRows[0] = originalRow
	})
	schemaContract.IndexDefinitions[0].Keys = []string{"wrong_column"}
	if got := SchemaContractChecksum(); got == baseline {
		t.Fatal("checksum ignored an index definition")
	}
	schemaContract.IndexDefinitions[0] = originalIndex
	schemaContract.ColumnDefinitions[0].Nullable = !originalDefinition.Nullable
	if got := SchemaContractChecksum(); got == baseline {
		t.Fatal("checksum ignored a type/nullability invariant")
	}
	schemaContract.ColumnDefinitions[0] = originalDefinition
	schemaContract.ColumnDefinitions[0].UDTName = "wrong_udt"
	if got := SchemaContractChecksum(); got == baseline {
		t.Fatal("checksum ignored an array/user-defined catalog type invariant")
	}
	schemaContract.ColumnDefinitions[0] = originalDefinition
	schemaContract.Constraints[0].Columns = []string{"wrong_column"}
	if got := SchemaContractChecksum(); got == baseline {
		t.Fatal("checksum ignored a primary/unique constraint")
	}
	schemaContract.Constraints[0] = originalConstraint
	schemaContract.RequiredRows[0].Values["epoch_id"] = originalValues["epoch_id"] + "_changed"
	if got := SchemaContractChecksum(); got == baseline {
		t.Fatal("checksum ignored a required bootstrap-row invariant")
	}
	originalRow.Values = originalValues
	schemaContract.RequiredRows[0] = originalRow
	if got := SchemaContractChecksum(); got != baseline {
		t.Fatalf("checksum did not return to baseline after restoring manifest: %s", got)
	}
}

func TestCanonicalCatalogExpression(t *testing.T) {
	got := canonicalCatalogExpression(`(status = ANY (ARRAY['reserved'::text, 'prompted'::text]))`)
	want := canonicalCatalogExpression(`status = any (array['reserved','prompted'])`)
	if got != want {
		t.Fatalf("canonical predicate = %q, want %q", got, want)
	}
	if got := canonicalCatalogExpression(`SUBSTRING("date", 1, 10)`); got != "substring(date,1,10)" {
		t.Fatalf("canonical expression = %q", got)
	}
	if got := canonicalCatalogExpression(`QUALITY = 'OK'`); got != `quality = 'OK'` {
		t.Fatalf("string literal case changed: %q", got)
	}
	if canonicalCatalogExpression(`quality = 'OK'`) == canonicalCatalogExpression(`quality = 'ok'`) {
		t.Fatal("case-sensitive string literals canonicalized as equal")
	}
	if got := canonicalCatalogExpression(`"Date"`); got != `"Date"` {
		t.Fatalf("case-sensitive quoted identifier changed: %q", got)
	}
}

func TestValidateSchemaContractTransition(t *testing.T) {
	v1, v2, v3 := 1, 2, 3
	c1, c2, c3 := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	for name, test := range map[string]struct {
		version        *int
		checksum       *string
		targetVersion  int
		targetChecksum string
		wantErr        bool
	}{
		"legacy to v2":                    {nil, nil, v2, c2, false},
		"v1 to v2":                        {&v1, &c1, v2, c2, false},
		"exact v2":                        {&v2, &c2, v2, c2, false},
		"downgrade":                       {&v3, &c3, v2, c2, true},
		"same version different checksum": {&v2, &c1, v2, c2, true},
		"partial":                         {&v1, nil, v2, c2, true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSchemaContractTransition(test.version, test.checksum, test.targetVersion, test.targetChecksum); (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
