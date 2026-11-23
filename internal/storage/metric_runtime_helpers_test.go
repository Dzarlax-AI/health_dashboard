package storage

import (
	"encoding/json"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type metricRuntimeCapture struct {
	family, source string
	value          any
	key            map[string]any
}

func writeMetricRuntimeCapture(t *testing.T, dir, name string, captures ...metricRuntimeCapture) {
	t.Helper()
	rows := []map[string]any{}
	for _, capture := range captures {
		values, err := dbtmetrics.ProjectFamilyValues(capture.family, capture.value)
		if err != nil {
			t.Fatal(err)
		}
		var version any
		switch v := capture.value.(type) {
		case BankResult:
			version = v.FormulaVersion
		case EnergySnapshotPoint:
			version = v.FormulaVersion
		case *health.SleepDurationComparison:
			version = v.RuleVersion
		case *health.SleepGoal:
			version = v.Version
		}
		for id, value := range values {
			rows = append(rows, map[string]any{"metric_id": id, "key": capture.key, "profile": "synthetic", "value": value, "source": capture.source, "status": "captured", "version": version})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["metric_id"].(string) < rows[j]["metric_id"].(string) })
	body, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, name), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
