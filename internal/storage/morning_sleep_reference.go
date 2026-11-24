package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

// Keep the original reader's bounded history, including its single-day insight slice.
var morningReportNightSQL = `with nights as materialized (
    select * from completed_night_sleep where wake_date between $1 and $2
), latest as (select * from nights order by wake_date desc limit 1),
reference as (` + dbtmetrics.SleepDurationReferenceSQL("nights") + `)
select l.wake_date,l.duration_hours,l.source,l.capture_completeness,l.duration_assessment,
       l.finalization_state,l.input_hash,r.eligible_nights,r.reference_hours
from latest l join reference r using(wake_date)`

func (s *DB) morningReportNight(ctx context.Context, from, through string) (health.MorningReportSleep, *health.CompletedNightSleep, error) {
	result := health.MorningReportSleep{ReportDate: through, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown}
	var n health.CompletedNightSleep
	var count int
	var mean *float64
	err := s.pool.QueryRow(ctx, morningReportNightSQL, from, through).Scan(&n.WakeDate, &n.DurationHours, &n.Source, &n.CaptureCompleteness, &n.DurationAssessment, &n.FinalizationState, &n.InputHash, &count, &mean)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil, nil
	}
	if err != nil {
		return result, nil, err
	}
	result.Date, result.Capture, result.Assessment, result.Finalization, result.InputHash = n.WakeDate, n.CaptureCompleteness, n.DurationAssessment, n.FinalizationState, n.InputHash
	if n.DurationHours > 0 {
		result.Hours = &n.DurationHours
	}
	if result.Capture == health.NightCaptureComplete && result.Assessment == health.NightDurationPlausible {
		result.BaselineHours, result.BaselineNights = mean, count
	}
	return result, &n, nil
}

var recentSleepComparisonSQL = `with recent_inputs as (select $1::text as latest_date,$2::text as report_date,$3::float8 as latest_hours,
    $4::float8 as hours,$5::int as nights,$6::float8 as average_hours), calculated as (` + dbtmetrics.RecentRecordedSleepSQL + `)
    select mean_hours,nights,difference_minutes from calculated`

func (s *DB) attachRecentSleepComparison(ctx context.Context, sleep *health.MorningReportSleep, b *health.BriefingResponse) error {
	if b == nil || b.Sleep == nil || !health.MorningSleepAnalysisEligible(sleep, b.Date) {
		return nil
	}
	var mean float64
	var nights, difference int
	err := s.pool.QueryRow(ctx, recentSleepComparisonSQL, b.Sleep.LatestDate, b.Date, b.Sleep.LatestTotal, sleep.Hours, b.Sleep.Nights, b.Sleep.TotalAvg).Scan(&mean, &nights, &difference)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	sleep.RecentRecordedMeanHours, sleep.RecentRecordedNights, sleep.RecentRecordedDifferenceMinutes = &mean, nights, &difference
	return nil
}
