package health

// MorningRecoveryContext uses today's admitted readiness and component facts.
// The legacy recovery section describes rolling averages, not today's state.
func MorningRecoveryContext(b *BriefingResponse, lang string) (summary string, facts []string, ok bool) {
	if b == nil {
		return "", nil, false
	}
	snapshot := BuildDailyInsightSnapshot(b, lang)
	context := BuildInsightFactContext(snapshot)
	if !insightContextDomainFinal(context, "recovery") {
		return "", nil, false
	}
	hasScore := false
	for _, fact := range context.Facts {
		switch fact.ID {
		case "readiness_current":
			hasScore = true
		case "readiness_hrv_current", "readiness_rhr_current":
			facts = append(facts, fact.Statement)
		}
	}
	if !hasScore {
		return "", nil, false
	}
	for _, domain := range snapshot.Domains {
		if domain.Key == "recovery" {
			return domain.Summary, facts, true
		}
	}
	return "", nil, false
}
