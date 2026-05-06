package health

import "fmt"

func scoreActivity(d RawMetrics, ls LangStrings) *BriefingSection {
	if len(d.Steps) == 0 && len(d.Cal) == 0 {
		return nil
	}
	sec := &BriefingSection{Key: "activity", Title: ls["sec_activity"], Icon: "activity"}
	score, maxScore := 0, 0

	var stepsToday, stepsRecent float64
	if len(d.Steps) >= 3 {
		stepsRecent = avg(d.Steps[:min(3, len(d.Steps))])
		stepsBase := avg(d.Steps)
		pct := pctChange(stepsRecent, stepsBase)
		maxScore += 2
		if pct > -10 {
			score += 2
		} else if pct > -30 {
			score += 1
		}
		// Display today's value, score uses 3-day avg.
		stepsToday = d.Steps[0]
		note := ls["steps_note_normal"]
		if pct > 10 {
			note = ls["steps_note_good"]
		} else if pct < -20 {
			note = ls["steps_note_low"]
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_steps"], Value: fmt.Sprintf(ls["unit_steps_day"], fmtFloat(stepsToday, 0)),
			Note: note, Trend: trend(pct, false),
		})
	}

	if len(d.Cal) >= 3 {
		calRecent := avg(d.Cal[:min(3, len(d.Cal))])
		calBase := avg(d.Cal)
		pct := pctChange(calRecent, calBase)
		maxScore += 2
		if pct > -10 {
			score += 2
		} else if pct > -30 {
			score += 1
		}
		calNote := ls["cal_note_normal"]
		if pct > 10 {
			calNote = ls["cal_note_high"]
		} else if pct < -15 {
			calNote = ls["cal_note_low"]
		}
		// Display today's value.
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_active_cal"], Value: fmtFloat(d.Cal[0], 0) + " kcal",
			Note: calNote, Trend: trend(pct, false),
		})
	}

	if len(d.Exercise) >= 3 {
		exRecent := avg(d.Exercise[:min(3, len(d.Exercise))])
		maxScore += 2
		if exRecent >= 30 {
			score += 2
		} else if exRecent >= 15 {
			score += 1
		}
		exNote := ls["ex_note_low"]
		if exRecent >= 30 {
			exNote = ls["ex_note_good"]
		}
		exTrend := "down"
		if exRecent >= 30 {
			exTrend = "up"
		} else if exRecent >= 15 {
			exTrend = "stable"
		}
		// Display today's value.
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_exercise"], Value: fmt.Sprintf(ls["unit_min_day"], fmtFloat(d.Exercise[0], 0)),
			Note: exNote, Trend: exTrend,
		})
	}

	if maxScore > 0 {
		ratio := float64(score) / float64(maxScore)
		// Summary text says "averaging X steps" — feed it the 3-day average,
		// not today's count. Falls back to today only if we never set stepsRecent
		// (no steps data was available, which can't happen here since maxScore>0
		// implies at least one branch ran, but guard anyway).
		stepsForLabel := stepsRecent
		if stepsForLabel == 0 {
			stepsForLabel = stepsToday
		}
		stepsLabel := fmt.Sprintf("%.0f", stepsForLabel)
		if stepsForLabel >= 1000 {
			stepsLabel = formatNumber(int(stepsForLabel))
		}
		if ratio >= 0.7 {
			sec.Status = "good"
			sec.Summary = fmt.Sprintf(ls["act_summary_good"], stepsLabel)
		} else if ratio >= 0.4 {
			sec.Status = "fair"
			sec.Summary = fmt.Sprintf(ls["act_summary_fair"], stepsLabel)
		} else {
			sec.Status = "low"
			sec.Summary = fmt.Sprintf(ls["act_summary_low"], stepsLabel)
		}
	}
	return sec
}
