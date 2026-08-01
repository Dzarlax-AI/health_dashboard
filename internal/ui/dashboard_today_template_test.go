package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestBuildDashboardPageDataCreatesPrimaryAndSupportingScores(t *testing.T) {
	sleepScore := 82
	updated := time.Date(2026, 8, 1, 8, 42, 0, 0, time.UTC)
	br := &health.BriefingResponse{
		Date:                  "2026-08-01",
		ReadinessToday:        73,
		ReadinessTodayBand:    "fair",
		ReadinessTodayLabel:   "Optimal",
		ReadinessServing:      &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal},
		EnergyBank:            &health.EnergyBank{Current: 81, Capacity: 94, ActionVerdict: "moderate"},
		SleepQuality:          &health.SleepQualityBreakdown{ScorePct: &sleepScore, DurationPct: 96, Confidence: health.SleepQualityConfidenceFinal},
		TodayGuidance:         &health.DashboardTodayGuidance{Action: "moderate", Label: "Moderate day", Summary: "Keep today comfortably active.", Reason: "Fresh evidence.", Confidence: health.ReadinessConfidenceFinal, UpdatedAt: &updated},
		MetricCards:           []health.MetricCard{},
		Sections:              []health.BriefingSection{},
		ReadinessDisplayScore: 73,
	}

	data := buildDashboardPageData(BasePage{Lang: "en", StaticVer: StaticVer()}, br, "")
	if !data.HasHealthData {
		t.Fatal("HasHealthData = false")
	}
	for name, gauge := range map[string]scoreGaugeView{
		"readiness": data.ReadinessGauge,
		"energy":    data.EnergyGauge,
		"sleep":     data.SleepGauge,
	} {
		if !gauge.HasValue {
			t.Fatalf("%s gauge missing value: %+v", name, gauge)
		}
	}
	if data.SleepGauge.Value != sleepScore {
		t.Fatalf("sleep gauge = %d, want %d", data.SleepGauge.Value, sleepScore)
	}
	if data.UpdatedLabel == "" {
		t.Fatal("UpdatedLabel is empty")
	}
	if data.ReadinessBand != "fair" {
		t.Fatalf("readiness band = %q, want canonical fair", data.ReadinessBand)
	}
}

func TestBuildDashboardPageDataDoesNotInventPartialSleepScore(t *testing.T) {
	total := 7.0
	br := &health.BriefingResponse{
		Date:             "2026-08-01",
		ReadinessToday:   64,
		ReadinessServing: &health.ReadinessServingState{Status: health.ReadinessServingDataAccruing, Confidence: health.ReadinessConfidenceProvisional},
		EnergyBank:       &health.EnergyBank{Current: 63, Capacity: 79, ActionVerdict: "moderate"},
		Sleep:            &health.SleepAnalysis{TotalAvg: total},
		SleepQuality:     &health.SleepQualityBreakdown{DurationPct: 88, Confidence: health.SleepQualityConfidencePartial},
		TodayGuidance:    &health.DashboardTodayGuidance{Action: "moderate", Summary: "Keep today comfortably active.", Reason: "Sleep is refining.", Confidence: health.ReadinessConfidenceProvisional},
	}
	data := buildDashboardPageData(BasePage{Lang: "en", StaticVer: StaticVer()}, br, "")
	if data.SleepGauge.HasValue {
		t.Fatalf("partial sleep rendered precise gauge: %+v", data.SleepGauge)
	}
	if data.SleepGauge.EmptyLabel == "" {
		t.Fatal("partial sleep explanation is empty")
	}
}

func TestBuildDashboardPageDataClampsSignedEnergyForGaugeGeometry(t *testing.T) {
	highSleepScore := 118
	br := &health.BriefingResponse{
		Date:             "2026-08-01",
		ReadinessToday:   140,
		ReadinessServing: &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal},
		EnergyBank:       &health.EnergyBank{Current: -18, Capacity: 60, ActionVerdict: "rest"},
		SleepQuality:     &health.SleepQualityBreakdown{ScorePct: &highSleepScore, Confidence: health.SleepQualityConfidenceFinal},
	}
	data := buildDashboardPageData(BasePage{Lang: "en", StaticVer: StaticVer()}, br, "")
	if data.EnergyGauge.Value != 0 {
		t.Fatalf("signed energy gauge = %d, want visual clamp 0", data.EnergyGauge.Value)
	}
	if data.ReadinessGauge.Value != 100 {
		t.Fatalf("readiness gauge = %d, want visual clamp 100", data.ReadinessGauge.Value)
	}
	if data.SleepGauge.Value != 100 {
		t.Fatalf("sleep gauge = %d, want visual clamp 100", data.SleepGauge.Value)
	}
}

