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
	headline := computeHeadline(d, ls)

	resp := &BriefingResponse{
		Date:                d.LastDate,
		Greeting:            "Here's your health summary",
		Overall:             overall,
		Headline:            headline,
		Sections:            sections,
		Highlights:          highlights,
		ReadinessScore:      readinessScore,
		ReadinessLabel:      label,
		ReadinessTip:        tip,
		RecoveryPct:         readinessScore,
		ReadinessToday:      readinessScore,
		ReadinessTodayLabel: label,
		Correlation:         buildCorrelation(d),
		Insights:            computeInsights(d, activitySec, readinessScore, ls),
		Alerts:              computeAlerts(d, ls),
		Sleep:               computeSleepAnalysis(d),
		MetricCards:         metricCards,
	}

	// Coherence pass: when a stress headline fires, downgrade conflicting
	// "good" verdicts so the briefing doesn't say "stress" up top and
	// "all good" in section cards (converging-evidence rule, Meeusen 2013).
	applyCoherencePass(resp, ls)
	resp.Overall = overallStatus(resp.Sections) // re-derive after coherence

	// Energy Bank runs *after* the coherence pass so a stress-capped readiness
	// flows in as morning capacity, and so the verdict can downgrade
	// `push_hard` when a stress headline is present.
	resp.EnergyBank = computeEnergyBank(d, resp.ReadinessScore, headline, ls)

	return resp
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
		// Today's value (index 0). Two baselines: 7-day acute and 30-day chronic.
		today := sp.vals[0]
		baseline7 := avg(safeSlice(sp.vals, 0, 7))
		baseline30 := avg(sp.vals)

		invertBetter := sp.metric == "resting_heart_rate"
		pct7, label7, status7 := formatTrend(today, baseline7, invertBetter, ls["trend_vs_7d"])
		pct30, label30, status30 := formatTrend(today, baseline30, invertBetter, ls["trend_vs_30d"])

		out = append(out, MetricCard{
			Name:   sp.name,
			Metric: sp.metric,
			Value:  fmtFloat(today, sp.decimal),
			Unit:   sp.unit,
			// Legacy single-baseline fields — kept pointing at the 30-day
			// baseline so existing template renders stay correct.
			TrendPct:    pct30,
			TrendLabel:  label30,
			TrendStatus: status30,
			// New Bevel-style dual baseline view.
			Trend7dPct:     pct7,
			Trend7dLabel:   label7,
			Trend7dStatus:  status7,
			Trend30dPct:    pct30,
			Trend30dLabel:  label30,
			Trend30dStatus: status30,
		})
	}
	return out
}

// formatTrend converts a (today, baseline) pair into a rounded pct, a label
// like "+13% vs last 7d", and a positive/negative/neutral status.
// `invertBetter=true` for metrics where lower is better (RHR).
func formatTrend(today, baseline float64, invertBetter bool, suffix string) (float64, string, string) {
	pct := roundTo1(pctChange(today, baseline))
	if baseline == 0 {
		return 0, "", "neutral"
	}
	directional := pct
	if invertBetter {
		directional = -pct
	}
	status := "neutral"
	switch {
	case directional > 1:
		status = "positive"
	case directional < -1:
		status = "negative"
	}
	var label string
	switch {
	case pct > 0:
		label = fmt.Sprintf("+%.0f%% %s", pct, suffix)
	case pct < 0:
		label = fmt.Sprintf("%.0f%% %s", pct, suffix)
	default:
		label = suffix
	}
	return pct, label, status
}
