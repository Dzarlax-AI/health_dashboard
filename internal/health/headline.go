package health

import (
	"fmt"
	"math"
)

// Cross-metric stress thresholds — chosen with explicit research anchors.
//
// rhrEvolveAbsBpm (5 bpm) — Wearable HF decompensation 2025 (MDPI):
//   nocturnal RHR rise > 5 bpm doubled CV hospitalisation/mortality risk.
//   Below this delta the signal is dominated by device noise (Dial 2025).
//
// sleepDebtThresholdH (6.5 h) — sits below the AASM ≥ 7 h recommendation
//   (Watson 2015) but above the U-curve worst-case (< 5 h, Li 2025), which
//   gives a "functional debt" zone where additional autonomic stress
//   should not be ignored.
//
// hrvDepressedZ (-1.0) — 1 SD below personal baseline, matching the
//   dynamic threshold approach already used in scoreRecovery (and
//   recommended by Beattie 2024, Plews 2014).
//
// awakeFragmentedH (0.5 h) — same threshold used by scoreSleep for the
//   "consolidated sleep" bonus; > 30 min of nightly wake is the line
//   above which sleep stops being restorative.
//
// stressSignalsRequired (2) — multi-signal converging evidence rule
//   (Meeusen 2013, Plews 2014). One signal alone has too many benign
//   explanations (illness, alcohol, late workout); two or more
//   simultaneously is the recognised overreaching/early-illness pattern
//   (Mishra 2024, Persistent COVID changes 2025).
const (
	rhrElevateAbsBpm      = 5.0
	sleepDebtThresholdH   = 6.5
	hrvDepressedZ         = -1.0
	awakeFragmentedH      = 0.5
	stressSignalsRequired = 2
)

// computeHeadline picks the single most notable signal of the day.
// Priority:
//  1. Multi-signal stress (≥ 2 of: RHR↑, sleep↓, HRV↓, awake↑)
//  2. Sleep debt without other markers
//  3. Largest single |z-score| metric (positive or negative)
//
// Returns nil only when there is genuinely nothing notable AND not enough
// data to compute baselines.
func computeHeadline(d RawMetrics, ls LangStrings) *HeadlineSignal {
	stress, signals := detectStressSignals(d)
	if stress {
		return &HeadlineSignal{
			Key:      "stress",
			Severity: "warning",
			Title:    ls["headline_stress_title"],
			Detail:   formatStressDetail(signals, ls),
			Metrics:  signals,
		}
	}
	if h := detectSleepDebt(d, ls); h != nil {
		return h
	}
	return detectMaxDeviation(d, ls)
}

// detectStressSignals returns (true, deltas) iff ≥2 of the stress markers
// are present. The returned deltas describe each contributing metric
// (with z-score where the baseline supports it).
func detectStressSignals(d RawMetrics) (bool, []HeadlineMetricDelta) {
	var signals []HeadlineMetricDelta

	// RHR: ≥ +5 bpm above baseline (MDPI HF 2025)
	if delta, ok := metricDelta(d.RHR, "resting_heart_rate", "bpm"); ok {
		if delta.DeltaAbs >= rhrElevateAbsBpm {
			signals = append(signals, delta)
		}
	}

	// Sleep: today < 6.5h (functional debt zone, absolute threshold per Watson 2015).
	// The signal fires from the absolute value alone; baseline-relative fields
	// are only populated when there's enough history to compute them, so we
	// don't ship a delta with a fake `baseline=0`.
	if len(d.Sleep) > 0 && d.Sleep[0] > 0 && d.Sleep[0] < sleepDebtThresholdH {
		delta := HeadlineMetricDelta{
			Metric: "sleep_total",
			Value:  d.Sleep[0],
			Unit:   "h",
		}
		if len(d.Sleep) >= minBaseline+2 {
			base := avg(safeSlice(d.Sleep, 7, len(d.Sleep)))
			if base > 0 {
				sd := stddev(safeSlice(d.Sleep, 7, len(d.Sleep)))
				delta.Baseline = base
				delta.DeltaAbs = d.Sleep[0] - base
				delta.DeltaPct = pctChange(d.Sleep[0], base)
				delta.ZScore = zScore(d.Sleep[0], base, sd)
			}
		}
		signals = append(signals, delta)
	}

	// HRV: z-score ≤ -1 (1 SD below personal baseline)
	if delta, ok := metricDelta(d.HRV, "heart_rate_variability", "ms"); ok {
		if delta.ZScore <= hrvDepressedZ {
			// HRV depression sign is "low value vs baseline" — DeltaAbs is
			// already negative; we keep the sign so the UI can show "−5 ms".
			signals = append(signals, delta)
		}
	}

	// Awake: > 0.5h fragmented sleep. awakeFragmentedH here is a clinical
	// threshold, not a personal baseline — we keep Baseline at zero rather
	// than misrepresent the threshold as the user's "norm".
	if len(d.Awake) > 0 && d.Awake[0] > awakeFragmentedH {
		signals = append(signals, HeadlineMetricDelta{
			Metric: "sleep_awake",
			Value:  d.Awake[0],
			Unit:   "h",
		})
	}

	return len(signals) >= stressSignalsRequired, signals
}

