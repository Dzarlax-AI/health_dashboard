package health

func scoreCardio(d RawMetrics, ls LangStrings, sectionsByName map[string]string) *BriefingSection {
	if len(d.SpO2) == 0 && len(d.VO2) == 0 && len(d.Resp) == 0 {
		return nil
	}
	sec := &BriefingSection{Key: "cardio", Title: ls["sec_cardio"], Icon: "heart"}
	score, maxScore := 0, 0

	if len(d.SpO2) >= 3 {
		recent := avg(d.SpO2[:min(3, len(d.SpO2))])
		todaySpO2 := d.SpO2[0]
		if sectionsByName == nil {
			maxScore += 2
			if recent >= 95 {
				score += 2
			} else if recent >= 92 {
				score += 1
			}
		}
		note := ls["spo2_note_good"]
		if todaySpO2 < 95 {
			note = ls["spo2_note_low"]
		}
		spo2Trend := "down"
		if todaySpO2 >= 95 {
			spo2Trend = "up"
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_blood_o2"], Value: fmtFloat(todaySpO2, 1) + "%",
			Note: note, Trend: spo2Trend,
		})
	}

	if len(d.VO2) >= 3 {
		recent := avg(d.VO2[:min(3, len(d.VO2))])
		todayVO2 := d.VO2[0]
		baseline := avg(d.VO2)
		pct := pctChange(recent, baseline)
		if sectionsByName == nil {
			maxScore += 2
			if pct > -3 {
				score += 2
			} else if pct > -8 {
				score += 1
			}
		}
		note := ls["vo2_note_stable"]
		if pct > 3 {
			note = ls["vo2_note_good"]
		} else if pct < -5 {
			note = ls["vo2_note_decline"]
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_vo2"], Value: fmtFloat(todayVO2, 1) + " ml/kg/min",
			Note: note, Trend: trend(pct, false),
		})
	}

	if len(d.Resp) >= 3 {
		recent := avg(d.Resp[:min(3, len(d.Resp))])
		todayResp := d.Resp[0]
		if sectionsByName == nil {
			maxScore += 2
			if recent >= 12 && recent <= 20 {
				score += 2
			} else if recent >= 10 && recent <= 24 {
				score += 1
			}
		}
		note := ls["resp_note_normal"]
		if todayResp < 12 || todayResp > 20 {
			note = ls["resp_note_outside"]
		}
		respTrend := "down"
		if todayResp >= 12 && todayResp <= 20 {
			respTrend = "up"
		}
		sec.Details = append(sec.Details, BriefingDetail{
			Label: ls["lbl_resp"], Value: fmtFloat(todayResp, 1) + " br/min",
			Note: note, Trend: respTrend,
		})
	}

	status := sectionsByName["cardio"]
	if sectionsByName == nil && maxScore > 0 {
		ratio := float64(score) / float64(maxScore)
		switch {
		case ratio >= 0.7:
			status = "good"
		case ratio >= 0.4:
			status = "fair"
		default:
			status = "low"
		}
	}
	if status != "" {
		if status == "good" {
			sec.Status = "good"
			sec.Summary = ls["cardio_summary_good"]
		} else if status == "fair" {
			sec.Status = "fair"
			sec.Summary = ls["cardio_summary_fair"]
		} else {
			sec.Status = "low"
			sec.Summary = ls["cardio_summary_low"]
		}
	}
	return sec
}
