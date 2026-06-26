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
			wantMissing: []string{`id="hero-section" class="status-good readiness-low-confidence"`, `<div class="readiness-confidence-note">`},
		},
		{
			name:    "data accruing",
			serving: readinessServingForTest(health.ReadinessServingDataAccruing, health.ReadinessConfidenceProvisional, "hrv_provisional"),
			want: []string{
				"readiness-trust-badge--pending",
				"Still settling",
				"signals are still settling",
				"HRV has too few samples today.",
			},
		},
		{
			name:    "missing",
			serving: readinessServingForTest(health.ReadinessServingMissing, health.ReadinessConfidenceProvisional, "missing_same_day_evidence"),
			want: []string{
				`id="hero-section" class="status-good readiness-low-confidence"`,
				"readiness-trust-badge--warning",
				"Missing inputs",
				"Waiting for today",
			},
		},
		{
			name:    "stale",
			serving: readinessServingForTest(health.ReadinessServingStale, health.ReadinessConfidenceProvisional, ""),
			want: []string{
				`id="hero-section" class="status-good readiness-low-confidence"`,
				"readiness-trust-badge--warning",
				"Stale inputs",
				"Some recovery inputs are stale.",
			},
		},
		{
			name:    "low coverage",
			serving: readinessServingForTest(health.ReadinessServingLowCoverage, health.ReadinessConfidenceLow, "sleep_quality_low"),
			want: []string{
				`id="hero-section" class="status-good readiness-low-confidence"`,
				"readiness-trust-badge--low",
				"Low confidence",
				"Low-confidence score: coverage is thin.",
			},
		},
		{
			name:    "capped",
			serving: readinessServingForTest(health.ReadinessServingCapped, health.ReadinessConfidenceProvisional, "illness_suspicion_moderate"),
			want: []string{
				"readiness-trust-badge--neutral",
				"Adjusted",
				"Displayed score adjusted for safety.",
				"Illness-like signals are present, so the score is conservative.",
			},
			wantMissing: []string{`id="hero-section" class="status-good readiness-low-confidence"`},
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

func renderDashboardIllnessForTest(t *testing.T, illness *health.IllnessSuspicion) string {
	t.Helper()
	w := httptest.NewRecorder()
	renderPage(w, "dashboard", map[string]any{
		"Lang":             "ru",
		"Title":            "Здоровье",
		"ActiveNav":        "dashboard",
		"StaticVer":        StaticVer(),
		"ReadinessScore":   88,
		"ReadinessServing": readinessServingView{},
		"IllnessSuspicion": illness,
		"Cards":            []health.MetricCard{},
		"Alerts":           []health.Alert{},
		"Sections":         []health.BriefingSection{},
		"Insights":         []health.Insight{},
		"CorrelationJSON":  "null",
		"AIInsight":        "",
		"IsAdmin":          false,
	})
	if w.Code != 200 {
		t.Fatalf("render dashboard: status=%d body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func renderDashboardReadinessServingForTest(t *testing.T, serving *health.ReadinessServingState) string {
	t.Helper()
	w := httptest.NewRecorder()
	renderPage(w, "dashboard", map[string]any{
		"Lang":             "en",
		"Title":            "Health",
		"ActiveNav":        "dashboard",
		"StaticVer":        StaticVer(),
		"ReadinessScore":   88,
		"ReadinessServing": buildReadinessServingView("en", serving),
		"Cards":            []health.MetricCard{},
		"Alerts":           []health.Alert{},
		"Sections":         []health.BriefingSection{},
		"Insights":         []health.Insight{},
		"CorrelationJSON":  "null",
		"AIInsight":        "",
		"IsAdmin":          false,
	})
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
