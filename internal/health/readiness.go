package health

import "math"

func scoreRecovery(d RawMetrics, ls LangStrings) *BriefingSection {
	if len(d.HRV) == 0 && len(d.RHR) == 0 {
		return nil
	}
	sec := &BriefingSection{Key: "recovery", Title: ls["sec_recovery"], Icon: "battery"}
	score, maxScore := 0, 0

	if len(d.HRV) >= 9 {
		recent := avg(d.HRV[:7])
		baseline := avg(d.HRV[7:])
		pct := pctChange(recent, baseline)
		t := trend(pct, false)
		// Dynamic threshold: ±1 SD expressed as % of baseline.
		// Clamped to [3%, 15%] so sparse or noisy data stays sensible.
		sd := stddev(d.HRV[7:])
		thresholdPct := 5.0
		if baseline > 0 && sd > 0 {
			thresholdPct = sd / baseline * 100
			if thresholdPct < 3 {
				thresholdPct = 3
			}
			if thresholdPct > 15 {
				thresholdPct = 15
			}
		}
		if pct > thresholdPct {
			score += 2
		} else if pct > -thresholdPct {
			score += 1
		}
		maxScore += 2
		note := ls["hrv_note_stable"]
		if pct > thresholdPct {
			note = ls["hrv_note_good"]
		} else if pct < -thresholdPct {
			note = ls["hrv_note_low"]
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_hrv"], Value: fmtFloat(recent, 0) + " ms", Note: note, Trend: t,
		})
	}

	if len(d.RHR) >= 9 {
		recent := avg(d.RHR[:7])
		baseline := avg(d.RHR[7:])
		pct := pctChange(recent, baseline)
		t := trend(pct, true)
		if pct < -2 {
			score += 2
		} else if pct < 3 {
			score += 1
		}
		maxScore += 2
		note := ls["rhr_note_normal"]
		if pct < -3 {
			note = ls["rhr_note_low"]
		} else if pct > 5 {
			note = ls["rhr_note_high"]
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_resting_hr"], Value: fmtFloat(recent, 0) + " bpm", Note: note, Trend: t,
		})
	}

	if maxScore > 0 {
		ratio := float64(score) / float64(maxScore)
		if ratio >= 0.75 {
			sec.Status = "good"
			sec.Summary = ls["rec_summary_good"]
		} else if ratio >= 0.4 {
			sec.Status = "fair"
			sec.Summary = ls["rec_summary_fair"]
		} else {
			sec.Status = "low"
			sec.Summary = ls["rec_summary_low"]
		}
	}
	return sec
}

// computeReadiness scores 0-100 based on how recent metrics (last 7 days)
// compare to the historical baseline (days 8+). Slices must be sorted
// most-recent-first (index 0 = today).
//
// Scoring philosophy:
//   - "Normal day" (metrics equal to baseline) → ~70
//   - "Good day"  (~10% above baseline)        → ~85
//   - "Peak day"  (~20%+ above baseline)        → ~95-100
//   - "Poor day"  (~15% below baseline)         → ~45
//
// This means 100 is genuinely exceptional, not the default.
//
// The 7-day recent window aligns with the ACWR acute window (Gabbett 2016)
// and is the consensus recommendation for HRV-guided training decisions
// (Plews 2014, Pereira 2026).
func computeReadiness(d RawMetrics) (score int, label, tip string, recoveryPct int) {
	// ratioScore maps a ratio (recent/baseline) to 0-100.
	// ratio=1.0 → 70, ratio=1.2 → 100, ratio=0.8 → 40, ratio=0.67 → ~5
	ratioScore := func(ratio float64) float64 {
		return math.Min(100, math.Max(0, 70+(ratio-1)*150))
	}

	hrvScore := 70.0
	if len(d.HRV) >= 9 {
		recent := avg(d.HRV[:7])
		baseline := avg(d.HRV[7:]) // exclude recent from baseline to avoid dilution
		if baseline > 0 {
			hrvScore = ratioScore(recent / baseline)
		}
	}

	rhrScore := 70.0
	if len(d.RHR) >= 9 {
		recent := avg(d.RHR[:7])
		baseline := avg(d.RHR[7:])
		if recent > 0 {
			// RHR: lower is better → invert ratio
			rhrScore = ratioScore(baseline / recent)
		}
	}

	// Sleep: blend absolute duration with relative-to-baseline component.
	sleepScore := 70.0
	if len(d.Sleep) >= 9 {
		recent := avg(d.Sleep[:7])
		baseline := avg(d.Sleep[7:])

		var absScore float64
		switch {
		case recent >= 8.0:
			absScore = 95
		case recent >= 7.5:
			absScore = 85
		case recent >= 7.0:
			absScore = 75
		case recent >= 6.5:
			absScore = 60
		case recent >= 6.0:
			absScore = 45
		case recent >= 5.5:
			absScore = 30
		default:
			absScore = 15
		}

		// Oversleep penalty: ≥9h associated with 34% higher all-cause
		// mortality (Li et al. 2025, 79 cohort meta-analysis).
		switch {
		case recent >= 10.0:
			absScore = math.Min(absScore, 40)
		case recent >= 9.5:
			absScore = math.Min(absScore, 60)
		case recent >= 9.0:
			absScore = math.Min(absScore, 80)
		}

		relScore := 70.0
		if baseline > 0 {
			relScore = ratioScore(recent / baseline)
		}
		sleepScore = absScore*0.5 + relScore*0.5
	}

	s := int(math.Round(hrvScore*0.4 + rhrScore*0.3 + sleepScore*0.3))
	if s > 100 {
		s = 100
	}
	if s < 0 {
		s = 0
	}
	return s, "", "", 0 // label/tip filled by caller with i18n
}

