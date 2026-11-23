package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Read the rows persisted by the four real writers. A missing optional JSON key
// represents a missing value only if the writer declares that key with omitempty.
func TestMetricSchemaFeatureSnapshots(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" {
		t.Skip("requires synthetic catalog runner")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	const date = "2026-04-20"
	anchor, _ := time.Parse(isoDate, date)
	seedRecoveryHistory(t, db, date, 60, .93, InitialSourceEpoch)
	seedSteadyHistory(t, db, date, 60)
	for i := -7; i <= 14; i++ {
		day := anchor.AddDate(0, 0, i).Format(isoDate)
		seedSleepRow(t, db, day, fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
		seedWalkingHRRow(t, db, day, 85)
		if i > 0 {
			seedRecoveryRolling3d(t, db, day, fp(.93), InitialSourceEpoch)
			seedAcuteOrEvent(t, db, day, 0, InitialSourceEpoch)
			seedAutonomicRow(t, db, day, fp(45), fp(60))
		}
	}
	for _, backfill := range []func(string, string) (int, error){db.BackfillRecoveryStabilitySnapshots,
		db.BackfillPassiveEfficiencySnapshots, db.BackfillAcuteRiskSnapshots, db.BackfillChronicLoadSnapshots} {
		if _, err := backfill(date, date); err != nil {
			t.Fatal(err)
		}
	}
	var catalog struct {
		Models []struct {
			Config struct {
				Meta struct {
					HealthCatalog struct {
						Families map[string]struct {
							Fields map[string]struct{ Field string }
						}
						Contracts map[string]json.RawMessage
					} `json:"health_catalog"`
				}
			}
		}
	}
	payload, err := os.ReadFile("../../experiments/dbt_metrics/models/health_metric_catalog.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(payload, &catalog); err != nil {
		t.Fatal(err)
	}
	spec := catalog.Models[0].Config.Meta.HealthCatalog
	type sample struct {
		ID      string         `json:"metric_id"`
		Key     map[string]any `json:"key"`
		Profile string         `json:"profile"`
		Value   any            `json:"value"`
		Source  string         `json:"source"`
		Version any            `json:"version"`
	}
	var samples []sample
	emit := func(id string, key map[string]any, value any, source string, version any) {
		samples = append(samples, sample{id, key, "readiness-writer-fixture", value, source, version})
	}
	types := map[string]reflect.Type{"recovery_stability": reflect.TypeOf(recoveryFeatures{}),
		"passive_efficiency": reflect.TypeOf(passiveEfficiencyFeatures{}), "acute_risk": reflect.TypeOf(acuteRiskFeatures{}),
		"chronic_load": reflect.TypeOf(chronicLoadFeatures{}), "sleep_architecture_features": reflect.TypeOf(SleepArchitectureFeatureFields{})}
	for family, typ := range types {
		score := family
		if family == "sleep_architecture_features" {
			score = "recovery_stability"
		}
		var values map[string]any
		var epoch string
		var version int
		err := db.pool.QueryRow(t.Context(), "select features,source_epoch,feature_version from feature_snapshots where date=$1 and sub_score=$2", date, score).Scan(&values, &epoch, &version)
		if err != nil {
			t.Fatal(err)
		}
		key := map[string]any{"date": date, "sub_score": score, "source_epoch": epoch}
		for name, field := range spec.Families[family].Fields {
			f, ok := typ.FieldByName(field.Field)
			if !ok {
				t.Fatalf("%s.%s: writer field missing", family, name)
			}
			tag := f.Tag.Get("json")
			column := strings.Split(tag, ",")[0]
			value, found := values[column]
			if column == "" || (!found && !strings.Contains(tag, ",omitempty")) {
				t.Fatalf("%s.%s: missing field %s", family, name, column)
			}
			emit(family+"."+name, key, value, "feature_snapshots.features."+column, version)
		}
	}
	for _, dataset := range []struct{ family, table, query string }{
		{"target", "target_snapshots", "select to_jsonb(t) from target_snapshots t where date=$1 order by sub_score,target_kind"},
		{"naive_baseline", "naive_baselines", "select to_jsonb(t) from naive_baselines t where date=$1 order by sub_score,target_kind,baseline_kind"},
	} {
		rows, err := db.pool.Query(t.Context(), dataset.query, date)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var record map[string]any
			if err := rows.Scan(&record); err != nil {
				t.Fatal(err)
			}
			key := map[string]any{"date": date, "sub_score": record["sub_score"], "target_kind": record["target_kind"], "source_epoch": record["source_epoch"]}
			if dataset.family == "naive_baseline" {
				key["baseline_kind"] = record["baseline_kind"]
			}
			for col := range spec.Families[dataset.family].Fields {
				value, ok := record[col]
				if !ok {
					t.Fatalf("%s missing column %s", dataset.table, col)
				}
				emit(dataset.family+"."+col, key, value, dataset.table+"."+col, record["formula_version"])
			}
			if dataset.family == "target" {
				id := record["sub_score"].(string) + ".target_" + record["target_kind"].(string)
				if _, ok := spec.Contracts[id]; ok {
					emit(id, key, record["target_value"], "target_snapshots.target_value", record["formula_version"])
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	data, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "features.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
