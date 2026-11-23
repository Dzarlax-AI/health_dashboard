package health

import (
	"fmt"
)

// ComputeBriefing calculates all health scores and insights from pre-fetched raw metrics.
// It is a pure function — no I/O, all inputs come from RawMetrics.
// lang selects the output language ("en", "ru", "sr"); defaults to "en".
func ComputeBriefing(d RawMetrics, lang string) *BriefingResponse {
	resp := ComputeBriefingWithProjection(d, lang, nil)
	resp.EnergyBank = computeLegacyEnergyBank(d, resp.ReadinessScore, resp.Headline, GetStrings(lang))
	return resp
}

// ComputeBriefingWithProjection composes the briefing around the reader's dated score.
func ComputeBriefingWithProjection(d RawMetrics, lang string, projection *ReadinessProjection) *BriefingResponse {
	return ComputeBriefingWithTrends(d, lang, projection, nil)
}

// MetricTrend contains the two dated comparisons used by a briefing card.
type MetricTrend struct {
	Pct7, Pct30       float64
	Status7, Status30 string
	Has7, Has30       bool
}

// ComputeBriefingWithTrends renders the SQL comparisons supplied by a dated storage reader.
func ComputeBriefingWithTrends(d RawMetrics, lang string, projection *ReadinessProjection, trends map[string]MetricTrend) *BriefingResponse {
	return ComputeBriefingWithMetrics(d, lang, projection, trends, computeSleepAnalysis(d))
}

// ComputeBriefingWithMetrics renders dated card trends and the weekly sleep summary.
func ComputeBriefingWithMetrics(d RawMetrics, lang string, projection *ReadinessProjection, trends map[string]MetricTrend, weeklySleep *SleepAnalysis) *BriefingResponse {
	ls := GetStrings(lang)

	recoverySec := scoreRecovery(d, ls)
	sleepSec := scoreSleep(d, ls)
	activitySec := scoreActivity(d, ls)
	cardioSec := scoreCardio(d, ls)

	resp := newReadinessBriefing(d, ls, projection)
	readinessScore := resp.ReadinessScore

	var sections []BriefingSection
	for _, sec := range []*BriefingSection{recoverySec, sleepSec, activitySec, cardioSec} {
		if sec != nil {
			sections = append(sections, *sec)
		}
	}

	highlights := buildHighlights(d, ls)
	metricCards := buildMetricCards(d, ls, trends)
	resp.Greeting = "Here's your health summary"
	resp.Overall = overallStatus(sections)
	resp.Sections = sections
	resp.Highlights = highlights
	resp.Correlation = buildCorrelation(d)
	resp.Insights = computeInsights(d, activitySec, readinessScore, ls)
	resp.Alerts = computeAlerts(d, ls)
	resp.Sleep = weeklySleep
	resp.MetricCards = metricCards
	resp.RawMetrics = &d

	// Coherence pass: when a stress headline fires, downgrade conflicting
	// "good" verdicts so the briefing doesn't say "stress" up top and
	// "all good" in section cards (converging-evidence rule, Meeusen 2013).
	applyCoherencePass(resp, ls)
	resp.Overall = overallStatus(resp.Sections) // re-derive after coherence
	updateReadinessServing(resp)

	return resp
}

// ComputeReadinessBriefing applies the live readiness formula and headline
// limits without computing the briefing's other scores.
func ComputeReadinessBriefing(d RawMetrics, lang string) *BriefingResponse {
	return ComputeReadinessBriefingWithProjection(d, lang, nil)
}

// ComputeReadinessBriefingWithProjection composes the briefing around the reader's dated score.
func ComputeReadinessBriefingWithProjection(d RawMetrics, lang string, projection *ReadinessProjection) *BriefingResponse {
	ls := GetStrings(lang)
	resp := newReadinessBriefing(d, ls, projection)
	applyCoherencePass(resp, ls)
	updateReadinessServing(resp)
	return resp
}

// ReadinessProjection is the score after dated evidence limits and before the headline pass.
type ReadinessProjection struct {
	RawScore, DisplayScore int
	Confidence, CapReason  string
}

func newReadinessBriefing(d RawMetrics, ls LangStrings, projection *ReadinessProjection) *BriefingResponse {
	var readiness readinessComputation
	if projection == nil {
		readiness = computeReadinessWithEvidence(d)
	} else {
		readiness.RawScore, readiness.DisplayScore = projection.RawScore, projection.DisplayScore
		readiness.Confidence, readiness.CapReason = projection.Confidence, projection.CapReason
		if e := d.ReadinessEvidence; e != nil {
			readiness.Components = readinessComponentSummaries(*e)
		}
		readiness.Serving = readinessServingState(readiness.Confidence, readiness.CapReason, readiness.Components)
	}
	score := readiness.DisplayScore
	band := ReadinessBand(score)
	label, tip := readinessLabelTip(score, ls)
	return &BriefingResponse{
		Date:                  d.LastDate,
		Headline:              computeHeadline(d, ls),
		ReadinessScore:        score,
		ReadinessBand:         band,
		ReadinessLabel:        label,
		ReadinessTip:          tip,
		ReadinessRawScore:     readiness.RawScore,
		ReadinessDisplayScore: score,
		ReadinessConfidence:   readiness.Confidence,
		ReadinessCapReason:    readiness.CapReason,
		ReadinessComponents:   readiness.Components,
		ReadinessServing:      readiness.Serving,
		RecoverySource:        ReadinessRecoverySourceLegacyAlias,
		RecoveryPct:           score,
		ReadinessToday:        score,
		ReadinessTodayBand:    band,
		ReadinessTodayLabel:   label,
		SleepQuality:          computeDashboardSleepQuality(d),
	}
}

