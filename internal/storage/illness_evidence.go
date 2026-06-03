package storage

import (
	"math"
	"time"

	"health-receiver/internal/health"
)

// BuildIllnessEvidenceInput constructs the date-aligned evidence object for
// the evaluated local date. The health package scores this object; storage
// owns the date alignment and aggregate source summaries.
func (s *DB) BuildIllnessEvidenceInput(date string, checkin *health.SubjectiveCheckinSummary) health.IllnessEvidenceInput {
	loc := reportTZLocation()
	in := health.IllnessEvidenceInput{
		Date:               date,
		RespiratoryRate:    s.respiratoryEvidence(date, loc),
		RHR:                s.channelMetricEvidence(date, ChannelHROvernight, "resting_heart_rate", "bpm", loc),
		HRV:                s.channelMetricEvidence(date, ChannelHRV, "heart_rate_variability", "ms", loc),
		WristTempDeviation: s.channelMetricEvidence(date, ChannelTemp, "wrist_temperature", "degC", loc),
		SubjectiveCheckin:  checkin,
	}
	in.SpO2Average = s.dailyScoreMetricEvidence(date, "blood_oxygen_saturation", "spo2_avg", "%")
	in.SleepDisruption = s.dailyScoreMetricEvidence(date, "sleep_total", "sleep_total", "hr")
	in.SustainedHRLoad = s.sustainedHRLoadEvidence(date)
	in.SpO2LowCluster = s.spO2ClusterEvidence(date)
	in.StressFlags = s.stressFlagsForDate(date)
	in.ObjectivePatternDays = s.objectiveIllnessPatternDays(date)
	return in
}

func (s *DB) respiratoryEvidence(date string, loc *time.Location) *health.MetricEvidenceInput {
	channel := s.channelMetricEvidence(date, ChannelResp, "respiratory_rate", "br/min", loc)
	daily := s.dailyScoreMetricEvidence(date, "respiratory_rate", "resp_avg", "br/min")
	if strongerEvidence(daily, channel, true) {
		daily.Method = "daily_scores_mean_std:resp_avg"
		return daily
	}
	return channel
}

func strongerEvidence(candidate, current *health.MetricEvidenceInput, highIsBad bool) bool {
	if candidate == nil || candidate.Status != "ok" {
		return false
	}
	if current == nil || current.Status != "ok" {
		return true
	}
	if highIsBad {
		return candidate.ZScore > current.ZScore
	}
	return candidate.ZScore < current.ZScore
}

func (s *DB) channelMetricEvidence(date string, channel BaselineChannel, metric, unit string, loc *time.Location) *health.MetricEvidenceInput {
	bl, ok := s.PersonalBaseline(date, channel, 30, loc)
	if !ok {
		return &health.MetricEvidenceInput{Metric: metric, Unit: unit, Method: "personal_baseline_mad", Status: "missing"}
	}
	today, todayOK := s.todayChannelMedian(date, channel, loc)
	if !todayOK {
		return &health.MetricEvidenceInput{Metric: metric, Unit: unit, Method: "personal_baseline_mad", Status: "missing"}
	}
	status := "ok"
	if bl.State == CalibrationWarmup {
		status = "warmup"
	}
	return &health.MetricEvidenceInput{
		Metric: metric, Value: today, Baseline: bl.Median,
		ZScore: (today - bl.Median) / bl.MADSD,
		Unit:   unit, Method: "personal_baseline_mad", Status: status,
	}
}

func (s *DB) dailyScoreMetricEvidence(date, metric, column, unit string) *health.MetricEvidenceInput {
	value, ok := s.dailyScoreValue(date, column)
	if !ok {
		return &health.MetricEvidenceInput{Metric: metric, Unit: unit, Method: "daily_scores_mean_std", Status: "missing"}
	}
	avg, sd, n, ok := s.dailyScoreBaseline(date, column, 30)
	if !ok {
		status := "missing"
		if n > 0 {
			status = "warmup"
		}
		return &health.MetricEvidenceInput{Metric: metric, Value: value, Unit: unit, Method: "daily_scores_mean_std", Status: status}
	}
	z := (value - avg) / sd
	return &health.MetricEvidenceInput{Metric: metric, Value: value, Baseline: avg, ZScore: z, Unit: unit, Method: "daily_scores_mean_std", Status: "ok"}
}

func (s *DB) sustainedHRLoadEvidence(date string) *health.MetricEvidenceInput {
	value, ok := s.dailyScoreValue(date, "sustained_hr_load")
	if !ok {
		return &health.MetricEvidenceInput{Metric: "sustained_hr_load", Method: "sustained_hr_load_z", Status: "missing", ActivityContext: "unknown"}
	}
	return &health.MetricEvidenceInput{
		Metric: "sustained_hr_load", Value: value, Baseline: 0, ZScore: value,
		Method: "sustained_hr_load_z", Status: "ok", ActivityContext: s.activityContextForDate(date),
	}
}

