package storage

import (
	"context"
	"encoding/json"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"time"
)

type chipFeatureWindow struct {
	Today, Mean7, Mean30, Mean45, SD45, Z45  *float64
	N7, N45, N180, AdaptiveN, SlowN          int
	Adaptive, Slow, DeltaAdaptive, DeltaSlow *float64
}

var chipFeatureSQL = `with selected as (
	select ''::text as case_id,$1::text::date as as_of_date,nullif($2::text,'')::date as epoch_start,$3::jsonb as days
), features as (` + dbtmetrics.ChipFeatureWindowsSQL("selected") + `)
select today,mean7,mean30,mean45,sd45,z45,n7,n45,n180,adaptive_n,slow_n,adaptive_value,slow_value,delta_adaptive,delta_slow from features`

func (s *DB) chipFeatureWindow(ctx context.Context, date time.Time, epochStart string, lookback int, lookup DailyValueLookup) (chipFeatureWindow, error) {
	var w chipFeatureWindow
	payload, err := json.Marshal(chipDays(date, lookback, lookup))
	if err != nil {
		return w, err
	}
	err = s.pool.QueryRow(ctx, chipFeatureSQL, date.Format(isoDate), epochStart, json.RawMessage(payload)).Scan(
		&w.Today, &w.Mean7, &w.Mean30, &w.Mean45, &w.SD45, &w.Z45, &w.N7, &w.N45, &w.N180, &w.AdaptiveN, &w.SlowN, &w.Adaptive, &w.Slow, &w.DeltaAdaptive, &w.DeltaSlow)
	return w, err
}
