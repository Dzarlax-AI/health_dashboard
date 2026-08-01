package ui

import (
	"net/http/httptest"
	"strings"
	"testing"

	"health-receiver/internal/health"
)

func TestDashboardIllnessSuspicionPanelVisibility(t *testing.T) {
	moderate := renderDashboardIllnessForTest(t, &health.IllnessSuspicion{
		Confidence: "moderate",
		Signals: []health.IllnessEvidenceSignal{
			{Metric: "respiratory_rate", Status: "ok", Strength: "strong", Contributes: true},
			{Metric: "resting_heart_rate", Status: "ok", Strength: "mild", Contributes: true},
			{Metric: "stress_flags", Status: "ok", Strength: "mild", Contributes: false},
		},
	})

	for _, want := range []string{
		"illness-suspicion-panel",
		"Возможные признаки болезни",
		"Данные носимого устройства показывают сходящиеся признаки",
		"Частота дыхания",
		"Пульс покоя",
	} {
		if !strings.Contains(moderate, want) {
			t.Fatalf("moderate illness panel missing %q", want)
		}
	}
	if strings.Contains(moderate, "Стресс-флаги") {
		t.Fatalf("non-contributing signals should not render as dashboard chips")
	}

	low := renderDashboardIllnessForTest(t, &health.IllnessSuspicion{
		Confidence: "low",
		Signals:    []health.IllnessEvidenceSignal{{Metric: "respiratory_rate", Status: "ok", Strength: "mild", Contributes: true}},
	})
	if strings.Contains(low, "Возможные признаки болезни") {
		t.Fatalf("low confidence illness suspicion should not render the dashboard panel")
	}
}

func TestDashboardIllnessSuspicionAutonomicProdromeCopy(t *testing.T) {
	html := renderDashboardIllnessForTest(t, &health.IllnessSuspicion{
		Confidence: "moderate",
		Pattern:    health.IllnessPatternAutonomicProdrome,
		Signals: []health.IllnessEvidenceSignal{
			{Metric: "autonomic_prodrome", Kind: "autonomic_pattern", Status: "ok", Strength: "strong", Contributes: true},
			{Metric: "sustained_hr_load", Status: "ok", Strength: "strong", Contributes: false},
		},
	})
	for _, want := range []string{
		"Будьте внимательны к себе",
		"повышенная автономная нагрузка",
		"Паттерн автономной нагрузки",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("autonomic prodrome panel missing %q", want)
		}
	}
	if strings.Contains(html, "Возможные признаки болезни") {
		t.Fatalf("autonomic prodrome panel should use softer title")
	}
}

