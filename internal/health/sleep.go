package health

import (
	"fmt"
	"math"
)

func scoreSleep(d RawMetrics, ls LangStrings) *BriefingSection {
	if len(d.Sleep) == 0 {
		return nil
	}
	sec := &BriefingSection{Key: "sleep", Title: ls["sec_sleep"], Icon: "moon"}
	recent := avg(d.Sleep[:min(3, len(d.Sleep))])
	baseline := avg(d.Sleep)
	pct := pctChange(recent, baseline)

	score := 0
	if recent >= 7 {
		score += 3
	} else if recent >= 6 {
		score += 2
	} else if recent >= 5 {
		score += 1
	}

	deepPct := 0.0
	if len(d.Deep) >= 3 && recent > 0 {
		recentDeep := avg(d.Deep[:min(3, len(d.Deep))])
		deepPct = recentDeep / recent * 100
		if deepPct >= 15 {
			score += 2
		} else if deepPct >= 10 {
			score += 1
		}
	}

	if len(d.Awake) >= 3 {
		recentAwake := avg(d.Awake[:min(3, len(d.Awake))])
		if recentAwake < 0.5 {
			score++
		}
	}

	// Sleep regularity: stddev of nightly duration over available window.
	// Low variability (≤0.5h SD) earns +1 point.
	// Motivated by PMC10782501 (2024): sleep regularity predicts all-cause
	// mortality more strongly than mean duration.
	sleepSD := 0.0
	if len(d.Sleep) >= 7 {
		sleepSD = stddev(d.Sleep)
		if sleepSD <= 0.5 {
			score++
		}
	}

	// Display values use today (index 0); scoring uses 3-day avg above.
	todaySleep := d.Sleep[0]

	if score >= 5 {
		sec.Status = "good"
		sec.Summary = fmt.Sprintf(ls["sleep_summary_good"], todaySleep)
	} else if score >= 3 {
		sec.Status = "fair"
		sec.Summary = fmt.Sprintf(ls["sleep_summary_fair"], todaySleep)
	} else {
		sec.Status = "low"
		sec.Summary = fmt.Sprintf(ls["sleep_summary_low"], todaySleep)
	}

	t := trend(pct, false)
	durationNote := ls["sleep_dur_stable"]
	if pct > 5 {
		durationNote = ls["sleep_dur_more"]
	} else if pct < -5 {
		durationNote = ls["sleep_dur_less"]
	}
	sec.Details = append(sec.Details, BriefingDetail{
		Label: ls["lbl_duration"], Value: fmt.Sprintf(ls["unit_hrs_night"], todaySleep), Note: durationNote, Trend: t,
	})

	// Deep/REM percentages: today's values relative to today's total.
	todayDeepPct := 0.0
	if len(d.Deep) > 0 && todaySleep > 0 {
		todayDeepPct = d.Deep[0] / todaySleep * 100
	}
	if deepPct > 0 {
		dNote := ls["sleep_deep_good"]
		if todayDeepPct < 15 {
			dNote = ls["sleep_deep_low"]
		}
		deepTrend := "down"
		if todayDeepPct >= 15 {
			deepTrend = "up"
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_deep_sleep"], Value: fmt.Sprintf(ls["unit_pct_total"], todayDeepPct),
			Note: dNote, Trend: deepTrend,
		})
	}

	todayRemPct := 0.0
	if len(d.REM) > 0 && todaySleep > 0 {
		todayRemPct = d.REM[0] / todaySleep * 100
		rNote := ls["sleep_rem_good"]
		if todayRemPct < 20 {
			rNote = ls["sleep_rem_low"]
		}
		remTrend := "stable"
		if todayRemPct >= 20 {
			remTrend = "up"
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_rem"], Value: fmt.Sprintf(ls["unit_pct_total"], todayRemPct),
			Note: rNote, Trend: remTrend,
		})
	}

	if len(d.Sleep) >= 7 {
		regNote := ls["sleep_reg_moderate"]
		regTrend := "stable"
		if sleepSD <= 0.5 {
			regNote = ls["sleep_reg_regular"]
			regTrend = "up"
		} else if sleepSD > 1.0 {
			regNote = ls["sleep_reg_irregular"]
			regTrend = "down"
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_sleep_regularity"], Value: fmt.Sprintf("±%.1fh", sleepSD),
			Note: regNote, Trend: regTrend,
		})
	}
	return sec
}

func computeSleepAnalysis(d RawMetrics) *SleepAnalysis {
	if len(d.Sleep) == 0 {
		return nil
	}
	sa := SleepAnalysis{}

	// Weekly summary: use up to 7 most-recent nights, skip zeros (no data).
	const window = 7
	n := min(window, len(d.Sleep))
	var validNights int
	var totalSum float64
	for i := 0; i < n; i++ {
		if d.Sleep[i] > 0 {
			validNights++
			totalSum += d.Sleep[i]
		}
	}
	sa.Nights = validNights
	if validNights > 0 {
		sa.TotalAvg = math.Round(totalSum/float64(validNights)*100) / 100
		if len(d.Deep) > 0 {
			sa.DeepAvg = math.Round(avg(d.Deep[:min(n, len(d.Deep))])*100) / 100
		}
		if len(d.REM) > 0 {
			sa.REMAvg = math.Round(avg(d.REM[:min(n, len(d.REM))])*100) / 100
		}
		if len(d.Awake) > 0 {
			sa.AwakeAvg = math.Round(avg(d.Awake[:min(n, len(d.Awake))])*100) / 100
		}
	}

	// Efficiency based on last night only.
	todaySleep := d.Sleep[0]
	if todaySleep > 0 && len(d.Awake) > 0 {
		awakeTonight := d.Awake[0]
		sa.Efficiency = math.Round((todaySleep-awakeTonight)/todaySleep*100*10) / 10
		if sa.Efficiency < 0 {
			sa.Efficiency = 0
		}
	}
	return &sa
}
