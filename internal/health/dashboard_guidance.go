package health

// ResolveDashboardTodayGuidance turns the final EnergyBank verdict and its
// confidence context into one conservative dashboard action. It never
// promotes an existing verdict.
func ResolveDashboardTodayGuidance(
	energy *EnergyBank,
	readiness *ReadinessServingState,
	illness *IllnessSuspicion,
	sleep *SleepQualityBreakdown,
	ls LangStrings,
) *DashboardTodayGuidance {
	if energy == nil || energy.ActionVerdict == "" {
		return nil
	}
	if ls == nil {
		ls = GetStrings("en")
	}

	action := energy.ActionVerdict
	confidence := ReadinessConfidenceFinal
	reason := energy.VerdictReason
	reasonLocked := false

	if illness != nil {
		switch illness.Confidence {
		case IllnessConfidenceHigh:
			action = conservativeAction(action, "rest")
			confidence = ReadinessConfidenceLow
			reasonLocked = true
		case IllnessConfidenceModerate:
			action = conservativeAction(action, "active_recovery")
			confidence = ReadinessConfidenceProvisional
			reasonLocked = true
		}
	}

	if !readinessSupportsFullAction(readiness) {
		if action == "push_hard" {
			action = "moderate"
		}
		if readinessIsLowConfidence(readiness) {
			confidence = ReadinessConfidenceLow
		} else if confidence == ReadinessConfidenceFinal {
			confidence = ReadinessConfidenceProvisional
		}
		if !reasonLocked {
			reason = ls["dashboard_guidance_reason_readiness_pending"]
		}
	}

	if sleep == nil || sleep.Confidence != SleepQualityConfidenceFinal {
		if action == "push_hard" {
			action = "moderate"
		}
		switch {
		case sleep == nil || sleep.Confidence == SleepQualityConfidenceMissing || sleep.Confidence == SleepQualityConfidenceLow:
			confidence = ReadinessConfidenceLow
			if !reasonLocked {
				reason = ls["dashboard_guidance_reason_sleep_low"]
			}
		case confidence == ReadinessConfidenceFinal:
			confidence = ReadinessConfidenceProvisional
			if !reasonLocked {
				reason = ls["dashboard_guidance_reason_sleep_partial"]
			}
		}
	}

	label := BuildVerdictLabel(action, ls)
	summary := ls["dashboard_guidance_summary_"+action]
	if label == "" {
		label = action
	}
	if summary == "" {
		summary = label
	}
	if reason == "" {
		reason = summary
	}

	return &DashboardTodayGuidance{
		Action:     action,
		Label:      label,
		Summary:    summary,
		Reason:     reason,
		Confidence: confidence,
	}
}

func conservativeAction(current, cap string) string {
	if verdictRank(current) > verdictRank(cap) {
		return cap
	}
	return current
}

func readinessSupportsFullAction(readiness *ReadinessServingState) bool {
	return readiness != nil &&
		readiness.Status == ReadinessServingFresh &&
		readiness.Confidence == ReadinessConfidenceFinal
}

func readinessIsLowConfidence(readiness *ReadinessServingState) bool {
	if readiness == nil {
		return true
	}
	return readiness.Confidence == ReadinessConfidenceLow ||
		readiness.Status == ReadinessServingMissing ||
		readiness.Status == ReadinessServingStale ||
		readiness.Status == ReadinessServingLowCoverage
}
