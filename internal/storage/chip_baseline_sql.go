package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

var chipBaselineSQL = `with selected as (
	select ''::text as case_id, $1::text as sub_score,''::text as target_kind,
	       $2::text::date as as_of_date,nullif($3::text,'')::date as epoch_start,$4::jsonb as days
), predictions as (` + dbtmetrics.ChipBaselineSQL("selected") + `)
select baseline_kind,predicted,reason from predictions order by baseline_kind`

type chipBaselineDay struct {
	Date     string  `json:"date"`
	Value    float64 `json:"value"`
	Eligible bool    `json:"eligible"`
}

func chipDays(date time.Time, lookback int, lookup DailyValueLookup) []chipBaselineDay {
	days := make([]chipBaselineDay, 0)
	for offset := 0; offset <= lookback; offset++ {
		day := date.AddDate(0, 0, -offset).Format(isoDate)
		if v, eligible := lookup(day); v != nil && eligible {
			days = append(days, chipBaselineDay{Date: day, Value: *v, Eligible: true})
		}
	}
	return days
}

// Targets with the same input window share one SQL calculation. The caller
// retains ownership of eligibility, source epoch and the persisted target keys.
func (s *DB) chipBaselines(ctx context.Context, date time.Time, epochStart, subScore string, lookup DailyValueLookup, targets ...string) ([]naiveBaselineRow, error) {
	days := chipDays(date, ewmaLookbackDays(ewmaWindowAdaptive), lookup)
	payload, err := json.Marshal(days)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, chipBaselineSQL, subScore, date.Format(isoDate), epochStart, json.RawMessage(payload))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []naiveBaselineRow
	for rows.Next() {
		var row naiveBaselineRow
		if err := rows.Scan(&row.BaselineKind, &row.Value, &row.Reason); err != nil {
			return nil, err
		}
		for _, target := range targets {
			row.TargetKind = target
			out = append(out, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no baseline kinds for %s", subScore)
	}
	return out, nil
}

var chipCalibrationSQL = `with selected as (
	select ''::text as case_id,$3::text as epoch,$4::text::date as as_of_date
), candidate_pairs as (
	select ''::text as case_id,n.date::date as day,n.source_epoch as prediction_epoch,t.source_epoch as label_epoch,
	n.baseline_kind,n.predicted_value as predicted,t.target_value as label,t.eligible
	from naive_baselines n join target_snapshots t using(date,sub_score,target_kind)
	where n.sub_score=$1 and n.target_kind=$2 and n.source_epoch=$3
	and n.date between ($4::text::date-179)::text and $4::text
), calibration as (` + dbtmetrics.ChipCalibrationSQL("selected", "candidate_pairs") + `)
select n_eligible,n_positives,calibration_status,p80,base_rate,cutoff from calibration`
