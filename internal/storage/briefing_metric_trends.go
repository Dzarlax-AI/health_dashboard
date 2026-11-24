package storage

import (
	"context"
	"encoding/json"

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
, captures as (select 0 as capture,''::text as profile,$11::jsonb as evidence), section_inputs as (select 0 as capture,''::text as profile,jsonb_build_object('raw',$12::jsonb) as inputs),
section_result as (` + dbtmetrics.BriefingSectionsSQL + `), quality_result as (` + dbtmetrics.DashboardSleepQualitySQL + `)
select (select coalesce(jsonb_object_agg(metric,jsonb_build_object(
    'Pct7',pct7,'Pct30',pct30,'Status7',status7,'Status30',status30,
    'Has7',coalesce(baseline7<>0,false),'Has30',coalesce(baseline30<>0,false))),'{}'::jsonb) from trends),
    weekly_sleep, quality,sections from weekly cross join quality_result cross join section_result`

func (s *DB) briefingMetrics(ctx context.Context, d *health.RawMetrics) (map[string]health.MetricTrend, *health.SleepAnalysis, *health.SleepQualityBreakdown, map[string]string, error) {
	metric, cardSleep := d.SleepCardSeries()
	var trends map[string]health.MetricTrend
	var weekly *health.SleepAnalysis
	var quality health.SleepQualityBreakdown
	evidence, err := json.Marshal(d.ReadinessEvidence)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sectionInput, err := json.Marshal(briefingSectionInputs(d))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var sections map[string]string
	err = s.pool.QueryRow(ctx, briefingMetricsQuery, d.Steps, d.RHR, d.HRV, d.Resp, metric, cardSleep, d.Sleep, d.Deep, d.REM, d.Awake, json.RawMessage(evidence), json.RawMessage(sectionInput)).Scan(&trends, &weekly, &quality, &sections)
	return trends, weekly, &quality, sections, err
}

func (s *DB) dashboardSleepQuality(ctx context.Context, d *health.RawMetrics) (*health.SleepQualityBreakdown, error) {
	evidence, err := json.Marshal(d.ReadinessEvidence)
	if err != nil {
		return nil, err
	}
	var result health.SleepQualityBreakdown
	const inputs = "with captures as (select 0 as capture,''::text as profile,$1::jsonb as evidence), evaluated as ("
	err = s.pool.QueryRow(ctx, inputs+dbtmetrics.DashboardSleepQualitySQL+") select quality from evaluated", json.RawMessage(evidence)).Scan(&result)
	return &result, err
}

func briefingSectionInputs(d *health.RawMetrics) map[string]any {
	return map[string]any{"Steps": d.Steps, "Cal": d.Cal, "Exercise": d.Exercise, "SpO2": d.SpO2, "VO2": d.VO2, "Resp": d.Resp}
}
