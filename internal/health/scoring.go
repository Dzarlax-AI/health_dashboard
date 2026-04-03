package health

import (
	"fmt"
)

// ComputeBriefing calculates all health scores and insights from pre-fetched raw metrics.
// It is a pure function — no I/O, all inputs come from RawMetrics.
// lang selects the output language ("en", "ru", "sr"); defaults to "en".
func ComputeBriefing(d RawMetrics, lang string) *BriefingResponse {
	ls := GetStrings(lang)

	recoverySec := scoreRecovery(d, ls)
	sleepSec := scoreSleep(d, ls)
	activitySec := scoreActivity(d, ls)
	cardioSec := scoreCardio(d, ls)

	readinessScore, label, tip := computeReadinessScore(d, ls)

	var sections []BriefingSection
	for _, sec := range []*BriefingSection{recoverySec, sleepSec, activitySec, cardioSec} {
		if sec != nil {
			sections = append(sections, *sec)
		}
	}

	overall := overallStatus(sections)
	highlights := buildHighlights(d, ls)
	metricCards := buildMetricCards(d, ls)

	return &BriefingResponse{
		Date:           d.LastDate,
		Greeting:       "Here's your health summary",
		Overall:        overall,
		Sections:       sections,
		Highlights:     highlights,
		ReadinessScore:      readinessScore,
		ReadinessLabel:      label,
		ReadinessTip:        tip,
		RecoveryPct:         readinessScore,
		ReadinessToday:      readinessScore,
		ReadinessTodayLabel: label,
		Correlation:    buildCorrelation(d),
		Insights:       computeInsights(d, activitySec, readinessScore, ls),
		Alerts:         computeAlerts(d, ls),
		Sleep:          computeSleepAnalysis(d),
		MetricCards:    metricCards,
	}
}

func computeReadinessScore(d RawMetrics, ls LangStrings) (score int, label, tip string) {
	score, _, _, _ = computeReadiness(d)
	label, tip = readinessLabelTip(score, ls)
	return score, label, tip
}

func overallStatus(sections []BriefingSection) string {
	good, fair, low := 0, 0, 0
	for _, s := range sections {
		switch s.Status {
		case "good":
			good++
		case "fair":
			fair++
		case "low":
			low++
		}
	}
	if low >= 2 {
		return "low"
	}
	if fair+low > good {
		return "fair"
	}
	return "good"
}

func buildHighlights(d RawMetrics, ls LangStrings) []BriefingDetail {
	var out []BriefingDetail
	// Show today's values (index 0), not multi-day averages.
	if len(d.Steps) > 0 {
		out = append(out, BriefingDetail{Label: ls["lbl_steps"], Value: formatNumber(int(d.Steps[0]))})
	}
	if len(d.Sleep) > 0 {
		out = append(out, BriefingDetail{Label: ls["sec_sleep"], Value: fmtFloat(d.Sleep[0], 1) + "h"})
	}
	if len(d.RHR) > 0 {
		out = append(out, BriefingDetail{Label: ls["lbl_resting_hr"], Value: fmtFloat(d.RHR[0], 0) + " bpm"})
	}
	if len(d.Cal) > 0 {
		out = append(out, BriefingDetail{Label: ls["lbl_active_cal"], Value: formatNumber(int(d.Cal[0])) + " kcal"})
	}
	return out
}

func buildMetricCards(d RawMetrics, ls LangStrings) []MetricCard {
	type cardSpec struct {
		name    string
		metric  string
		unit    string
		vals    []float64
		decimal int
	}
	var out []MetricCard
	for _, sp := range []cardSpec{
		{ls["lbl_steps"], "step_count", ls["lbl_steps"], d.Steps, 0},
		{ls["sec_sleep"], "sleep_total", "hrs", d.Sleep, 1},
		{ls["lbl_hrv"], "heart_rate_variability", "ms", d.HRV, 0},
		{ls["lbl_resting_hr"], "resting_heart_rate", "bpm", d.RHR, 0},
		{ls["lbl_resp"], "respiratory_rate", "br/min", d.Resp, 1},
	} {
		if len(sp.vals) == 0 {
			continue
		}
		// Show today's value (index 0), trend vs 30-day baseline.
		today := sp.vals[0]
		baseline := avg(sp.vals)
		pct := pctChange(today, baseline)
		
		pctR := roundTo1(pct)
		tLabel := ""
		if pctR > 0 {
			tLabel = fmt.Sprintf("+%.0f%% %s", pctR, ls["lbl_vs_avg"])
		} else if pctR < 0 {
			tLabel = fmt.Sprintf("%.0f%% %s", pctR, ls["lbl_vs_avg"])
		} else {
			tLabel = ls["lbl_vs_avg"]
		}

		out = append(out, MetricCard{
			Name:       sp.name,
			Metric:     sp.metric,
			Value:      fmtFloat(today, sp.decimal),
			Unit:       sp.unit,
			TrendPct:   pctR,
			TrendLabel: tLabel,
		})
	}
	return out
}
