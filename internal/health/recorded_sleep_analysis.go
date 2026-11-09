package health

import "math"

// RecordedSleepAggregate exposes the existing current-date, per-source rollup
// only when its date, source and duration agree with the canonical observation.
// It never substitutes the rolling stage averages for the current night.
func RecordedSleepAggregate(b *BriefingResponse, date, source string, hours float64) *MorningSleepEpisode {
	if math.IsNaN(hours) || math.IsInf(hours, 0) || hours <= 0 || hours > 24 {
		return nil
	}
	if b == nil || b.Date != date || b.Sleep == nil || b.Sleep.LatestDate != date || b.Sleep.LatestTotal == nil || math.Abs(*b.Sleep.LatestTotal-hours)*60 > 1 {
		return nil
	}
	for _, s := range b.Sleep.Sources {
		valid := true
		for _, v := range []float64{s.Total, s.Deep, s.REM, s.Core, s.Unspecified, s.Awake} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 24 {
				valid = false
			}
		}
		if !valid {
			continue
		}
		if s.Source != source || math.Abs(s.Total-hours)*60 > 1 || math.Abs(s.Deep+s.REM+s.Core+s.Unspecified-s.Total)*60 > 1 {
			continue
		}
		e := &MorningSleepEpisode{StageStatus: "recorded_aggregate", AsleepMinutes: int(hours*60 + 0.5)}
		for value, target := range map[*float64]**int{&s.Deep: &e.DeepMinutes, &s.REM: &e.REMMinutes, &s.Core: &e.CoreMinutes, &s.Unspecified: &e.UnspecifiedMinutes, &s.Awake: &e.AwakeMinutes} {
			if *value > 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0) {
				minutes := int(*value*60 + 0.5)
				*target = &minutes
			}
		}
		return e
	}
	return nil
}

func AttachRecentRecordedSleepComparison(sleep *MorningReportSleep, b *BriefingResponse) {
	if sleep == nil || b == nil || b.Sleep == nil || !MorningSleepAnalysisEligible(sleep, b.Date) || b.Sleep.LatestDate != b.Date || b.Sleep.LatestTotal == nil || math.Abs(*b.Sleep.LatestTotal-*sleep.Hours)*60 > 1 || b.Sleep.Nights < 2 || math.IsNaN(b.Sleep.TotalAvg) || math.IsInf(b.Sleep.TotalAvg, 0) || b.Sleep.TotalAvg <= 0 || b.Sleep.TotalAvg > 24 {
		return
	}
	mean := math.Round(b.Sleep.TotalAvg*100) / 100
	difference := int(math.Round((*sleep.Hours - mean) * 60))
	sleep.RecentRecordedMeanHours, sleep.RecentRecordedNights, sleep.RecentRecordedDifferenceMinutes = &mean, b.Sleep.Nights, &difference
}