func computeDashboardSleepQuality(d RawMetrics) *SleepQualityBreakdown {
	if d.ReadinessEvidence == nil {
		return ComputeSleepQualityBreakdown(nil, nil, nil, nil, ReadinessComponentEvidence{})
	}
	e := d.ReadinessEvidence
	qualityEvidence := e.SleepQuality
	for _, component := range []ReadinessComponentEvidence{
		e.SleepDuration,
		e.SleepDeep,
		e.SleepREM,
		e.SleepAwake,
	} {
		if !component.Present || component.Value == nil {
			qualityEvidence.Present = false
			qualityEvidence.Freshness = ReadinessFreshnessMissing
			break
		}
		if component.Freshness == ReadinessFreshnessStale {
			qualityEvidence.Freshness = ReadinessFreshnessStale
			qualityEvidence.Confidence = ReadinessConfidenceProvisional
		}
		if component.Confidence == ReadinessConfidenceLow {
			qualityEvidence.Confidence = ReadinessConfidenceLow
		}
	}
	return ComputeSleepQualityBreakdown(
		e.SleepDuration.Value,
		e.SleepDeep.Value,
		e.SleepREM.Value,
		e.SleepAwake.Value,
		qualityEvidence,
	)
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

// SleepCardSeries returns the metric and observations shown by the sleep card.
func (d *RawMetrics) SleepCardSeries() (string, []float64) {
	if len(d.NightSleep) > 0 {
		return "night_sleep_total", d.NightSleep
	}
	return "sleep_total", d.Sleep
}

func buildMetricCards(d RawMetrics, ls LangStrings, trends map[string]MetricTrend) []MetricCard {
	type cardSpec struct {
		name    string
		metric  string
		unit    string
		vals    []float64
		decimal int
	}
	// Sleep card: prefer night_sleep_total when the iOS client has synced
	// new-format data — it filters out daytime naps so the displayed
	// "today" value reflects actual nightly recovery rather than total
	// time-in-bed across naps. Fall back to d.Sleep for older data.
	// Trends still use whichever series is in `vals`, so going forward
	// trend math stays consistent with the displayed value. Readiness
	// scoring (separate code path) is intentionally untouched in this
	// change — it still uses d.Sleep so historical scores stay stable.
	sleepMetric, sleepVals := d.SleepCardSeries()
	var out []MetricCard
	for _, sp := range []cardSpec{
		{ls["lbl_steps"], "step_count", ls["lbl_steps"], d.Steps, 0},
		{ls["sec_sleep"], sleepMetric, "hrs", sleepVals, 1},
		{ls["lbl_hrv"], "heart_rate_variability", "ms", d.HRV, 0},
		{ls["lbl_resting_hr"], "resting_heart_rate", "bpm", d.RHR, 0},
		{ls["lbl_resp"], "respiratory_rate", "br/min", d.Resp, 1},
	} {
		if len(sp.vals) == 0 {
			continue
		}
		// Today's value (index 0). Two baselines: 7-day acute and 30-day chronic.
		// Both baselines exclude today so the comparison is "today vs prior" —
		// matches the `chronicAvg` convention used by Energy Bank's strain
		// (Gabbett 2016 ACWR: today / prior chronic mean).
		today := sp.vals[0]
		var pct7, pct30 float64
		var label7, label30, status7, status30 string
		if trends != nil {
			t := trends[sp.metric]
			pct7, pct30, status7, status30 = t.Pct7, t.Pct30, t.Status7, t.Status30
			if t.Has7 {
				label7 = trendLabel(pct7, ls["trend_vs_7d"])
			}
			if t.Has30 {
				label30 = trendLabel(pct30, ls["trend_vs_30d"])
			}
		} else {
			invert := sp.metric == "resting_heart_rate"
			pct7, label7, status7 = formatTrend(today, avg(safeSlice(sp.vals, 1, 8)), invert, ls["trend_vs_7d"])
			pct30, label30, status30 = formatTrend(today, avg(safeSlice(sp.vals, 1, len(sp.vals))), invert, ls["trend_vs_30d"])
		}

		card := MetricCard{
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
		}
		// Nap badge: append "+%dm nap" to the sleep card when the user
		// took at least one detectable daytime sleep TODAY. Use
		// d.NapToday (point-in-time) rather than d.Nap[0] — the latter
		// is the most recent day someone napped, which would latch a
		// stale yesterday-nap onto today's card.
		if sp.metric == "night_sleep_total" && d.NapToday > 0 {
			napMin := int(d.NapToday*60 + 0.5)
			if napMin > 0 {
				// Guard against missing translation key — fmt.Sprintf
				// on an empty format with arguments emits "%!(EXTRA …)"
				// garbage. Skip the badge silently in that case.
				if fmtStr := ls["lbl_nap_badge"]; fmtStr != "" {
					card.Badge = fmt.Sprintf(fmtStr, napMin)
				}
			}
		}
		out = append(out, card)
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
	return pct, trendLabel(pct, suffix), status
}

func trendLabel(pct float64, suffix string) string {
	var label string
	switch {
	case pct > 0:
		label = fmt.Sprintf("+%.0f%% %s", pct, suffix)
	case pct < 0:
		label = fmt.Sprintf("%.0f%% %s", pct, suffix)
	default:
		label = suffix
	}
	return label
}
