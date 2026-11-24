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
	` + dbtmetrics.ReadinessEffectiveEligibilitySQL("inputs") + `
), hrv_scores as (` + dbtmetrics.ReadinessHRVMathSQL("eligibility") + `),
captures as (select *, weighted_z as hrv_weighted_z, component_weight as hrv_weight, skip_reason as hrv_reason from hrv_scores),
scores as (` + dbtmetrics.ReadinessScoreSQL("captures") + `)
`

const readinessResultSQL = ` select target_date,raw_score,display_score,coalesce(confidence,''),coalesce(cap_reason,'') from scores order by target_date`

const readinessDecisionPackets = `, decision_packets as (
    select 'readiness'::text as metric, $2||'/'||s.target_date as decision_key, $3::int as formula_version,
        jsonb_build_object('reader',$2,'effective',e.input) as inputs,
        jsonb_build_object('RawScore',s.raw_score,'DisplayScore',s.display_score,
            'Confidence',coalesce(s.confidence,''),'CapReason',coalesce(s.cap_reason,'')) as result
    from scores s join jsonb_array_elements($1::jsonb) e(input) on e.input->>'Date'=s.target_date
)`

// Dates are unique and ascending. A history window requires only one query.
func (s *DB) readReadinessScores(ctx context.Context, reader string, inputs []readinessScoreInput) ([]health.ReadinessProjection, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	if s.profileGraphSchema == "" {
		if published, ok := s.publishedReadinessScores(ctx, reader, payload, len(inputs)); ok {
			return published, nil
		}
	}
	return withDecisionJournal(ctx, s, func(db *DB, journal bool) ([]health.ReadinessProjection, error) {
		return db.evaluateReadinessScores(ctx, reader, inputs, payload, journal)
	})
}

// A history request uses one snapshot for the entire batch. An absent receipt,
// including a late input revision, keeps the ordinary evaluator on the same request.
func (s *DB) publishedReadinessScores(ctx context.Context, reader string, payload []byte, count int) ([]health.ReadinessProjection, bool) {
	const query = `select (r.result->>'RawScore')::int,(r.result->>'DisplayScore')::int,
        r.result->>'Confidence',r.result->>'CapReason' from jsonb_array_elements($1::jsonb) with ordinality e(input,ord)
        join hdbt_decision_results r on r.metric='readiness'
          and r.decision_key=$2||'/'||(e.input->>'Date') and r.formula_version=$3::integer
          and r.input_hash=encode(sha256(convert_to(jsonb_build_array($3::integer,
              jsonb_build_object('reader',$2::text,'effective',e.input))::text,'UTF8')),'hex')
        where ` + decisionPublishedReadPredicate + ` order by e.ord`
	scores, found, err := withPublishedDecision(ctx, s, func(db *DB) ([]health.ReadinessProjection, bool, error) {
		rows, err := db.pool.Query(ctx, query, json.RawMessage(payload), reader, ScoreVersion)
		if err != nil {
			return nil, false, err
		}
		defer rows.Close()
		var scores []health.ReadinessProjection
		for rows.Next() {
			var score health.ReadinessProjection
			if err := rows.Scan(&score.RawScore, &score.DisplayScore, &score.Confidence, &score.CapReason); err != nil {
				return nil, false, err
			}
			scores = append(scores, score)
		}
		return scores, len(scores) == count, rows.Err()
	})
	return scores, found && err == nil
}

func (s *DB) evaluateReadinessScores(ctx context.Context, reader string, inputs []readinessScoreInput, payload []byte, journal bool) ([]health.ReadinessProjection, error) {
	query := readinessScoresSQL
	args := []any{json.RawMessage(payload), reader}
	if journal {
		query += readinessDecisionPackets + decisionJournalInsertSQL
		args = append(args, ScoreVersion)
	}
	rows, err := s.pool.Query(ctx, query+readinessResultSQL, args...)
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
	if packet, _ := ctx.Value(localReadinessCaptureKey{}).(*LocalReadinessDecision); packet != nil && s.profileGraphSchema != "" && len(inputs) == 1 {
		packet.Input = inputs[0]
		packet.Result = scores[0]
	}
	return scores, nil
}

func morningReadinessInput(d *health.RawMetrics) readinessScoreInput {
	i := readinessScoreInput{Date: d.LastDate, HRV: d.HRV, RHR: d.RHR, Sleep: d.Sleep, HRVPresent: true, RHRPresent: true, SleepPresent: true}
	if e := d.ReadinessEvidence; e != nil {
		i.HRVPresent, i.RHRPresent, i.SleepPresent = e.HRV.Present, e.RHR.Present, e.SleepDuration.Present
		i.LowQuality = e.SleepQuality.Present && e.SleepQuality.Confidence == health.ReadinessConfidenceLow
		i.Provisional = e.HRV.Confidence == health.ReadinessConfidenceProvisional
		i.Samples = e.HRV.SampleCount
		i.Illness = e.IllnessConfidence
	}
	return i
}

func (s *DB) morningReadinessProjection(ctx context.Context, d *health.RawMetrics) (health.ReadinessProjection, error) {
	scores, err := s.readReadinessScores(ctx, "morning", []readinessScoreInput{morningReadinessInput(d)})
	if err != nil {
		return health.ReadinessProjection{}, err
	}
	return scores[0], nil
}
