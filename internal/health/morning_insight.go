package health

import (
	"sort"
	"strings"
)

const maxMorningInsightReasons = 3

type rankedMorningReason struct {
	MorningInsightReason
	priority int
	order    int
}

// MorningInsightOptions controls report-specific evidence exclusions before
// ranking and the three-reason cap are applied.
type MorningInsightOptions struct {
	ExcludeSections map[string]bool
}

// BuildMorningInsightEvidence reduces the full rule-based briefing to the
// small, deterministic contract sent to an AI provider. It deliberately keeps
// the final verdict and action outside model control.
func BuildMorningInsightEvidence(briefing *BriefingResponse, raw *RawMetrics) MorningInsightEvidence {
	return BuildMorningInsightEvidenceWithOptions(briefing, raw, MorningInsightOptions{})
}

// BuildMorningInsightEvidenceWithOptions applies exclusions before selecting
// the top reasons, allowing a freshness-aware renderer to refill the list from
// lower-ranked eligible evidence instead of filtering an already-capped list.
func BuildMorningInsightEvidenceWithOptions(briefing *BriefingResponse, raw *RawMetrics, options MorningInsightOptions) MorningInsightEvidence {
	if briefing == nil {
		return MorningInsightEvidence{}
	}
	evidence := MorningInsightEvidence{
		Date: briefing.Date,
		Readiness: MorningReadinessContext{
			Score:      briefing.ReadinessToday,
			RawScore:   briefing.ReadinessRawScore,
			Label:      briefing.ReadinessTodayLabel,
			Confidence: briefing.ReadinessConfidence,
			CapReason:  briefing.ReadinessCapReason,
		},
	}
	if raw != nil {
		evidence.Daily = raw.Daily
	}
	for _, section := range briefing.Sections {
		if options.ExcludeSections[section.Key] {
			continue
		}
		evidence.Sections = append(evidence.Sections, MorningInsightSection{
			Key:     section.Key,
			Status:  section.Status,
			Summary: section.Summary,
			Details: append([]BriefingDetail(nil), section.Details...),
		})
	}
	if briefing.ReadinessServing != nil {
		evidence.Readiness.Status = briefing.ReadinessServing.Status
		if evidence.Readiness.Confidence == "" {
			evidence.Readiness.Confidence = briefing.ReadinessServing.Confidence
		}
	}
	if briefing.EnergyBank != nil {
		copyEnergy := *briefing.EnergyBank
		evidence.EnergyBank = &copyEnergy
		evidence.Verdict = copyEnergy.ActionVerdict
		evidence.VerdictLabel = copyEnergy.VerdictLabel
		evidence.VerdictReason = copyEnergy.VerdictReason
	}
	if briefing.TodayGuidance != nil {
		evidence.Verdict = firstNonEmpty(briefing.TodayGuidance.Action, evidence.Verdict)
		evidence.VerdictLabel = firstNonEmpty(briefing.TodayGuidance.Label, evidence.VerdictLabel)
		evidence.VerdictReason = firstNonEmpty(briefing.TodayGuidance.Reason, evidence.VerdictReason)
		evidence.Action = firstNonEmpty(briefing.TodayGuidance.Summary, briefing.ReadinessTip)
	} else {
		evidence.Action = briefing.ReadinessTip
	}
	if evidence.Action == "" {
		evidence.Action = evidence.VerdictReason
	}

	var candidates []rankedMorningReason
	order := 0
	add := func(key, section, severity, text string, priority int) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		candidates = append(candidates, rankedMorningReason{
			MorningInsightReason: MorningInsightReason{
				Key: key, Section: section, Severity: severity, Text: text,
			},
			priority: priority,
			order:    order,
		})
		order++
	}

	for i, alert := range briefing.Alerts {
		priority := 90
		if alert.Severity == "critical" {
			priority = 110
		}
		add("alert:"+alert.Metric+":"+itoaSmall(i), "alert", alert.Severity, alert.Text, priority)
	}
	if briefing.Headline != nil {
		priority := 85
		switch briefing.Headline.Severity {
		case "critical":
			priority = 105
		case "warning":
			priority = 95
		case "info":
			priority = 85
		}
		add("headline", "headline", briefing.Headline.Severity,
			firstNonEmpty(briefing.Headline.Detail, briefing.Headline.Title), priority)
	}

	for _, section := range briefing.Sections {
		priority := 20
		switch section.Status {
		case "low":
			priority = 75
		case "fair":
			priority = 55
		case "good":
			priority = 25
		}
		if section.Key == "sleep" || section.Key == "recovery" {
			priority += 25
		}
		add("section:"+section.Key, section.Key, section.Status, section.Summary, priority)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority == candidates[j].priority {
			return candidates[i].order < candidates[j].order
		}
		return candidates[i].priority > candidates[j].priority
	})
	seenSections := make(map[string]bool)
	seenText := make(map[string]bool)
	for _, candidate := range candidates {
		if options.ExcludeSections[candidate.Section] {
			continue
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(candidate.Text), " "))
		if seenSections[candidate.Section] || seenText[normalized] {
			continue
		}
		evidence.Reasons = append(evidence.Reasons, candidate.MorningInsightReason)
		seenSections[candidate.Section] = true
		seenText[normalized] = true
		if len(evidence.Reasons) == maxMorningInsightReasons {
			break
		}
	}
	return evidence
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func itoaSmall(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return "many"
}
