package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNamedContractStressHourCapture(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" {
		t.Skip("synthetic audit output not requested")
	}
	cases := []struct{ hour, baseline, sd, want float64 }{{70, 60, 5, 2}, {55, 60, 5, -1}, {60, 60, 0, 0}}
	rows := make([]map[string]any, 0, len(cases))
	for i, c := range cases {
		got := HourZShift(c.hour, c.baseline, c.sd)
		if got != c.want {
			t.Fatalf("capture %d: got %v want %v", i, got, c.want)
		}
		rows = append(rows, map[string]any{"metric_id": "stress_hour_z", "key": map[string]any{"capture": i}, "value": got, "source": "health.HourZShift", "profile": "synthetic"})
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stress-hour.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