// computeReadinessToday scores 0-100 using today's values (index 0) vs the
// historical baseline (days 7+). Same formula as computeReadiness but reacts
// immediately to a single bad night instead of smoothing over 7 days.
func computeReadinessToday(d RawMetrics) int {
	ratioScore := func(ratio float64) float64 {
		return math.Min(100, math.Max(0, 70+(ratio-1)*150))
	}

	hrvScore := 70.0
	if len(d.HRV) >= 9 {
		today := d.HRV[0]
		baseline := avg(d.HRV[7:])
		if baseline > 0 && today > 0 {
			hrvScore = ratioScore(today / baseline)
		}
	}

	rhrScore := 70.0
	if len(d.RHR) >= 9 {
		today := d.RHR[0]
		baseline := avg(d.RHR[7:])
		if today > 0 {
			rhrScore = ratioScore(baseline / today)
		}
	}

	sleepScore := 70.0
	if len(d.Sleep) >= 9 {
		today := d.Sleep[0]
		baseline := avg(d.Sleep[7:])

		var absScore float64
		switch {
		case today >= 8.0:
			absScore = 95
		case today >= 7.5:
			absScore = 85
		case today >= 7.0:
			absScore = 75
		case today >= 6.5:
			absScore = 60
		case today >= 6.0:
			absScore = 45
		case today >= 5.5:
			absScore = 30
		default:
			absScore = 15
		}
		switch {
		case today >= 10.0:
			absScore = math.Min(absScore, 40)
		case today >= 9.5:
			absScore = math.Min(absScore, 60)
		case today >= 9.0:
			absScore = math.Min(absScore, 80)
		}

		relScore := 70.0
		if baseline > 0 {
			relScore = ratioScore(today / baseline)
		}
		sleepScore = absScore*0.5 + relScore*0.5
	}

	s := int(math.Round(hrvScore*0.4 + rhrScore*0.3 + sleepScore*0.3))
	if s > 100 {
		s = 100
	}
	if s < 0 {
		s = 0
	}
	return s
}

// ComputeReadinessScore computes a composite 0–100 readiness that blends
// today's snapshot (60%) with the 7-day trend (40%). This gives a single
// number that reflects "how am I right now" while accounting for accumulated
// fatigue or sleep debt from the past week.
func ComputeReadinessScore(hrv, rhr, sleep []float64) int {
	d := RawMetrics{HRV: hrv, RHR: rhr, Sleep: sleep}
	trend, _, _, _ := computeReadiness(d)
	today := computeReadinessToday(d)
	composite := int(math.Round(float64(today)*0.6 + float64(trend)*0.4))
	if composite > 100 {
		composite = 100
	}
	if composite < 0 {
		composite = 0
	}
	return composite
}

func readinessLabelTip(score int, ls LangStrings) (label, tip string) {
	if score >= 80 {
		return ls["readiness_optimal"], ls["tip_optimal"]
	}
	if score >= 50 {
		return ls["readiness_fair"], ls["tip_fair"]
	}
	return ls["readiness_low"], ls["tip_low"]
}
