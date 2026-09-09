package storage

import (
	"time"
)

// WakeCandidateVariants is a read-only diagnostic comparison used by
// cmd/wake_detection_probe while wake-v1 is being calibrated.
type WakeCandidateVariants struct {
	SleepTotalEnd      time.Time
	DetailedSessionEnd time.Time
	SummarySessionEnd  time.Time
	SelectedSource     string
	DetailedRows       int
}

type wakeProbeRow struct {
	Metric     string
	Start      time.Time
	Hours      float64
	Source     string
	ReceivedAt time.Time
}

func (s *DB) WakeCandidateVariantsForDate(localDate string, loc *time.Location) (WakeCandidateVariants, error) {
	target, err := time.ParseInLocation("2006-01-02", localDate, loc)
	if err != nil {
		return WakeCandidateVariants{}, err
	}
	from := target.AddDate(0, 0, -1).Format("2006-01-02")
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT metric_name, date, qty, source, received_at
		  FROM metric_points
		 WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
		   AND quality='ok'
		   AND qty > 0
		   AND SUBSTRING(date,1,10) BETWEEN $1 AND $2
		 ORDER BY date, metric_name
	`, from, localDate)
	if err != nil {
		return WakeCandidateVariants{}, err
	}
	defer rows.Close()
	var input []wakeProbeRow
	totals := map[string]float64{}
	for rows.Next() {
		var metric, dateValue, source string
		var hours float64
		var receivedAt time.Time
		if err := rows.Scan(&metric, &dateValue, &hours, &source, &receivedAt); err != nil {
			return WakeCandidateVariants{}, err
		}
		start, err := parseMetricDate(dateValue)
		if err != nil {
			continue
		}
		input = append(input, wakeProbeRow{Metric: metric, Start: start, Hours: hours, Source: source, ReceivedAt: receivedAt})
		if metric == "sleep_total" {
			totals[source] += hours
		}
	}
	if err := rows.Err(); err != nil {
		return WakeCandidateVariants{}, err
	}
	source := pickWinningSource(totals)
	if source == "" {
		return WakeCandidateVariants{}, nil
	}
	var out WakeCandidateVariants
	out.SelectedSource = source
	type summary struct {
		start time.Time
		sleep float64
		awake float64
	}
	summaries := map[string]*summary{}
	for _, row := range input {
		if row.Source != source {
			continue
		}
		end := row.Start.Add(time.Duration(row.Hours * float64(time.Hour)))
		endLocal := end.In(loc)
		if endLocal.Format("2006-01-02") == localDate && endLocal.Hour() >= 3 && endLocal.Hour() <= 15 {
			if row.Metric == "sleep_total" && end.After(out.SleepTotalEnd) {
				out.SleepTotalEnd = end
			}
			if row.Metric != "sleep_total" && row.Start.In(loc).Format("15:04:05") != "00:00:00" {
				out.DetailedRows++
				if end.After(out.DetailedSessionEnd) {
					out.DetailedSessionEnd = end
				}
			}
		}
		if row.Start.In(loc).Format("15:04:05") == "00:00:00" {
			key := row.Start.Format(time.RFC3339Nano)
			if summaries[key] == nil {
				summaries[key] = &summary{start: row.Start}
			}
			switch row.Metric {
			case "sleep_total":
				summaries[key].sleep += row.Hours
			case "sleep_awake":
				summaries[key].awake += row.Hours
			}
		}
	}
	for _, summary := range summaries {
		if summary.sleep <= 0 {
			continue
		}
		end := summary.start.Add(time.Duration((summary.sleep + summary.awake) * float64(time.Hour)))
		localEnd := end.In(loc)
		if localEnd.Format("2006-01-02") == localDate && localEnd.Hour() >= 3 && localEnd.Hour() <= 15 && end.After(out.SummarySessionEnd) {
			out.SummarySessionEnd = end
		}
	}
	if out.SleepTotalEnd.IsZero() && out.DetailedSessionEnd.IsZero() && out.SummarySessionEnd.IsZero() {
		return out, nil
	}
	return out, nil
}
