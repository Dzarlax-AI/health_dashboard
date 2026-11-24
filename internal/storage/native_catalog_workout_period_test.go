package storage

import (
	"testing"
	"time"
)

func checkNativeWorkoutPeriod(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	date := time.Date(2022, 2, 10, 0, 0, 0, 0, time.UTC)
	if err := db.SaveSettings(map[string]string{SettingDecisionCapture: "true"}); err != nil {
		t.Fatal(err)
	}
	run := func(want int) {
		t.Helper()
		result, err := db.WorkoutStats(date, date.Add(24*time.Hour), "period-probe")
		if err != nil || result.Count != want {
			t.Fatalf("period count %v, %v", result.Count, err)
		}
	}
	run(0)
	if _, err := db.pool.Exec(ctx, `insert into workouts(external_id,name,start_time,end_time,duration_sec,hr_z1_sec)
        values('period-probe','period-probe',$1,$1::timestamptz+interval '1 minute',60,3)`, date); err != nil {
		t.Fatal(err)
	}
	run(1)
	run(1)
	if _, err := db.pool.Exec(ctx, "update workouts set hr_z1_sec=5 where external_id='period-probe'"); err != nil {
		t.Fatal(err)
	}
	run(1)
	var sessions, periods int
	if err := db.pool.QueryRow(ctx, `select count(*) filter(where metric='workout_session'),count(*) filter(where metric='workout_period') from hdbt_decision_inputs where metric in ('workout_session','workout_period')`).Scan(&sessions, &periods); err != nil || sessions != 2 || periods != 3 {
		t.Fatalf("session revisions %d, periods %d, err %v", sessions, periods, err)
	}
	var matches int
	if err := db.pool.QueryRow(ctx, `select count(*) from tenant_native_metric_read_v1 m join hdbt_decision_inputs j
        on j.metric='workout_period' and j.input_hash=m.sample_key->>'period_id'
        where m.metric_id='workout_period.total_hr_z1_sec' and m.numeric_value is not distinct from (j.result->>'total_hr_z1_sec')::numeric`).Scan(&matches); err != nil || matches != 3 {
		t.Fatalf("zone total replay matched %d, error %v", matches, err)
	}
}
