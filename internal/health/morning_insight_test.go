package health

import "testing"

func TestBuildMorningInsightEvidenceKeepsCanonicalActionAndRanksReasons(t *testing.T) {
	briefing := &BriefingResponse{
		Date:                "2026-08-04",
		ReadinessToday:      65,
		ReadinessRawScore:   65,
		ReadinessTodayLabel: "Умеренно",
		ReadinessConfidence: ReadinessConfidenceFinal,
		ReadinessServing:    &ReadinessServingState{Status: ReadinessServingFresh},
		EnergyBank: &EnergyBank{
			Current:         67,
			Capacity:        81,
			ActionVerdict:   "moderate",
			VerdictLabel:    "Умеренная нагрузка",
			VerdictReason:   "Запас энергии достаточный, но готовность умеренная.",
			VerdictSeverity: "info",
		},
		TodayGuidance: &DashboardTodayGuidance{
			Action:  "moderate",
			Label:   "Умеренная нагрузка",
			Summary: "Обычная тренировка средней интенсивности.",
			Reason:  "Запас энергии достаточный, но готовность умеренная.",
		},
		Alerts: []Alert{
			{Metric: "hrv_cv", Severity: "warning", Text: "Вариативность HRV за 7 дней повышена."},
		},
		Headline: &HeadlineSignal{
			Severity: "info",
			Title:    "HRV выше обычного",
			Detail:   "51.8 мс — +37.4% от среднего.",
		},
		Sections: []BriefingSection{
			{Key: "sleep", Status: "fair", Summary: "7.0 часа, глубокий сон ниже цели."},
			{Key: "activity", Status: "low", Summary: "Вчерашняя активность была низкой."},
			{Key: "recovery", Status: "fair", Summary: "Восстановление умеренное."},
		},
	}
	raw := &RawMetrics{Daily: []DailyHealthMetrics{{Date: "2026-08-04"}}}

	got := BuildMorningInsightEvidence(briefing, raw)
	if got.Verdict != "moderate" {
		t.Fatalf("verdict = %q, want moderate", got.Verdict)
	}
	if got.Action != "Обычная тренировка средней интенсивности." {
		t.Fatalf("action = %q", got.Action)
	}
	if len(got.Reasons) != 3 {
		t.Fatalf("reasons = %#v, want exactly 3", got.Reasons)
	}
	if got.Reasons[0].Key != "alert:hrv_cv:0" {
		t.Fatalf("first reason = %#v, want warning alert", got.Reasons[0])
	}
	if got.Reasons[1].Section != "headline" {
		t.Fatalf("second reason = %#v, want headline evidence", got.Reasons[1])
	}
	if got.Reasons[2].Section != "sleep" {
		t.Fatalf("third reason = %#v, want sleep evidence", got.Reasons[2])
	}
}

func TestBuildMorningInsightEvidenceDeduplicatesSectionsAndText(t *testing.T) {
	briefing := &BriefingResponse{
		Date: "2026-08-04",
		Alerts: []Alert{
			{Metric: "hrv_cv", Severity: "critical", Text: "Recovery signal."},
			{Metric: "hrv_cv", Severity: "warning", Text: "Recovery signal."},
		},
		Sections: []BriefingSection{
			{Key: "sleep", Status: "low", Summary: "Sleep needs attention."},
			{Key: "sleep", Status: "fair", Summary: "Second sleep sentence."},
			{Key: "recovery", Status: "fair", Summary: "Recovery is moderate."},
		},
	}
	got := BuildMorningInsightEvidence(briefing, nil)
	if len(got.Reasons) != 3 {
		t.Fatalf("reasons = %#v, want 3 unique facts", got.Reasons)
	}
	seen := map[string]bool{}
	for _, reason := range got.Reasons {
		if seen[reason.Section] {
			t.Fatalf("section %q selected twice: %#v", reason.Section, got.Reasons)
		}
		seen[reason.Section] = true
	}
}

func TestBuildMorningInsightEvidenceUsesWithheldServerGuidance(t *testing.T) {
	briefing := &BriefingResponse{
		Date: "2026-08-04",
		EnergyBank: &EnergyBank{
			ActionVerdict: "push_hard",
			VerdictReason: "Energy is high.",
		},
		TodayGuidance: &DashboardTodayGuidance{
			Action:  "pending",
			Label:   "Waiting for data",
			Summary: "Wait for today's recovery data before choosing a workout.",
			Reason:  "Recovery evidence is incomplete.",
		},
	}
	got := BuildMorningInsightEvidence(briefing, nil)
	if got.Verdict != "pending" {
		t.Fatalf("verdict = %q, want server guidance pending", got.Verdict)
	}
	if got.Action != "Wait for today's recovery data before choosing a workout." {
		t.Fatalf("action = %q", got.Action)
	}
}

func TestBuildMorningInsightEvidenceAppliesExclusionsBeforeReasonCap(t *testing.T) {
	briefing := &BriefingResponse{
		Alerts: []Alert{{Metric: "hrv_cv", Severity: "warning", Text: "Alert."}},
		Headline: &HeadlineSignal{
			Severity: "info",
			Detail:   "Headline.",
		},
		Sections: []BriefingSection{
			{Key: "sleep", Status: "low", Summary: "Stale sleep."},
			{Key: "recovery", Status: "fair", Summary: "Recovery."},
			{Key: "activity", Status: "good", Summary: "Activity."},
		},
	}
	got := BuildMorningInsightEvidenceWithOptions(briefing, nil, MorningInsightOptions{
		ExcludeSections: map[string]bool{"sleep": true},
	})
	if len(got.Reasons) != 3 {
		t.Fatalf("reasons = %#v, want refilled top 3", got.Reasons)
	}
	for _, reason := range got.Reasons {
		if reason.Section == "sleep" {
			t.Fatalf("excluded sleep reason selected: %#v", got.Reasons)
		}
	}
	if got.Reasons[2].Section != "recovery" {
		t.Fatalf("third reason = %#v, want recovery refill", got.Reasons[2])
	}
}