func TestDashboardReadinessServingBadges(t *testing.T) {
	tests := []struct {
		name        string
		serving     *health.ReadinessServingState
		want        []string
		wantMissing []string
	}{
		{
			name:    "fresh",
			serving: readinessServingForTest(health.ReadinessServingFresh, health.ReadinessConfidenceFinal, ""),
			want: []string{
				"readiness-trust-badge--fresh",
				"Fresh data",
				"Core coverage: 3/3 fresh signals.",
			},
			wantMissing: []string{`class="today-hero status-good readiness-low-confidence"`, `<div class="readiness-confidence-note">`},
		},
		{
			name:    "data accruing",
			serving: readinessServingForTest(health.ReadinessServingDataAccruing, health.ReadinessConfidenceProvisional, "hrv_provisional"),
			want: []string{
				"readiness-trust-badge--pending",
				"data-tooltip=",
				"Still settling",
				"signals are still settling",
				"fewer than 4 usable samples",
			},
		},
		{
			name:    "missing",
			serving: readinessServingForTest(health.ReadinessServingMissing, health.ReadinessConfidenceProvisional, "missing_same_day_evidence"),
			want: []string{
				`class="today-hero status-good readiness-low-confidence"`,
				"readiness-trust-badge--warning",
				"Missing inputs",
				"Waiting for today",
			},
		},
		{
			name:    "stale",
			serving: readinessServingForTest(health.ReadinessServingStale, health.ReadinessConfidenceProvisional, ""),
			want: []string{
				`class="today-hero status-good readiness-low-confidence"`,
				"readiness-trust-badge--warning",
				"Stale inputs",
				"Some recovery inputs are stale.",
			},
		},
		{
			name:    "low coverage",
			serving: readinessServingForTest(health.ReadinessServingLowCoverage, health.ReadinessConfidenceLow, "sleep_quality_low"),
			want: []string{
				`class="today-hero status-good readiness-low-confidence"`,
				"readiness-trust-badge--low",
				"Low confidence",
				"Low-confidence score: input quality is weak.",
				"stage breakdown looks unreliable",
			},
		},
		{
			name:    "capped",
			serving: readinessServingForTest(health.ReadinessServingCapped, health.ReadinessConfidenceProvisional, "illness_suspicion_moderate"),
			want: []string{
				"readiness-trust-badge--neutral",
				"Adjusted",
				"Displayed score adjusted for safety.",
				"subjective signals are present",
			},
			wantMissing: []string{`class="today-hero status-good readiness-low-confidence"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderDashboardReadinessServingForTest(t, tt.serving)
			for _, want := range tt.want {
				if !strings.Contains(html, want) {
					t.Fatalf("dashboard readiness serving missing %q in:\n%s", want, html)
				}
			}
			for _, unwanted := range tt.wantMissing {
				if strings.Contains(html, unwanted) {
					t.Fatalf("dashboard readiness serving unexpectedly contained %q in:\n%s", unwanted, html)
				}
			}
		})
	}
}

func TestReadinessServingReasonTextCoversKnownReasons(t *testing.T) {
	reasons := []string{
		"missing_same_day_evidence",
		"hrv_provisional",
		"hrv_sparse",
		"rhr_missing_overnight_hr_available",
		"sleep_quality_low",
		"illness_suspicion_moderate",
		"illness_suspicion_high",
		"stress_headline",
		"sleep_debt_headline",
	}
	for _, lang := range []string{"en", "ru", "sr"} {
		for _, reason := range reasons {
			got := readinessServingReasonText(lang, reason)
			if got == "" {
				t.Fatalf("readiness reason %q missing for lang %s", reason, lang)
			}
			if strings.Contains(got, reason) || strings.HasPrefix(got, "readiness_serving_reason_") {
				t.Fatalf("readiness reason %q leaked raw key for lang %s: %q", reason, lang, got)
			}
		}
	}
}

func renderDashboardIllnessForTest(t *testing.T, illness *health.IllnessSuspicion) string {
	t.Helper()
	return renderDashboardForTest(t, "ru", nil, illness)
}

func renderDashboardReadinessServingForTest(t *testing.T, serving *health.ReadinessServingState) string {
	t.Helper()
	return renderDashboardForTest(t, "en", serving, nil)
}

func renderDashboardForTest(t *testing.T, lang string, serving *health.ReadinessServingState, illness *health.IllnessSuspicion) string {
	t.Helper()
	sleepScore := 76
	br := &health.BriefingResponse{
		Date:                "2026-08-01",
		ReadinessToday:      88,
		ReadinessTodayLabel: "Optimal",
		ReadinessTip:        "A balanced day is a good fit.",
		ReadinessServing:    serving,
		EnergyBank: &health.EnergyBank{
			Current:       72,
			Capacity:      90,
			ActionVerdict: "moderate",
		},
		SleepQuality: &health.SleepQualityBreakdown{
			ScorePct:   &sleepScore,
			Confidence: health.SleepQualityConfidenceFinal,
		},
		IllnessSuspicion: illness,
	}
	base := BasePage{
		Lang:      lang,
		Title:     "Health",
		ActiveNav: "dashboard",
		StaticVer: StaticVer(),
	}
	w := httptest.NewRecorder()
	renderPage(w, "dashboard", buildDashboardPageData(base, br, ""))
	if w.Code != 200 {
		t.Fatalf("render dashboard: status=%d body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func readinessServingForTest(status, confidence, reason string) *health.ReadinessServingState {
	components := []health.ReadinessComponentSummary{
		{Metric: "heart_rate_variability", Present: true, Freshness: health.ReadinessFreshnessOK, Confidence: health.ReadinessConfidenceFinal},
		{Metric: "resting_heart_rate", Present: true, Freshness: health.ReadinessFreshnessOK, Confidence: health.ReadinessConfidenceFinal},
		{Metric: "sleep_total", Present: true, Freshness: health.ReadinessFreshnessOK, Confidence: health.ReadinessConfidenceFinal},
	}
	switch status {
	case health.ReadinessServingMissing:
		components[1].Present = false
		components[1].Freshness = health.ReadinessFreshnessMissing
		components[1].MissingReason = "missing_same_day_value"
	case health.ReadinessServingStale:
		components[0].Freshness = health.ReadinessFreshnessStale
	}
	return &health.ReadinessServingState{
		Status:     status,
		Confidence: confidence,
		Reason:     reason,
		Components: components,
	}
}
