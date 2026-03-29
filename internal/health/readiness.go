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

// ---------------------------------------------------------------------------
// Readiness Score — z-score based, following Plews et al. (2013/2014)
// ---------------------------------------------------------------------------
//
// Each metric is converted to a z-score against a 30-day personal baseline.
// A blended z-score (today 60% + 7-day trend 40%) captures both immediate
// state and accumulated fatigue/debt.
//
// References:
//   - Plews et al. (2013): lnRMSSD rolling mean + CV for training adaptation
//   - Buchheit (2014): combining HRV trend with autonomic markers
//   - Bellenger et al. (2016): HRV-guided training improves outcomes
//   - Walker (2017): sleep debt accumulates, one good night doesn't erase it
//   - Li et al. (2025): U-shaped sleep duration mortality curve (6.5-8h optimal)
//
// Mapping: z=0 (at baseline) → 70, z=+1 → 85, z=-1 → 55, clamped [0,100].
// This means 70 = "normal you", not "average human".
//
// Component weights (evidence-based):
//   - HRV:   40% — strongest autonomic recovery marker (Plews 2013)
//   - RHR:   25% — complementary cardiac marker (Buchheit 2014)
//   - Sleep: 35% — duration + consistency penalty (Walker 2017)

const (
	zToScoreCenter = 70.0  // z=0 maps to this score
	zToScoreScale  = 15.0  // each 1 SD = 15 points
	minReadiness   = 0
	maxReadiness   = 100

	// Minimum data points in baseline for meaningful z-score.
	minBaseline = 7

	// Component weights.
	wHRV   = 0.40
	wRHR   = 0.25
	wSleep = 0.35

	// Blending: today vs 7-day trend.
	wToday = 0.60
	wTrend = 0.40
)

// zScore returns (value - mean) / sd. Returns 0 if sd ≈ 0 (no variance).
func zScore(value, mean, sd float64) float64 {
	if sd < 1e-9 {
		return 0
	}
	return (value - mean) / sd
}

// zToScore maps a z-score to 0-100 readiness.
func zToScore(z float64) float64 {
	return math.Min(float64(maxReadiness),
		math.Max(float64(minReadiness), zToScoreCenter+z*zToScoreScale))
}

// sleepCV returns the coefficient of variation of sleep durations.
// High CV (>15%) indicates inconsistent sleep, which is independently
// associated with worse health outcomes (Huang et al. 2020).
func sleepCV(vals []float64) float64 {
	if len(vals) < 3 {
		return 0
	}
	m := avg(vals)
	if m < 0.1 {
		return 0
	}
	return stddev(vals) / m * 100
}

// computeReadiness computes a single composite 0-100 readiness score.
//
// Data slices must be sorted most-recent-first (index 0 = today).
// Needs at least minBaseline+2 days of data for meaningful results.
func computeReadiness(d RawMetrics) (score int, label, tip string, recoveryPct int) {
	components := 0
	totalZ := 0.0

	// --- HRV (higher = better) ---
	if len(d.HRV) >= minBaseline+2 {
		today := d.HRV[0]
		recent7 := avg(safeSlice(d.HRV, 0, 7))
		baseVals := safeSlice(d.HRV, 7, len(d.HRV))
		baseMean := avg(baseVals)
		baseSD := stddev(baseVals)

		zToday := zScore(today, baseMean, baseSD)
		zTrend := zScore(recent7, baseMean, baseSD)
		blended := zToday*wToday + zTrend*wTrend

		totalZ += blended * wHRV
		components++
	}

	// --- RHR (lower = better → invert z-score) ---
	if len(d.RHR) >= minBaseline+2 {
		today := d.RHR[0]
		recent7 := avg(safeSlice(d.RHR, 0, 7))
		baseVals := safeSlice(d.RHR, 7, len(d.RHR))
		baseMean := avg(baseVals)
		baseSD := stddev(baseVals)

		zToday := -zScore(today, baseMean, baseSD) // inverted
		zTrend := -zScore(recent7, baseMean, baseSD)
		blended := zToday*wToday + zTrend*wTrend

		totalZ += blended * wRHR
		components++
	}

	// --- Sleep (duration + consistency) ---
	if len(d.Sleep) >= minBaseline+2 {
		today := d.Sleep[0]
		recent7 := safeSlice(d.Sleep, 0, 7)
		recent7Avg := avg(recent7)
		baseVals := safeSlice(d.Sleep, 7, len(d.Sleep))
		baseMean := avg(baseVals)
		baseSD := stddev(baseVals)

		// Duration z-score
		zToday := zScore(today, baseMean, baseSD)
		zTrend := zScore(recent7Avg, baseMean, baseSD)
		durationZ := zToday*wToday + zTrend*wTrend

		// Absolute duration penalty (U-shaped: <6h and >9.5h are bad)
		absPenalty := 0.0
		switch {
		case recent7Avg < 5.0:
			absPenalty = -1.5
		case recent7Avg < 5.5:
			absPenalty = -1.0
		case recent7Avg < 6.0:
			absPenalty = -0.5
		case recent7Avg >= 10.0:
			absPenalty = -1.0
		case recent7Avg >= 9.5:
			absPenalty = -0.5
		}

		// Consistency penalty: CV > 15% = inconsistent (Huang et al. 2020)
		cv := sleepCV(recent7)
		consistencyPenalty := 0.0
		if cv > 25 {
			consistencyPenalty = -0.5
		} else if cv > 15 {
			consistencyPenalty = -0.25
		}

		sleepZ := durationZ + absPenalty + consistencyPenalty
		totalZ += sleepZ * wSleep
		components++
	}

	if components == 0 {
		return 70, "", "", 70 // no data → neutral
	}

	// Normalize: totalZ is a weighted sum where weights sum to <1 when
	// not all components are present. Scale up to compensate.
	totalWeight := 0.0
	if len(d.HRV) >= minBaseline+2 {
		totalWeight += wHRV
	}
	if len(d.RHR) >= minBaseline+2 {
		totalWeight += wRHR
	}
	if len(d.Sleep) >= minBaseline+2 {
		totalWeight += wSleep
	}
	if totalWeight > 0 {
		totalZ = totalZ / totalWeight
	}

	s := int(math.Round(zToScore(totalZ)))
	return s, "", "", s
}

// ComputeReadinessScore is the public API for daily_scores backfill.
// Takes pre-sorted (most-recent-first) slices.
func ComputeReadinessScore(hrv, rhr, sleep []float64) int {
	d := RawMetrics{HRV: hrv, RHR: rhr, Sleep: sleep}
	score, _, _, _ := computeReadiness(d)
	return score
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

// safeSlice returns s[from:to] clamped to valid indices.
func safeSlice(s []float64, from, to int) []float64 {
	if from < 0 {
		from = 0
	}
	if to > len(s) {
		to = len(s)
	}
	if from >= to {
		return nil
	}
	return s[from:to]
}
