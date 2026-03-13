package health

import (
	"fmt"
	"math"
)

// ComputeBriefing calculates all health scores and insights from pre-fetched raw metrics.
// It is a pure function — no I/O, all inputs come from RawMetrics.
// lang selects the output language ("en", "ru", "sr"); defaults to "en".
func ComputeBriefing(d RawMetrics, lang string) *BriefingResponse {
	ls := GetStrings(lang)

	avg := func(vals []float64) float64 {
		if len(vals) == 0 {
			return 0
		}
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals))
	}

	pctChange := func(recent, baseline float64) float64 {
		if baseline == 0 {
			return 0
		}
		return (recent - baseline) / baseline * 100
	}

	trend := func(pct float64, invertBetter bool) string {
		if invertBetter {
			pct = -pct
		}
		if pct > 3 {
			return "up"
		}
		if pct < -3 {
			return "down"
		}
		return "stable"
	}

	fmtFloat := func(v float64, decimals int) string {
		if decimals == 0 {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%.*f", decimals, v)
	}

	// ---- Recovery ----
	var recoverySec *BriefingSection
	if len(d.HRV) > 0 || len(d.RHR) > 0 {
		recoverySec = &BriefingSection{Key: "recovery", Title: ls["sec_recovery"], Icon: "battery"}
		score, maxScore := 0, 0

		if len(d.HRV) >= 3 {
			recent := avg(d.HRV[:min(3, len(d.HRV))])
			baseline := avg(d.HRV)
			pct := pctChange(recent, baseline)
			t := trend(pct, false)
			if pct > 5 {
				score += 2
			} else if pct > -5 {
				score += 1
			}
			maxScore += 2
			note := ls["hrv_note_stable"]
			if pct > 5 {
				note = ls["hrv_note_good"]
			} else if pct < -5 {
				note = ls["hrv_note_low"]
			}
			recoverySec.Details = append(recoverySec.Details, BriefingDetail{
				Label: ls["lbl_hrv"], Value: fmtFloat(recent, 0) + " ms", Note: note, Trend: t,
			})
		}

		if len(d.RHR) >= 3 {
			recent := avg(d.RHR[:min(3, len(d.RHR))])
			baseline := avg(d.RHR)
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
			recoverySec.Details = append(recoverySec.Details, BriefingDetail{
				Label: ls["lbl_resting_hr"], Value: fmtFloat(recent, 0) + " bpm", Note: note, Trend: t,
			})
		}

		if maxScore > 0 {
			ratio := float64(score) / float64(maxScore)
			if ratio >= 0.75 {
				recoverySec.Status = "good"
				recoverySec.Summary = ls["rec_summary_good"]
			} else if ratio >= 0.4 {
				recoverySec.Status = "fair"
				recoverySec.Summary = ls["rec_summary_fair"]
			} else {
				recoverySec.Status = "low"
				recoverySec.Summary = ls["rec_summary_low"]
			}
		}
	}

	// ---- Sleep ----
	var sleepSec *BriefingSection
	if len(d.Sleep) > 0 {
		sleepSec = &BriefingSection{Key: "sleep", Title: ls["sec_sleep"], Icon: "moon"}
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

		if score >= 5 {
			sleepSec.Status = "good"
			sleepSec.Summary = fmt.Sprintf(ls["sleep_summary_good"], recent)
		} else if score >= 3 {
			sleepSec.Status = "fair"
			sleepSec.Summary = fmt.Sprintf(ls["sleep_summary_fair"], recent)
		} else {
			sleepSec.Status = "low"
			sleepSec.Summary = fmt.Sprintf(ls["sleep_summary_low"], recent)
		}

		t := trend(pct, false)
		durationNote := ls["sleep_dur_stable"]
		if pct > 5 {
			durationNote = ls["sleep_dur_more"]
		} else if pct < -5 {
			durationNote = ls["sleep_dur_less"]
		}
		sleepSec.Details = append(sleepSec.Details, BriefingDetail{
			Label: ls["lbl_duration"], Value: fmt.Sprintf(ls["unit_hrs_night"], recent), Note: durationNote, Trend: t,
		})

		if deepPct > 0 {
			dNote := ls["sleep_deep_good"]
			if deepPct < 15 {
				dNote = ls["sleep_deep_low"]
			}
			sleepSec.Details = append(sleepSec.Details, BriefingDetail{
				Label: ls["lbl_deep_sleep"], Value: fmt.Sprintf(ls["unit_pct_total"], deepPct), Note: dNote,
				Trend: func() string {
					if deepPct >= 15 {
						return "up"
					}
					return "down"
				}(),
			})
		}

		if len(d.REM) >= 3 && recent > 0 {
			recentRem := avg(d.REM[:min(3, len(d.REM))])
			remPct := recentRem / recent * 100
			rNote := ls["sleep_rem_good"]
			if remPct < 20 {
				rNote = ls["sleep_rem_low"]
			}
			sleepSec.Details = append(sleepSec.Details, BriefingDetail{
				Label: ls["lbl_rem"], Value: fmt.Sprintf(ls["unit_pct_total"], remPct), Note: rNote,
				Trend: func() string {
					if remPct >= 20 {
						return "up"
					}
					return "stable"
				}(),
			})
		}
	}

	// ---- Activity ----
	var activitySec *BriefingSection
	if len(d.Steps) > 0 || len(d.Cal) > 0 {
		activitySec = &BriefingSection{Key: "activity", Title: ls["sec_activity"], Icon: "activity"}
		score, maxScore := 0, 0

		var stepsRecent float64
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
			note := ls["steps_note_normal"]
			if pct > 10 {
				note = ls["steps_note_good"]
			} else if pct < -20 {
				note = ls["steps_note_low"]
			}
			activitySec.Details = append(activitySec.Details, BriefingDetail{
				Label: ls["lbl_steps"], Value: fmt.Sprintf(ls["unit_steps_day"], fmtFloat(stepsRecent, 0)),
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
			activitySec.Details = append(activitySec.Details, BriefingDetail{
				Label: ls["lbl_active_cal"], Value: fmtFloat(calRecent, 0) + " kcal",
				Note: func() string {
					if pct > 10 {
						return ls["cal_note_high"]
					}
					if pct < -15 {
						return ls["cal_note_low"]
					}
					return ls["cal_note_normal"]
				}(), Trend: trend(pct, false),
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
			activitySec.Details = append(activitySec.Details, BriefingDetail{
				Label: ls["lbl_exercise"], Value: fmt.Sprintf(ls["unit_min_day"], fmtFloat(exRecent, 0)),
				Note: func() string {
					if exRecent >= 30 {
						return ls["ex_note_good"]
					}
					return ls["ex_note_low"]
				}(),
				Trend: func() string {
					if exRecent >= 30 {
						return "up"
					}
					if exRecent >= 15 {
						return "stable"
					}
					return "down"
				}(),
			})
		}

		if maxScore > 0 {
			ratio := float64(score) / float64(maxScore)
			stepsLabel := fmt.Sprintf("%.0f", stepsRecent)
			if stepsRecent >= 1000 {
				stepsLabel = formatNumber(int(stepsRecent))
			}
			if ratio >= 0.7 {
				activitySec.Status = "good"
				activitySec.Summary = fmt.Sprintf(ls["act_summary_good"], stepsLabel)
			} else if ratio >= 0.4 {
				activitySec.Status = "fair"
				activitySec.Summary = fmt.Sprintf(ls["act_summary_fair"], stepsLabel)
			} else {
				activitySec.Status = "low"
				activitySec.Summary = fmt.Sprintf(ls["act_summary_low"], stepsLabel)
			}
		}
	}

	// ---- Heart & Lungs ----
	var cardioSec *BriefingSection
	if len(d.SpO2) > 0 || len(d.VO2) > 0 || len(d.Resp) > 0 {
		cardioSec = &BriefingSection{Key: "cardio", Title: ls["sec_cardio"], Icon: "heart"}
		score, maxScore := 0, 0

		if len(d.SpO2) >= 3 {
			recent := avg(d.SpO2[:min(3, len(d.SpO2))])
			maxScore += 2
			if recent >= 95 {
				score += 2
			} else if recent >= 92 {
				score += 1
			}
			note := ls["spo2_note_good"]
			if recent < 95 {
				note = ls["spo2_note_low"]
			}
			cardioSec.Details = append(cardioSec.Details, BriefingDetail{
				Label: ls["lbl_blood_o2"], Value: fmtFloat(recent, 1) + "%", Note: note,
				Trend: func() string {
					if recent >= 95 {
						return "up"
					}
					return "down"
				}(),
			})
		}

		if len(d.VO2) >= 3 {
			recent := avg(d.VO2[:min(3, len(d.VO2))])
			baseline := avg(d.VO2)
			pct := pctChange(recent, baseline)
			maxScore += 2
			if pct > -3 {
				score += 2
			} else if pct > -8 {
				score += 1
			}
			note := ls["vo2_note_stable"]
			if pct > 3 {
				note = ls["vo2_note_good"]
			} else if pct < -5 {
				note = ls["vo2_note_decline"]
			}
			cardioSec.Details = append(cardioSec.Details, BriefingDetail{
				Label: ls["lbl_vo2"], Value: fmtFloat(recent, 1) + " ml/kg/min", Note: note, Trend: trend(pct, false),
			})
		}

		if len(d.Resp) >= 3 {
			recent := avg(d.Resp[:min(3, len(d.Resp))])
			maxScore += 2
			if recent >= 12 && recent <= 20 {
				score += 2
			} else if recent >= 10 && recent <= 24 {
				score += 1
			}
			note := ls["resp_note_normal"]
			if recent < 12 || recent > 20 {
				note = ls["resp_note_outside"]
			}
			cardioSec.Details = append(cardioSec.Details, BriefingDetail{
				Label: ls["lbl_resp"], Value: fmtFloat(recent, 1) + " br/min", Note: note,
				Trend: func() string {
					if recent >= 12 && recent <= 20 {
						return "up"
					}
					return "down"
				}(),
			})
		}

		if maxScore > 0 {
			ratio := float64(score) / float64(maxScore)
			if ratio >= 0.7 {
				cardioSec.Status = "good"
				cardioSec.Summary = ls["cardio_summary_good"]
			} else if ratio >= 0.4 {
				cardioSec.Status = "fair"
				cardioSec.Summary = ls["cardio_summary_fair"]
			} else {
				cardioSec.Status = "low"
				cardioSec.Summary = ls["cardio_summary_low"]
			}
		}
	}

	// ---- Readiness Score ----
	readinessScore := 50
	{
		hrvScore := 100.0
		if len(d.HRV) >= 3 {
			recent := avg(d.HRV[:min(3, len(d.HRV))])
			baseline := avg(d.HRV)
			pct := pctChange(recent, baseline)
			if pct < -3 {
				hrvScore = math.Max(0, math.Min(100, 100+(pct+3)*6))
			}
		}

		rhrScore := 100.0
		if len(d.RHR) >= 3 {
			recent := avg(d.RHR[:min(3, len(d.RHR))])
			baseline := avg(d.RHR)
			pct := pctChange(recent, baseline)
			if pct > 3 {
				rhrScore = math.Max(0, math.Min(100, 100-(pct-3)*6))
			}
		}

		sleepScore := 100.0
		if len(d.Sleep) >= 3 {
			recent := avg(d.Sleep[:min(3, len(d.Sleep))])
			baseline := avg(d.Sleep)
			pct := pctChange(recent, baseline)
			if pct < -5 {
				sleepScore = math.Max(0, math.Min(100, 100+(pct+5)*5))
			}
			if recent < 5.5 {
				sleepScore = math.Min(sleepScore, 60)
			}
		}

		readinessScore = int(math.Round(hrvScore*0.4 + rhrScore*0.3 + sleepScore*0.3))
		if readinessScore > 100 {
			readinessScore = 100
		}
		if readinessScore < 0 {
			readinessScore = 0
		}
	}

	readinessLabel := ls["readiness_low"]
	if readinessScore >= 80 {
		readinessLabel = ls["readiness_optimal"]
	} else if readinessScore >= 50 {
		readinessLabel = ls["readiness_fair"]
	}

	readinessTip := ""
	switch readinessScore >= 80 {
	case true:
		readinessTip = ls["tip_optimal"]
	default:
		if readinessScore >= 50 {
			readinessTip = ls["tip_fair"]
		} else {
			readinessTip = ls["tip_low"]
		}
	}

	recoveryPct := readinessScore

	// ---- Correlation data ----
	var correlation []CorrelationPoint
	{
		maxSteps := 1.0
		for _, s := range d.StepsWithDates {
			if s.Val > maxSteps {
				maxSteps = s.Val
			}
		}
		hrvByDate := make(map[string]float64)
		for _, h := range d.HRVWithDates {
			hrvByDate[h.Date] = h.Val
		}
		for _, s := range d.StepsWithDates {
			if h, ok := hrvByDate[s.Date]; ok {
				correlation = append(correlation, CorrelationPoint{
					Date: s.Date,
					Load: math.Round(s.Val/maxSteps*100*10) / 10,
					HRV:  math.Round(h*10) / 10,
				})
			}
		}
	}

	// ---- Insights ----
	var insights []Insight
	{
		if len(d.Steps) >= 7 {
			stepsAvg := avg(d.Steps)
			aboveCount := 0
			checkDays := min(7, len(d.Steps))
			for i := 0; i < checkDays; i++ {
				if d.Steps[i] >= stepsAvg {
					aboveCount++
				}
			}
			if aboveCount >= 5 {
				insights = append(insights, Insight{
					Text: fmt.Sprintf(ls["insight_steps_good"], aboveCount),
					Type: "positive",
				})
			} else {
				insights = append(insights, Insight{
					Text: fmt.Sprintf(ls["insight_steps_low"], aboveCount),
					Type: "warning",
				})
			}
		}

		if len(d.Steps) >= 3 && len(d.HRV) >= 3 {
			stepsAvg := avg(d.Steps)
			highActivityLowHRV := 0
			highActivityDays := 0
			checkLen := min(len(d.Steps), len(d.HRV)) - 1
			for i := 0; i < checkLen; i++ {
				if d.Steps[i+1] > stepsAvg*1.2 {
					highActivityDays++
					if d.HRV[i] < avg(d.HRV)*0.95 {
						highActivityLowHRV++
					}
				}
			}
			if highActivityDays >= 2 && highActivityLowHRV > highActivityDays/2 {
				insights = append(insights, Insight{
					Text: ls["insight_hrv_drop"],
					Type: "warning",
				})
			} else if highActivityDays >= 2 {
				insights = append(insights, Insight{
					Text: ls["insight_hrv_resilient"],
					Type: "positive",
				})
			}
		}

		if len(d.Steps) >= 7 && len(d.Sleep) >= 7 {
			stepsAvg := avg(d.Steps)
			var sleepOnActive, sleepOnRest []float64
			checkLen := min(len(d.Steps), len(d.Sleep))
			for i := 0; i < checkLen; i++ {
				if d.Steps[i] > stepsAvg {
					sleepOnActive = append(sleepOnActive, d.Sleep[i])
				} else {
					sleepOnRest = append(sleepOnRest, d.Sleep[i])
				}
			}
			if len(sleepOnActive) > 0 && len(sleepOnRest) > 0 {
				activeSleepAvg := avg(sleepOnActive)
				restSleepAvg := avg(sleepOnRest)
				if activeSleepAvg > restSleepAvg+0.5 {
					insights = append(insights, Insight{
						Text: fmt.Sprintf(ls["insight_sleep_active"], activeSleepAvg, restSleepAvg),
						Type: "positive",
					})
				} else if restSleepAvg > activeSleepAvg+0.5 {
					insights = append(insights, Insight{
						Text: fmt.Sprintf(ls["insight_sleep_rest"], restSleepAvg, activeSleepAvg),
						Type: "warning",
					})
				}
			}
		}

		if activitySec != nil && activitySec.Status == "good" && readinessScore < 50 {
			insights = append(insights, Insight{
				Text: ls["insight_overtrain"],
				Type: "warning",
			})
		}

		if len(insights) > 3 {
			insights = insights[:3]
		}
	}

	// ---- Sleep Analysis ----
	var sleepAnalysis *SleepAnalysis
	if len(d.Sleep) >= 3 {
		sa := SleepAnalysis{}
		recentSleep := avg(d.Sleep[:min(3, len(d.Sleep))])
		if len(d.Deep) >= 3 {
			sa.DeepAvg = math.Round(avg(d.Deep[:min(3, len(d.Deep))])*100) / 100
		}
		if len(d.REM) >= 3 {
			sa.REMAvg = math.Round(avg(d.REM[:min(3, len(d.REM))])*100) / 100
		}
		if len(d.Awake) >= 3 {
			sa.AwakeAvg = math.Round(avg(d.Awake[:min(3, len(d.Awake))])*100) / 100
		}
		if recentSleep > 0 {
			sa.Efficiency = math.Round((recentSleep-sa.AwakeAvg)/recentSleep*100*10) / 10
			if sa.Efficiency < 0 {
				sa.Efficiency = 0
			}
		}
		sleepAnalysis = &sa
	}

	// ---- Metric Cards ----
	type cardSpec struct {
		name    string
		unit    string
		vals    []float64
		decimal int
	}
	var metricCards []MetricCard
	for _, sp := range []cardSpec{
		{"Steps", "steps", d.Steps, 0},
		{"Sleep", "hrs", d.Sleep, 1},
		{"HRV", "ms", d.HRV, 0},
		{"Resting HR", "bpm", d.RHR, 0},
		{"Respiratory Rate", "br/min", d.Resp, 1},
	} {
		if len(sp.vals) < 3 {
			continue
		}
		recent := avg(sp.vals[:min(3, len(sp.vals))])
		baseline := avg(sp.vals)
		pct := pctChange(recent, baseline)
		tLabel := "stable"
		if pct > 3 {
			tLabel = "up"
		} else if pct < -3 {
			tLabel = "down"
		}
		metricCards = append(metricCards, MetricCard{
			Name:       sp.name,
			Value:      fmtFloat(recent, sp.decimal),
			Unit:       sp.unit,
			TrendPct:   math.Round(pct*10) / 10,
			TrendLabel: tLabel,
		})
	}

	// ---- Assemble sections ----
	var sections []BriefingSection
	for _, sec := range []*BriefingSection{recoverySec, sleepSec, activitySec, cardioSec} {
		if sec != nil {
			sections = append(sections, *sec)
		}
	}

	// ---- Overall status ----
	overall := "good"
	goodCount, fairCount, lowCount := 0, 0, 0
	for _, s := range sections {
		switch s.Status {
		case "good":
			goodCount++
		case "fair":
			fairCount++
		case "low":
			lowCount++
		}
	}
	if lowCount >= 2 {
		overall = "low"
	} else if fairCount+lowCount > goodCount {
		overall = "fair"
	}

	// ---- Highlights ----
	var highlights []BriefingDetail
	if len(d.Steps) > 0 {
		recent := avg(d.Steps[:min(3, len(d.Steps))])
		highlights = append(highlights, BriefingDetail{
			Label: ls["lbl_steps"], Value: formatNumber(int(recent)),
		})
	}
	if len(d.Sleep) > 0 {
		recent := avg(d.Sleep[:min(3, len(d.Sleep))])
		highlights = append(highlights, BriefingDetail{
			Label: ls["sec_sleep"], Value: fmtFloat(recent, 1) + "h",
		})
	}
	if len(d.RHR) > 0 {
		recent := avg(d.RHR[:min(3, len(d.RHR))])
		highlights = append(highlights, BriefingDetail{
			Label: ls["lbl_resting_hr"], Value: fmtFloat(recent, 0) + " bpm",
		})
	}
	if len(d.Cal) > 0 {
		recent := avg(d.Cal[:min(3, len(d.Cal))])
		highlights = append(highlights, BriefingDetail{
			Label: ls["lbl_active_cal"], Value: formatNumber(int(recent)) + " kcal",
		})
	}

	return &BriefingResponse{
		Date:           d.LastDate,
		Greeting:       "Here's your health summary",
		Overall:        overall,
		Sections:       sections,
		Highlights:     highlights,
		ReadinessScore: readinessScore,
		ReadinessLabel: readinessLabel,
		ReadinessTip:   readinessTip,
		RecoveryPct:    recoveryPct,
		Correlation:    correlation,
		Insights:       insights,
		Sleep:          sleepAnalysis,
		MetricCards:    metricCards,
	}
}

func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%d,%03d", n/1000, n%1000)
}
