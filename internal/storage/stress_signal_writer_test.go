package storage

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestStressSignalWriterShadow(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	const day = "2026-03-20"
	exec := func(sql string) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into health_records (payload) values ('{}');
	insert into daily_scores (date,baseline_hr_overnight)
	select ('2026-03-20'::date-i)::text, case when i=0 then 66 else 60 end from generate_series(0,15)i;
	insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
	select 'heart_rate',('2026-03-20'::date-i)::text||' 09:00','A',60,60,60,1 from generate_series(1,15)i;
	insert into metric_points(health_record_id,metric_name,date,source,qty,quality)
	select (select min(id) from health_records),metric,('2026-03-20'::date-i)::text||' 05:00:00 +0000','A',
	case when i=0 then today else baseline end,'ok' from generate_series(0,15)i
	cross join (values ('heart_rate_variability',50,40),('wrist_temperature',35,36),('respiratory_rate',15,16)) m(metric,baseline,today);
	insert into metric_points(health_record_id,metric_name,date,source,qty,quality)
	select (select min(id) from health_records),'heart_rate',to_char(timestamp '2026-03-20' + h*interval '1 hour' + m*interval '5 minutes','YYYY-MM-DD HH24:MI:SS')||' +0000','A',69,'ok'
	from generate_series(8,15) h cross join generate_series(0,11) m;
	insert into settings(key,value) values ('energy.z_threshold','1.5') on conflict(key) do update set value=excluded.value`)
	check := func(load float64, flags ...string) {
		t.Helper()
		_, err := db.UpsertSustainedHRLoadForDate(day, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		var actual float64
		var got []string
		if err := db.pool.QueryRow(ctx, `select sustained_hr_load,stress_flags from daily_scores where date=$1`, day).Scan(&actual, &got); err != nil {
			t.Fatal(err)
		}
		if actual != load || !slices.Equal(got, flags) {
			t.Fatalf("load %v flags %v; want %v, %v", actual, got, load, flags)
		}
	}
	check(12, "acute_stress", "sustained_load", "illness_signature", "recovery_debt")
	// Two low-coverage hours break the four-hour runs while all eight clock
	// hours still count toward the awake-window gate.
	exec(`update metric_points set quality='invalid' where metric_name='heart_rate' and substring(date,12,2) in ('11','15') and substring(date,15,2)<>'00'`)
	check(9, "acute_stress", "illness_signature", "recovery_debt")
	exec(`update metric_points set qty=65 where metric_name='heart_rate_variability' and date like '2026-03-20%'`)
	check(9, "acute_stress", "parasympathetic_rebound")
	exec(`update metric_points set quality='invalid' where metric_name='heart_rate' and substring(date,12,2)='08';
	update metric_points set qty=40 where metric_name='heart_rate_variability' and date like '2026-03-20%'`)
	check(0, "stale_stress", "illness_signature", "recovery_debt")
	exec(`update metric_points set quality='invalid' where metric_name='wrist_temperature' and date like '2026-03-20%'`)
	check(0, "stale_stress", "recovery_debt")
}
