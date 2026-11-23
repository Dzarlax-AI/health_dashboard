package health

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// Captured rows shared with the dbt comparison. The headline is an input to
// section coherence; the oracle derives it with the complete serving function.
func TestBriefingComponentsOracle(t *testing.T) {
	path := os.Getenv("BRIEFING_COMPONENTS_FIXTURE")
	if path == "" {
		t.Skip("local synthetic dbt pilot")
	}
	type input struct {
		Decision  BriefingResponse `json:"decision"`
		Zones     string           `json:"zones"`
		Samples   []HRSample       `json:"hr_samples"`
		End       time.Time        `json:"workout_end"`
		Raw       RawMetrics       `json:"raw"`
		HourZ     []*float64       `json:"hour_z"`
		Threshold *float64         `json:"z_threshold"`
		Temp      float64          `json:"temp"`
		Resp      float64          `json:"resp"`
		Drop      float64          `json:"drop"`
		RHR       float64          `json:"rhr"`
		DayHR     float64          `json:"day_hr"`
		Walking   *float64         `json:"walking"`
	}
	var fixtures []input
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	rows := make([]map[string]any, 0, len(fixtures))
	for _, in := range fixtures {
		b := ComputeBriefing(in.Raw, "en")
		sections := map[string]string{}
		for _, sec := range b.Sections {
			sections[sec.Key] = sec.Status
		}
		correlation := b.Correlation
		if correlation == nil {
			correlation = []CorrelationPoint{}
		}
		trends := map[string]any{}
		for _, card := range b.MetricCards {
			trends[card.Metric] = []any{card.Trend7dPct, card.Trend7dStatus, card.Trend30dPct, card.Trend30dStatus}
		}
		headline := ""
		if b.Headline != nil {
			headline = b.Headline.Key
		}
		decision := BuildDailyDecision(&in.Decision)
		zones, err := ParseHRZones(in.Zones)
		if err != nil {
			t.Fatal(err)
		}
		threshold := 1.0
		if in.Threshold != nil {
			threshold = *in.Threshold
		}
		hourZ := make([]float64, len(in.HourZ))
		for i, h := range in.HourZ {
			if h == nil {
				hourZ[i] = math.NaN()
			} else {
				hourZ[i] = *h
			}
		}
		walking := ComputeWalkingHREligibility(WalkingHRRow{Value: in.Walking})
		rows = append(rows, map[string]any{
			"decision_mode": decision.Mode, "decision_domain": decision.EvidenceDomains[0], "weekly_sleep": computeSleepAnalysis(in.Raw), "zone_input": NewWorkoutZoneInput(in.Samples, in.End, zones), "zone_seconds": ComputeTimeInZones(in.Samples, in.End, zones), "headline": headline, "trends": trends, "sections": sections, "overall": b.Overall, "correlation": correlation,
			"load": SustainedHRLoad(hourZ, threshold), "acute": AcuteStress(hourZ), "sustained": SustainedLoadFlag(hourZ),
			"illness_signature": IllnessSignature(in.Temp, in.Resp, in.Drop), "recovery_debt": RecoveryDebt(in.Drop, in.RHR),
			"rebound": ParasympatheticRebound(in.DayHR, in.Drop), "walking_reason": walking.EligibilityReason,
		})
	}
	if os.Getenv("HEALTH_ARCHIVE_ENERGY_ORACLE") == "1" {
		for i, in := range fixtures {
			b := ComputeBriefing(in.Raw, "en")
			rows[i]["readiness_score"] = b.ReadinessScore
			if e := b.EnergyBank; e != nil {
				rows[i]["legacy_energy"] = map[string]any{"capacity": e.Capacity, "current": e.Current,
					"drained": e.DrainSoFar, "strain": e.Strain, "stress": e.Stress,
					"hrv_z": e.HRVZRaw, "verdict": e.ActionVerdict}
			}
		}
	}
	result, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".expected", result, 0600); err != nil {
		t.Fatal(err)
	}
}
