package storage

import "github.com/jackc/pgx/v5"

// Published hours include observed v2 zeros. A daily-only historical summary
// does not establish a new observation date for the current briefing.
const latestBriefingDateSQL = "select substring(hour,1,10) from hourly_metrics order by hour desc limit 1"

var stepsOnDateQuery = physicalStepTotalsSQL("substring(date,1,10)=$1", "hourly_metrics", "substring(hour,1,10)=$1")

// Combine selected physical hours only after source and UTC-offset resolution.
// The public hour chart has one label per wall hour, even on a clock change.
func (s *DB) stepChart(from, to, bucket string) ([]DataPoint, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	pointScope, hourScope := "date >= $1 and date <= $2", "hour >= $1 and hour <= $2"
	if _, bound := compatibilityDateWindow(to); bound != to {
		to = bound
		pointScope, hourScope = "date >= $1 and date < $2", "hour >= $1 and hour < $2"
	}
	period := "hour"
	if bucket == "day" {
		period = "substring(hour,1,10)"
	}
	query := "WITH selected AS (" + physicalStepHoursSQL(pointScope, "hourly_metrics", hourScope) + `)
		SELECT ` + period + ` AS period,sum(avg_val::float8),min(min_val),max(max_val)
		FROM selected GROUP BY period ORDER BY period`
	rows, err := s.pool.Query(ctx, query, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DataPoint])
}

// The usual case visits one day. A later raw row can be ineligible (for
// example, an unrecognized unit); only then search older dates for a result.
func latestPhysicalStepsSQL() string {
	return `with last_day as (
        select greatest((select substring(date,1,10) from metric_points where metric_name='step_count' order by date desc limit 1),
                        (select substring(date,1,10) from metric_points where metric_name='__healthkit_archive_sample:step_count' order by date desc limit 1),
                        (select substring(hour,1,10) from hourly_metrics where metric_name='step_count' order by hour desc limit 1)) as day
    ), recent as materialized (` + physicalStepTotalsSQL("substring(date,1,10)=(select day from last_day)", "hourly_metrics", "substring(hour,1,10)=(select day from last_day)") + `)
    select day,source_total from recent union all
    select day,source_total from (` + physicalStepTotalsSQL("not exists(select from recent) and substring(date,1,10)<(select day from last_day)", "hourly_metrics", "not exists(select from recent) and substring(hour,1,10)<(select day from last_day)") + ` order by day desc limit 1) older`
}
