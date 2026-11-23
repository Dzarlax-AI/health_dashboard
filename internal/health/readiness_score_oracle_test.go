package health

import (
	"encoding/json"
	"os"
	"testing"
)

// The fixture captures the headline and illness classifiers at the point where
// they enter Readiness. Both caps run through the normal Go serving functions.
func TestReadinessScoreShadowOracle(t *testing.T) {
	input := os.Getenv("READINESS_SCORE_ORACLE_INPUT")
	if input == "" {
		t.Skip("local synthetic dbt comparison")
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID               string    `json:"case_id"`
		Reader           string    `json:"reader"`
		Captured         bool      `json:"captured"`
		HRV              []float64 `json:"hrv"`
		RHR              []float64 `json:"rhr"`
		Sleep            []float64 `json:"sleep"`
		HRVPresent       bool      `json:"hrv_present"`
		RHRPresent       bool      `json:"rhr_present"`
		SleepPresent     bool      `json:"sleep_present"`
		LowSleepQuality  bool      `json:"low_sleep_quality"`
		HRVProvisional   bool      `json:"hrv_provisional"`
		HRVSamples       int       `json:"hrv_samples"`
		OvernightPresent bool      `json:"overnight_present"`
		Headline         string    `json:"headline"`
		Illness          string    `json:"illness"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]map[string]any)
	ls := GetStrings("en")
	for _, c := range cases {
		row := map[string]any{"weighted_sum": nil, "available_weight": nil, "raw_score": nil, "display_score": nil, "band": nil, "confidence": nil, "cap_reason": nil}
		for _, field := range []string{"hrv_contribution", "rhr_contribution", "sleep_contribution"} {
			row[field] = nil
		}
		rows[c.ID] = row
		if !c.Captured {
			continue
		}
		d := RawMetrics{HRV: c.HRV, RHR: c.RHR, Sleep: c.Sleep}
		scored := d
		if c.Reader == "morning" {
			hrvConfidence := ReadinessConfidenceFinal
			if c.HRVProvisional {
				hrvConfidence = ReadinessConfidenceProvisional
			}
			d.ReadinessEvidence = &ReadinessEvidenceInput{
				HRV: ReadinessComponentEvidence{Present: c.HRVPresent, Confidence: hrvConfidence, SampleCount: c.HRVSamples},
				RHR: ReadinessComponentEvidence{Present: c.RHRPresent}, SleepDuration: ReadinessComponentEvidence{Present: c.SleepPresent},
				SleepQuality: ReadinessComponentEvidence{Present: c.LowSleepQuality, Confidence: ReadinessConfidenceLow},
				OvernightHR:  ReadinessComponentEvidence{Present: c.OvernightPresent},
			}
			resp := newReadinessBriefing(d, ls, nil)
			resp.Headline = &HeadlineSignal{Key: c.Headline}
			applyCoherencePass(resp, ls)
			resp.IllnessSuspicion = &IllnessSuspicion{Confidence: c.Illness}
			ApplyIllnessSafetyCap(resp, ls)
			row["raw_score"] = resp.ReadinessRawScore
			row["display_score"] = resp.ReadinessDisplayScore
			row["band"] = resp.ReadinessBand
			row["confidence"] = resp.ReadinessConfidence
			if resp.ReadinessCapReason != "" {
				row["cap_reason"] = resp.ReadinessCapReason
			}
			if !c.HRVPresent {
				scored.HRV = nil
			}
			if !c.RHRPresent {
				scored.RHR = nil
			}
			if !c.SleepPresent {
				scored.Sleep = nil
			}
		} else {
			score := ComputeReadinessScore(d.HRV, d.RHR, d.Sleep)
			row["raw_score"] = score
			row["display_score"] = score
			row["band"] = ReadinessBand(score)
		}
		row["weighted_sum"], row["available_weight"] = readinessWeightedInputs(scored)
		for label, component := range map[string]RawMetrics{
			"hrv_contribution":   {HRV: scored.HRV},
			"rhr_contribution":   {RHR: scored.RHR},
			"sleep_contribution": {Sleep: scored.Sleep},
		} {
			weighted, weight := readinessWeightedInputs(component)
			if weight > 0 {
				row[label] = weighted
			}
		}
	}
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("READINESS_SCORE_ORACLE_OUTPUT"), out, 0600); err != nil {
		t.Fatal(err)
	}
}
