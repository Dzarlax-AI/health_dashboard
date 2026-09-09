package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DailyDecision is the deterministic action boundary for a day. AI may
// explain this decision, but must not choose a competing intensity on its own.
// ID changes only when inputs relevant to that boundary change.
type DailyDecision struct {
	ID         string   `json:"id"`
	Mode       string   `json:"mode"`
	Label      string   `json:"label"`
	Reason     string   `json:"reason"`
	SignalKeys []string `json:"signal_keys,omitempty"`
}

// BuildDailyDecision must run after the final EnergyBank has been attached to
// the briefing. The bank is the authoritative action guardrail; readiness is
// a safe fallback for installations without an EnergyBank snapshot yet.
func BuildDailyDecision(resp *BriefingResponse) *DailyDecision {
	if resp == nil {
		return nil
	}
	decision := &DailyDecision{
		Mode:   fallbackDecisionMode(resp.ReadinessToday),
		Label:  resp.ReadinessTodayLabel,
		Reason: resp.ReadinessTip,
	}
	if resp.EnergyBank != nil {
		decision.Mode = resp.EnergyBank.ActionVerdict
		if resp.EnergyBank.VerdictLabel != "" {
			decision.Label = resp.EnergyBank.VerdictLabel
		}
		if resp.EnergyBank.VerdictReason != "" {
			decision.Reason = resp.EnergyBank.VerdictReason
		}
	}
	// TodayGuidance is newer than EnergyBank and applies additional illness,
	// readiness-serving and sleep-confidence safety caps. It is the final
	// authoritative action whenever it is available.
	if resp.TodayGuidance != nil {
		decision.Mode = resp.TodayGuidance.Action
		decision.Label = resp.TodayGuidance.Label
		decision.Reason = resp.TodayGuidance.Reason
	}
	if resp.Headline != nil && resp.Headline.Key != "" {
		decision.SignalKeys = []string{resp.Headline.Key}
	}
	decision.ID = dailyDecisionID(resp, decision)
	return decision
}

func fallbackDecisionMode(score int) string {
	switch {
	case score >= 80:
		return "moderate"
	case score >= 50:
		return "active_recovery"
	default:
		return "rest"
	}
}

func dailyDecisionID(resp *BriefingResponse, decision *DailyDecision) string {
	headline := ""
	if resp.Headline != nil {
		parts := make([]string, 0, len(resp.Headline.Metrics))
		for _, metric := range resp.Headline.Metrics {
			parts = append(parts, fmt.Sprintf("%s:%.4f:%.4f", metric.Metric, metric.Value, metric.Baseline))
		}
		headline = strings.Join(append([]string{resp.Headline.Key, resp.Headline.Severity}, parts...), "|")
	}
	parts := []string{
		"daily-decision-v1", resp.Date, decision.Mode, decision.Reason,
		resp.ReadinessConfidence, resp.ReadinessCapReason, headline,
	}
	if resp.SubjectiveCheckin != nil {
		parts = append(parts, resp.SubjectiveCheckin.Status, resp.SubjectiveCheckin.Answer)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}