func (s *DB) dailyScoreValue(date, column string) (float64, bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	var v *float64
	err := s.pool.QueryRow(ctx, `SELECT `+column+` FROM daily_scores WHERE date = $1`, date).Scan(&v)
	if err != nil || v == nil || !isFiniteFloat(*v) {
		return 0, false
	}
	return *v, true
}

func (s *DB) dailyScoreBaseline(date, column string, days int) (avg, sd float64, n int, ok bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	if days <= 0 {
		days = 30
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+column+`
		  FROM daily_scores
		 WHERE date >= $1
		   AND date <  $2
		   AND `+column+` IS NOT NULL
		   AND `+column+` > 0`,
		subtractDays(date, days), date)
	if err != nil {
		return 0, 0, 0, false
	}
	defer rows.Close()
	var vals []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err == nil && isFiniteFloat(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) < 7 {
		return 0, 0, len(vals), false
	}
	avg = avgFloat(vals)
	sd = stddevFloat(vals)
	if sd <= 0 {
		return avg, 0, len(vals), false
	}
	return avg, sd, len(vals), true
}

func (s *DB) spO2ClusterEvidence(date string) *health.SpO2ClusterEvidence {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT source, COUNT(*), MIN(qty), AVG(qty),
		       COUNT(*) FILTER (WHERE qty < 94),
		       COUNT(*) FILTER (WHERE qty < 92)
		  FROM metric_points
		 WHERE metric_name = 'blood_oxygen_saturation'
		   AND SUBSTRING(date,1,10) = $1
		   AND qty > 0
		   AND quality = 'ok'
		 GROUP BY source
		 ORDER BY source`, date)
	if err != nil {
		return &health.SpO2ClusterEvidence{Status: "missing"}
	}
	defer rows.Close()
	out := &health.SpO2ClusterEvidence{Status: "ok"}
	var sum float64
	for rows.Next() {
		var src string
		var count, below94, below92 int
		var minV, avgV float64
		if err := rows.Scan(&src, &count, &minV, &avgV, &below94, &below92); err != nil {
			continue
		}
		out.ValidReadings += count
		out.Below94Count += below94
		out.Below92Count += below92
		if out.Min == 0 || minV < out.Min {
			out.Min = minV
		}
		sum += avgV * float64(count)
		out.Sources = append(out.Sources, health.SpO2SourceEvidence{
			Source: src, Count: count, Below94Count: below94, Below92Count: below92,
			Min: minV, Avg: avgV, Window: "unknown",
		})
	}
	if out.ValidReadings == 0 {
		out.Status = "missing"
		return out
	}
	out.Avg = sum / float64(out.ValidReadings)
	return out
}

func (s *DB) stressFlagsForDate(date string) []string {
	ctx, cancel := queryCtx()
	defer cancel()
	var flags []string
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(stress_flags, ARRAY[]::text[]) FROM daily_scores WHERE date = $1`, date).Scan(&flags)
	return flags
}

func (s *DB) activityContextForDate(date string) string {
	steps, stepsOK := s.dailyScoreValue(date, "steps")
	cal, calOK := s.dailyScoreValue(date, "calories")
	stepsBase := s.chronicAvg(date, "steps")
	calBase := s.chronicAvg(date, "calories")
	if !stepsOK && !calOK {
		return "unknown"
	}
	if (stepsOK && stepsBase > 0 && steps > stepsBase*1.25) || (calOK && calBase > 0 && cal > calBase*1.25) {
		return "high"
	}
	return "normal"
}

func (s *DB) objectiveIllnessPatternDays(date string) int {
	days := 0
	for i := 0; i < 3; i++ {
		d := subtractDays(date, i)
		if s.hasObjectiveIllnessPattern(d) {
			days++
		}
	}
	return days
}

func (s *DB) hasObjectiveIllnessPattern(date string) bool {
	loc := reportTZLocation()
	resp := s.respiratoryEvidence(date, loc)
	spo2 := s.dailyScoreMetricEvidence(date, "blood_oxygen_saturation", "spo2_avg", "%")
	rhr := s.channelMetricEvidence(date, ChannelHROvernight, "resting_heart_rate", "bpm", loc)
	hrv := s.channelMetricEvidence(date, ChannelHRV, "heart_rate_variability", "ms", loc)
	temp := s.channelMetricEvidence(date, ChannelTemp, "wrist_temperature", "degC", loc)

	primary := metricHigh(resp, 1.5) || metricLow(spo2, -1.5) || spO2Drop(spo2, 0.7)
	support := metricHigh(rhr, 1.0) || metricLow(hrv, -1.0) || metricHigh(temp, 1.0)
	return primary && support
}

func metricHigh(in *health.MetricEvidenceInput, z float64) bool {
	return in != nil && in.Status == "ok" && in.ZScore >= z
}

func metricLow(in *health.MetricEvidenceInput, z float64) bool {
	return in != nil && in.Status == "ok" && in.ZScore <= z
}

func spO2Drop(in *health.MetricEvidenceInput, drop float64) bool {
	return in != nil && in.Status == "ok" && in.Baseline-in.Value >= drop
}

func avgFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func stddevFloat(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	mean := avgFloat(vals)
	var sum float64
	for _, v := range vals {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(vals)))
}