// detectSleepDebt fires when sleep is short but other markers are normal:
// caller can then highlight just sleep without the multi-signal stress wording.
func detectSleepDebt(d RawMetrics, ls LangStrings) *HeadlineSignal {
	if len(d.Sleep) == 0 || d.Sleep[0] >= sleepDebtThresholdH || d.Sleep[0] <= 0 {
		return nil
	}
	delta, _ := metricDelta(d.Sleep, "sleep_total", "h")
	return &HeadlineSignal{
		Key:      "sleep_debt",
		Severity: "warning",
		Title:    ls["headline_sleep_debt_title"],
		Detail:   fmt.Sprintf(ls["headline_sleep_debt_detail"], d.Sleep[0]),
		Metrics:  []HeadlineMetricDelta{delta},
	}
}

// detectMaxDeviation surfaces the single metric with the largest |z-score|.
// Used when nothing rises to "stress"/"sleep_debt" — keeps the briefing
// focused on the one number that deviates most from the user's normal.
func detectMaxDeviation(d RawMetrics, ls LangStrings) *HeadlineSignal {
	candidates := []struct {
		name string
		unit string
		vals []float64
	}{
		{"heart_rate_variability", "ms", d.HRV},
		{"resting_heart_rate", "bpm", d.RHR},
		{"sleep_total", "h", d.Sleep},
	}

	var best HeadlineMetricDelta
	bestKey := ""
	bestAbsZ := 0.0
	evaluated := false // becomes true the moment any candidate produces a valid delta
	for _, c := range candidates {
		delta, ok := metricDelta(c.vals, c.name, c.unit)
		if !ok {
			continue
		}
		evaluated = true
		// Pick the metric with the largest deviation from baseline regardless
		// of direction. The "good vs bad" classification (severity = positive
		// vs warning) happens below using per-metric semantics — here we just
		// rank by magnitude of |z-score|.
		absZ := math.Abs(delta.ZScore)
		if absZ > bestAbsZ {
			bestAbsZ = absZ
			best = delta
			if delta.ZScore > 0 {
				bestKey = "above_baseline"
			} else {
				bestKey = "below_baseline"
			}
		}
	}

	if !evaluated {
		// No candidate had enough history to compute a baseline at all —
		// don't fabricate a "stable" verdict from the absence of evidence.
		return nil
	}
	if bestAbsZ < 0.5 {
		// Nothing meaningfully deviates → "stable" headline (positive framing).
		return &HeadlineSignal{
			Key:      "stable",
			Severity: "info",
			Title:    ls["headline_stable_title"],
			Detail:   ls["headline_stable_detail"],
		}
	}

	severity := "info"
	if bestAbsZ >= 1.5 {
		severity = "warning"
	}
	if (best.Metric == "heart_rate_variability" && best.ZScore > 0) ||
		(best.Metric == "resting_heart_rate" && best.ZScore < 0) ||
		(best.Metric == "sleep_total" && best.ZScore > 0) {
		severity = "positive"
	}

	titleKey := "headline_dev_" + best.Metric + "_" + bestKey
	if _, ok := ls[titleKey]; !ok {
		titleKey = "headline_dev_generic"
	}
	return &HeadlineSignal{
		Key:      "single_deviation",
		Severity: severity,
		Title:    ls[titleKey],
		Detail:   formatDeviationDetail(best, ls),
		Metrics:  []HeadlineMetricDelta{best},
	}
}

