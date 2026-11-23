package health

import (
	"encoding/json"
	"os"
	"testing"
)

// TestReadinessHRVOracle is an opt-in synthetic comparison adapter. The
// calculation itself is the same helper used by computeReadiness.
func TestReadinessHRVOracle(t *testing.T) {
	input, output := os.Getenv("READINESS_HRV_ORACLE_INPUT"), os.Getenv("READINESS_HRV_ORACLE_OUTPUT")
	if input == "" && output == "" {
		t.Skip("synthetic oracle not requested")
	}
	if input == "" || output == "" {
		t.Fatal("both oracle paths are required")
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		SnapshotID      string    `json:"snapshot_id"`
		TargetDate      string    `json:"target_date"`
		Reader          string    `json:"reader"`
		SnapshotState   string    `json:"snapshot_state"`
		EvidencePresent bool      `json:"evidence_present"`
		Dates           []string  `json:"dates"`
		Values          []float64 `json:"values"`
		RHR             []float64 `json:"rhr"`
		Sleep           []float64 `json:"sleep"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	results := make([]map[string]any, 0, len(cases))
	for _, c := range cases {
		if len(c.Dates) != len(c.Values) {
			t.Fatalf("%s: mismatched dates and values", c.SnapshotID)
		}
		dates, values := c.Dates, c.Values
		if dates == nil {
			dates = []string{}
		}
		if values == nil {
			values = []float64{}
		}
		row := map[string]any{"snapshot_id": c.SnapshotID, "selected_dates": dates, "selected_values": values, "n_values": len(values), "available": false, "skip_reason": nil, "current_ms": nil, "recent7_mean_ms": nil, "baseline_mean_ms": nil, "baseline_sd_ms": nil, "z_today": nil, "z_trend": nil, "blended_z": nil, "component_weight": nil, "weighted_z": nil, "score": nil}
		reason := ""
		switch {
		case c.SnapshotState == "unrecoverable":
			reason = "unrecoverable_snapshot"
		case c.SnapshotState != "captured" || (c.Reader != "history" && c.Reader != "morning"):
			reason = "invalid_snapshot"
		case c.Reader == "history" && (len(dates) == 0 || dates[0] != c.TargetDate):
			reason = "missing_same_day_value"
		case c.Reader == "morning" && !c.EvidencePresent:
			reason = "missing_same_day_evidence"
		case len(values) < minBaseline+2:
			reason = "insufficient_observations"
		}
		if reason != "" {
			row["skip_reason"] = reason
		} else {
			h, ok := computeReadinessHRV(values)
			if !ok {
				t.Fatal("unexpected unavailable component")
			}
			row["available"] = true
			row["current_ms"] = h.today
			row["recent7_mean_ms"] = h.recentMean
			row["baseline_mean_ms"] = h.baselineMean
			row["baseline_sd_ms"] = h.baselineSD
			row["z_today"] = h.zToday
			row["z_trend"] = h.zTrend
			row["blended_z"] = h.blended
			row["component_weight"] = wHRV
			row["weighted_z"] = h.blended * wHRV
		}
		if c.SnapshotState == "captured" {
			scoringValues := values
			if reason == "missing_same_day_value" || reason == "missing_same_day_evidence" || reason == "invalid_snapshot" {
				scoringValues = nil
			}
			row["score"] = ComputeReadinessScore(scoringValues, c.RHR, c.Sleep)
		}
		results = append(results, row)
	}
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, b, 0600); err != nil {
		t.Fatal(err)
	}
}
