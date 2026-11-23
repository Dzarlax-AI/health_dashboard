package storage

import (
	"context"
	"encoding/json"
	"fmt"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

// Arrays retain the exact order supplied by the history, warm, cold or
// projected-sleep reader. Evidence is dated independently by the morning reader.
type readinessScoreInput struct {
	Date                                 string
	HRV, RHR, Sleep                      []float64
	HRVPresent, RHRPresent, SleepPresent bool
	LowQuality, Provisional              bool
	Samples                              int
	Illness                              string
}

var readinessScoresSQL = `with inputs as (
	select "Date" as target_date, "Date" as case_id, ''::text as profile, $2::text as reader,
	"HRV" as hrv, "RHR" as rhr, "Sleep" as sleep, "HRVPresent" as hrv_present,
	"RHRPresent" as rhr_present, "SleepPresent" as sleep_present,
	"LowQuality" as low_sleep_quality, "Provisional" as hrv_provisional,
	"Samples" as hrv_samples, "Illness" as illness, ''::text as headline,
	true as captured
	from jsonb_to_recordset($1::jsonb) as i("Date" text, "HRV" float8[], "RHR" float8[], "Sleep" float8[],
		"HRVPresent" bool, "RHRPresent" bool, "SleepPresent" bool, "LowQuality" bool,
		"Provisional" bool, "Samples" int, "Illness" text)
), eligibility as (
	select i.*, h.*, case when reader='morning' and not hrv_present then 'missing_same_day_evidence'
		when coalesce(cardinality(hrv),0)=0 then 'missing_same_day_value'
		when cardinality(hrv)<9 then 'insufficient_observations' end as skip_reason
	from inputs i cross join lateral (
		select max(v) filter(where pos=1) as current_ms, avg(v) filter(where pos<=7) as recent7_mean_ms,
		       avg(v) filter(where pos>7) as baseline_mean_ms, stddev_pop(v) filter(where pos>7) as baseline_sd_ms
		from unnest(i.hrv) with ordinality as a(v,pos)
	) h
), hrv_scores as (` + dbtmetrics.ReadinessHRVMathSQL("eligibility") + `),
captures as (select *, weighted_z as hrv_weighted_z, component_weight as hrv_weight, skip_reason as hrv_reason from hrv_scores),
scores as (` + dbtmetrics.ReadinessScoreSQL("captures") + `)
select target_date,raw_score,display_score,coalesce(confidence,''),coalesce(cap_reason,'') from scores order by target_date`

// Dates are unique and ascending. A history window requires only one query.
func (s *DB) readReadinessScores(ctx context.Context, reader string, inputs []readinessScoreInput) ([]health.ReadinessProjection, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, readinessScoresSQL, json.RawMessage(payload), reader)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scores := make([]health.ReadinessProjection, 0, len(inputs))
	for rows.Next() {
		var date string
		var p health.ReadinessProjection
		if err := rows.Scan(&date, &p.RawScore, &p.DisplayScore, &p.Confidence, &p.CapReason); err != nil {
			return nil, err
		}
		if len(scores) >= len(inputs) || date != inputs[len(scores)].Date {
			return nil, fmt.Errorf("unexpected readiness date %q", date)
		}
		scores = append(scores, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(scores) != len(inputs) {
		return nil, fmt.Errorf("readiness returned %d of %d dates", len(scores), len(inputs))
	}
	return scores, nil
}

func (s *DB) morningReadinessProjection(ctx context.Context, d *health.RawMetrics) (health.ReadinessProjection, error) {
	i := readinessScoreInput{Date: d.LastDate, HRV: d.HRV, RHR: d.RHR, Sleep: d.Sleep, HRVPresent: true, RHRPresent: true, SleepPresent: true}
	if e := d.ReadinessEvidence; e != nil {
		i.HRVPresent, i.RHRPresent, i.SleepPresent = e.HRV.Present, e.RHR.Present, e.SleepDuration.Present
		i.LowQuality = e.SleepQuality.Present && e.SleepQuality.Confidence == health.ReadinessConfidenceLow
		i.Provisional = e.HRV.Confidence == health.ReadinessConfidenceProvisional
		i.Samples = e.HRV.SampleCount
		i.Illness = e.IllnessConfidence
	}
	scores, err := s.readReadinessScores(ctx, "morning", []readinessScoreInput{i})
	if err != nil {
		return health.ReadinessProjection{}, err
	}
	return scores[0], nil
}