func TestDashboardGaugeGeometryIsStructural(t *testing.T) {
	score := 80
	data := buildDashboardPageData(BasePage{Lang: "en", Title: "Health", StaticVer: StaticVer()}, &health.BriefingResponse{
		Date:               "2026-08-01",
		ReadinessToday:     72,
		ReadinessTodayBand: "fair",
		ReadinessServing:   &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal},
		EnergyBank: &health.EnergyBank{
			Current: 70, Capacity: 90, ActionVerdict: "moderate",
			Components: []health.EnergyBankComponent{{Name: "morning_capacity", Value: 80, Note: "review fixture"}},
			Flags:      []string{"backfilled"},
		},
		SleepQuality:  &health.SleepQualityBreakdown{ScorePct: &score, Confidence: health.SleepQualityConfidenceFinal},
		TodayGuidance: &health.DashboardTodayGuidance{Action: "moderate", Summary: "Keep today comfortably active.", Reason: "Fresh evidence.", Confidence: health.ReadinessConfidenceFinal},
	}, "")

	w := httptest.NewRecorder()
	renderPage(w, "dashboard", data)
	if w.Code != 200 {
		t.Fatalf("render status = %d; body=%s", w.Code, w.Body.String())
	}
	html := w.Body.String()
	if got := strings.Count(html, `viewBox="0 0 120 120"`); got != 3 {
		t.Fatalf("fixed gauge viewBox count = %d, want 3", got)
	}
	if got := strings.Count(html, `preserveAspectRatio="xMidYMid meet"`); got != 3 {
		t.Fatalf("preserveAspectRatio count = %d, want 3", got)
	}
	if strings.Contains(html, `daily-score-card--readiness`) {
		t.Fatal("readiness gauge is duplicated below the hero")
	}
	if !strings.Contains(html, `class="today-hero status-fair"`) {
		t.Fatal("hero does not use the canonical readiness band")
	}
	if strings.Contains(html, `stress_flag_backfilled`) {
		t.Fatal("internal EnergyBank provenance flag leaked into the user-facing dashboard")
	}
	for _, id := range []string{`id="readiness-sparkline"`, `id="energy-sparkline"`, `id="energy-hourly-chart"`} {
		if !strings.Contains(html, id) {
			t.Fatalf("friendly dashboard dropped existing deep-data surface %s", id)
		}
	}
	if !strings.Contains(cssStyle, ".score-gauge__frame") || !strings.Contains(cssStyle, "aspect-ratio: 1") || !strings.Contains(cssStyle, "place-items: center") {
		t.Fatal("gauge CSS is missing square centered geometry")
	}
	if !strings.Contains(cssStyle, "#hero-section.today-hero") {
		t.Fatal("friendly hero must override the legacy #hero-section specificity")
	}
	for _, selector := range []string{
		"#hero-section.today-hero.status-optimal",
		"#hero-section.today-hero.status-fair",
		"#hero-section.today-hero.status-low",
		"#hero-section.today-hero.readiness-low-confidence .score-gauge__value",
		".today-action--push_hard",
		".today-action--moderate",
	} {
		if !strings.Contains(cssStyle, selector) {
			t.Fatalf("friendly dashboard state styling missing %s", selector)
		}
	}
}

func TestDashboardNoDataRendersOnboardingWithoutGauge(t *testing.T) {
	data := buildDashboardPageData(BasePage{Lang: "en", Title: "Health", StaticVer: StaticVer()}, &health.BriefingResponse{}, "")
	w := httptest.NewRecorder()
	renderPage(w, "dashboard", data)
	if w.Code != 200 {
		t.Fatalf("render status = %d", w.Code)
	}
	html := w.Body.String()
	if strings.Contains(html, `class="score-gauge__svg"`) {
		t.Fatal("no-data dashboard rendered a gauge")
	}
	if !strings.Contains(html, `id="dashboard-no-data"`) {
		t.Fatal("no-data onboarding missing")
	}
}
