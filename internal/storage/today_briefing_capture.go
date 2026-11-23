package storage

import (
	"encoding/json"

	"health-receiver/internal/health"
)

// Capture only the measured fields consumed by the catalog and replay models.
// computeBriefing has already applied both the headline and final illness caps.
// The solver's earlier receipt retains its original evidence and cutoff.
func todayBriefingCapture(b *health.BriefingResponse) (json.RawMessage, json.RawMessage, error) {
	d := b.RawMetrics
	if d == nil {
		return nil, nil, nil
	}
	headline, illness := "", ""
	decision := map[string]any{}
	if b.Headline != nil {
		headline = b.Headline.Key
	}
	if b.IllnessSuspicion != nil {
		illness = b.IllnessSuspicion.Confidence
		decision["illness_suspicion"] = map[string]any{"signals": b.IllnessSuspicion.Signals}
	}
	if b.Sleep != nil {
		decision["sleep"] = map[string]any{"latest_total": b.Sleep.LatestTotal}
	}
	if b.EnergyBank != nil {
		decision["energy_bank"] = map[string]any{"flags": b.EnergyBank.Flags}
	}
	inputs, err := json.Marshal(map[string]any{"reader": "morning", "effective": morningReadinessInput(d),
		"headline": headline, "final_illness": illness,
		"raw": map[string]any{"ReadinessEvidence": d.ReadinessEvidence}, "decision": decision})
	if err != nil {
		return nil, nil, err
	}
	observed, err := json.Marshal(map[string]any{"raw_score": b.ReadinessRawScore, "display_score": b.ReadinessDisplayScore,
		"score": b.ReadinessScore, "today": b.ReadinessToday, "recovery_pct": b.RecoveryPct, "recovery_source": b.RecoverySource,
		"band": b.ReadinessBand, "today_band": b.ReadinessTodayBand, "confidence": b.ReadinessConfidence, "cap_reason": b.ReadinessCapReason,
		"quality": b.SleepQuality})
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(inputs), json.RawMessage(observed), nil
}
