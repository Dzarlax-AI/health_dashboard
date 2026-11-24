package storage

import (
	"testing"
	"time"
)

func checkNativeChipReceipts(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into settings(key,value) values($1,'true') on conflict(key) do update set value='true'", SettingDecisionCapture)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	value := 22.
	w := chipTargetWindow{subScore: "passive_efficiency", date: day.Format(isoDate), days: []chipTargetDay{{"2026-09-02", &value, true, "ok"}}}
	for _, v := range []float64{22, 23, 23} {
		value = v
		result, err := db.chipTargets(ctx, w, "daily_point")
		if err != nil || result["daily_point"].Value == nil || *result["daily_point"].Value != v {
			t.Fatalf("target %v: %v", result, err)
		}
	}
	lookup := func(date string) (*float64, bool) {
		if date == day.Format(isoDate) {
			return &value, true
		}
		return nil, false
	}
	if _, err := db.chipBaselines(ctx, day, "", "passive_efficiency", lookup, "daily_point"); err != nil {
		t.Fatal(err)
	}
	exec(`insert into naive_baselines(date,sub_score,target_kind,baseline_kind,predicted_value,source_epoch,formula_version)
        select (date '2026-09-01'-n)::text,'acute_risk','event_t1_t3','event_base_rate',0.25,'test',1 from generate_series(0,99) n;
        insert into target_snapshots(date,sub_score,target_kind,target_value,eligible,eligibility_reason,source_epoch,formula_version)
        select (date '2026-09-01'-n)::text,'acute_risk','event_t1_t3',(n<50)::int,true,'ok','test',1 from generate_series(0,99) n`)
	calibration, err := db.recomputeOneChipCalibration("acute_risk", "event_t1_t3", "test", day)
	if err != nil || calibration.Status != "active" {
		t.Fatalf("calibration: %v %v", calibration, err)
	}
	exec("update target_snapshots set target_value=0 where source_epoch='test'")
	checks := []struct {
		id, predicate string
		n             int
	}{
		{"passive_efficiency.target_daily_point", "numeric_value in (22,23)", 2},
		{"target.eligible", "boolean_value", 2},
		{"naive_baseline.predicted_value", "sample_key->>'baseline_kind'='persistence_yesterday' and numeric_value=23", 1},
		{"chip_calibration.cutoff", "numeric_value=0.5", 1}, {"chip_calibration.status", "text_value='active'", 1},
	}
	for _, c := range checks {
		var count int
		if err := db.pool.QueryRow(ctx, "select count(*) from tenant_native_metric_read_v1 where metric_id=$1 and ("+c.predicate+")", c.id).Scan(&count); err != nil || count != c.n {
			t.Fatalf("%s: %d expected %d: %v", c.id, count, c.n, err)
		}
	}
}