// metricDelta builds a HeadlineMetricDelta from a slice (most-recent-first)
// using the standard 7d-recent / day8+ baseline split.
// Returns ok=false if there isn't enough data for a meaningful baseline.
func metricDelta(vals []float64, metric, unit string) (HeadlineMetricDelta, bool) {
	if len(vals) < minBaseline+2 || vals[0] <= 0 {
		return HeadlineMetricDelta{}, false
	}
	today := vals[0]
	base := avg(safeSlice(vals, 7, len(vals)))
	sd := stddev(safeSlice(vals, 7, len(vals)))
	z := zScore(today, base, sd)
	return HeadlineMetricDelta{
		Metric:   metric,
		Value:    today,
		Baseline: base,
		DeltaAbs: today - base,
		DeltaPct: pctChange(today, base),
		ZScore:   z,
		Unit:     unit,
	}, true
}

func formatStressDetail(signals []HeadlineMetricDelta, ls LangStrings) string {
	parts := make([]string, 0, len(signals))
	for _, s := range signals {
		switch s.Metric {
		case "resting_heart_rate":
			parts = append(parts, fmt.Sprintf(ls["headline_part_rhr"],
				s.Value, s.DeltaAbs))
		case "heart_rate_variability":
			parts = append(parts, fmt.Sprintf(ls["headline_part_hrv"],
				s.Value, s.ZScore))
		case "sleep_total":
			parts = append(parts, fmt.Sprintf(ls["headline_part_sleep"], s.Value))
		case "sleep_awake":
			parts = append(parts, fmt.Sprintf(ls["headline_part_awake"], s.Value))
		}
	}
	return fmt.Sprintf(ls["headline_stress_detail"], joinComma(parts))
}

func formatDeviationDetail(d HeadlineMetricDelta, ls LangStrings) string {
	return fmt.Sprintf(ls["headline_dev_detail"],
		d.Value, d.Unit, d.DeltaPct, d.Baseline, d.Unit)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// applyCoherencePass enforces converging-evidence consistency between the
// headline and the section statuses. When a stress headline fires, a "good"
// recovery section is downgraded to "fair" and a separate explanatory note
// is attached, so the dashboard doesn't simultaneously say "stress" up top
// and "great recovery" below.
func applyCoherencePass(resp *BriefingResponse, ls LangStrings) {
	if resp.Headline == nil {
		return
	}
	switch resp.Headline.Key {
	case "stress":
		// Downgrade good Recovery to fair, with note explaining why.
		for i := range resp.Sections {
			if resp.Sections[i].Key == "recovery" && resp.Sections[i].Status == "good" {
				resp.Sections[i].Status = "fair"
				resp.Sections[i].Summary = ls["rec_summary_fair_stress"]
			}
		}
		// Cap readiness label at "fair" — per Meeusen 2013 / Plews 2014,
		// converging stress markers contradict an "Optimal" verdict even if
		// the numeric score from any single component looks fine.
		if resp.ReadinessScore > 65 {
			resp.ReadinessScore = 65
			resp.ReadinessLabel = ls["readiness_fair"]
			resp.ReadinessTip = ls["tip_fair"]
			resp.RecoveryPct = 65
			resp.ReadinessToday = 65
			resp.ReadinessTodayLabel = ls["readiness_fair"]
		}
	case "sleep_debt":
		// Sleep < 6.5h alone shouldn't push readiness over 65 (Watson 2015,
		// Walker 2017): one good HRV/RHR night doesn't pay off slept-debt.
		if resp.ReadinessScore > 65 {
			resp.ReadinessScore = 65
			resp.ReadinessLabel = ls["readiness_fair"]
			resp.ReadinessTip = ls["tip_fair"]
			resp.RecoveryPct = 65
			resp.ReadinessToday = 65
			resp.ReadinessTodayLabel = ls["readiness_fair"]
		}
	}
}
