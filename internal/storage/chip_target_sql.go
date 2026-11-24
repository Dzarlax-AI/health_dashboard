package storage

import (
	"context"
	"encoding/json"
	"fmt"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

type chipTargetWindow struct {
	date, subScore, latestSleep string
	paired, breach, density     int
	days                        any
}

type chipTargetDay struct {
	Date     string   `json:"date"`
	Value    *float64 `json:"value"`
	Eligible bool     `json:"eligible"`
	Reason   string   `json:"reason"`
}

var chipTargetsSQL = `with selected as (
	select kind as case_id,$1::text as sub_score,$2::text::date as target_date,
	nullif($3::text,'')::date as latest_sleep,$4::int as paired_count,
	case when kind='chronic_acute_density' then $6::int else $5::int end as breach_threshold,
	coalesce((select jsonb_agg(d || jsonb_build_object('observed',
		coalesce((d->>'recovery_observed')::bool,(d->>'observed')::bool,
		         d->>'hrv' is not null or d->>'rhr' is not null)))
		from jsonb_array_elements($7::jsonb) d),'[]'::jsonb) as days,
	kind as target_kind from unnest($8::text[]) kind
), outcomes as (` + dbtmetrics.ChipTargetsSQL("selected") + `)`

const chipTargetsResultSQL = ` select target_kind,target_value,eligible,reason from outcomes`

func (s *DB) chipTargets(ctx context.Context, w chipTargetWindow, kinds ...string) (map[string]targetWriteSpec, error) {
	payload, err := json.Marshal(w.days)
	if err != nil {
		return nil, err
	}
	if string(payload) == "null" {
		payload = []byte("[]")
	}
	return withDecisionJournal(ctx, s, func(db *DB, journal bool) (map[string]targetWriteSpec, error) {
		query := chipTargetsSQL
		if journal {
			query += `, decision_packets as (
          select 'chip_targets'::text as metric,concat_ws('/',i.target_date,i.sub_score,i.target_kind) as decision_key,
                 1 as formula_version,to_jsonb(i)-'case_id' as inputs,to_jsonb(o)-'case_id' as result
          from selected i join outcomes o using(case_id,sub_score,target_kind)
        )` + decisionJournalInsertSQL
		}
		rows, err := db.pool.Query(ctx, query+chipTargetsResultSQL, w.subScore, w.date, w.latestSleep, w.paired, w.breach, w.density, json.RawMessage(payload), kinds)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make(map[string]targetWriteSpec, len(kinds))
		for rows.Next() {
			var kind string
			var r targetWriteSpec
			if err := rows.Scan(&kind, &r.Value, &r.Eligible, &r.Reason); err != nil {
				return nil, err
			}
			out[kind] = r
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(out) != len(kinds) {
			return nil, fmt.Errorf("returned %d of %d targets for %s", len(out), len(kinds), w.subScore)
		}
		return out, nil
	})
}

func (s *DB) saveChipTarget(date, subScore, kind, epoch string, version int, result targetWriteSpec, coverage json.RawMessage) error {
	return s.SaveTargetSnapshot(TargetSnapshot{Date: date, SubScore: subScore, TargetKind: kind, SourceEpoch: epoch,
		FormulaVersion: version, TargetValue: result.Value, Eligible: result.Eligible, EligibilityReason: result.Reason, DataCoverage: coverage})
}
