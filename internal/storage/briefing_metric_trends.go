package storage

import (
	"context"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

var briefingMetricsQuery = `with series as (
    select ''::text as profile,0 as capture,s.metric,x.ordinal,x.value
    from (values ('step_count',$1::float8[]),('resting_heart_rate',$2::float8[]),
       ('heart_rate_variability',$3::float8[]),('respiratory_rate',$4::float8[]),
       ($5::text,$6::float8[])) s(metric,observations)
    cross join lateral unnest(s.observations) with ordinality x(value,ordinal)
), weekly_inputs as (
    select ''::text as profile,0 as capture,$7::float8[] as sleep,$8::float8[] as deep,$9::float8[] as rem,$10::float8[] as awake
), trends as (` + dbtmetrics.BriefingMetricTrendsSQL + `), weekly as (` + dbtmetrics.WeeklySleepAnalysisSQL + `)
select (select coalesce(jsonb_object_agg(metric,jsonb_build_object(
    'Pct7',pct7,'Pct30',pct30,'Status7',status7,'Status30',status30,
    'Has7',coalesce(baseline7<>0,false),'Has30',coalesce(baseline30<>0,false))),'{}'::jsonb) from trends),
    weekly_sleep from weekly`

func (s *DB) briefingMetrics(ctx context.Context, d *health.RawMetrics) (map[string]health.MetricTrend, *health.SleepAnalysis, error) {
	metric, cardSleep := d.SleepCardSeries()
	var trends map[string]health.MetricTrend
	var weekly *health.SleepAnalysis
	err := s.pool.QueryRow(ctx, briefingMetricsQuery, d.Steps, d.RHR, d.HRV, d.Resp, metric, cardSleep, d.Sleep, d.Deep, d.REM, d.Awake).Scan(&trends, &weekly)
	return trends, weekly, err
}
