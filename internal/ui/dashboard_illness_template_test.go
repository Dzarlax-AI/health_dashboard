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
			{Metric: "respiratory_rate", Status: "ok", Strength: "strong"},
			{Metric: "resting_heart_rate", Status: "ok", Strength: "moderate"},
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

	low := renderDashboardIllnessForTest(t, &health.IllnessSuspicion{
		Confidence: "low",
		Signals:    []health.IllnessEvidenceSignal{{Metric: "respiratory_rate", Status: "ok", Strength: "moderate"}},
	})
	if strings.Contains(low, "Возможные признаки болезни") {
		t.Fatalf("low confidence illness suspicion should not render the dashboard panel")
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
