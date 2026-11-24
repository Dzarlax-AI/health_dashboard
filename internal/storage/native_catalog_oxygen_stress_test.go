package storage

import (
	"math"
	"testing"
	"time"
)

func checkNativeOxygenStress(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	day := time.Date(2021, 10, 31, 0, 0, 0, 0, time.UTC)
	if _, err := db.pool.Exec(ctx, `with receipt as (insert into health_records(automation_name,content_type,payload)
           values('oxygen fixture','application/json','{}') returning id)
       insert into metric_points(metric_name,date,source,qty,quality,units,health_record_id)
       select rows.*,receipt.id from receipt cross join (values('blood_oxygen_saturation','2021-10-31 06:00:00 +0100','probe A',91,'ok','%'),
             ('blood_oxygen_saturation','2021-10-31 07:00:00 +0100','probe B',97,'ok','%'),
             ('blood_oxygen_saturation','2021-10-31 08:00:00 +0100','probe B',80,'bad','%')) rows(name,date,source,qty,quality,units)`); err != nil {
		t.Fatal(err)
	}
	var avg float64
	if err := db.pool.QueryRow(ctx, `select numeric_value from tenant_native_metric_read_v1 where metric_id='oxygen_cluster.avg' and sample_key->>'capture'='2021-10-31'`).Scan(&avg); err != nil || avg != 94 {
		t.Fatalf("oxygen mean %v, error %v", avg, err)
	}
	if err := db.SaveSettings(map[string]string{SettingDecisionCapture: "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, "insert into daily_scores(date) values($1)", day.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	res := SustainedHRLoadResult{signalInputs: &stressSignalInputs{threshold: 1, baseline: 60, baselineSD: 5,
		series: []HourlyHRStat{{Hour: day, CoverageOK: true, Median5minMinHR: 70}, {Hour: day.Add(time.Hour), CoverageOK: false}}, hourZ: []float64{2, math.NaN()}}}
	if err := db.writeSustainedHRLoadRowContext(ctx, day.Format(time.DateOnly), res); err != nil {
		t.Fatal(err)
	}
	var available, missing int
	if err := db.pool.QueryRow(ctx, `select count(*) filter(where numeric_value=2 and value_status='available'),
           count(*) filter(where value_status='missing')
        from tenant_native_metric_read_v1 m join hdbt_decision_inputs j on j.input_hash=m.sample_key->>'capture'
        where j.metric='stress_signals' and j.decision_key='2021-10-31' and m.metric_id='stress_hour_z'`).Scan(&available, &missing); err != nil || available != 1 || missing != 1 {
		t.Fatalf("stress replay available=%d missing=%d error=%v", available, missing, err)
	}
}
