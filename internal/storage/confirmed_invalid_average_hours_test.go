package storage

import "testing"

func TestConfirmedInvalidAverageHours(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	const day = "2026-04-06"
	const metric = "respiratory_rate"
	for _, path := range []string{"live", "historical"} {
		t.Run(path, func(t *testing.T) {
			_, err := db.pool.Exec(ctx, `DELETE FROM metric_points WHERE metric_name='respiratory_rate';
				DELETE FROM hourly_metrics WHERE metric_name='respiratory_rate';
				INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
				SELECT 'respiratory_rate','2026-04-06 08:00',s,42,42,42,1
				FROM unnest(array['rejected','mixed','unobserved']) s;
				WITH receipt AS (INSERT INTO health_records DEFAULT VALUES RETURNING id)
				INSERT INTO metric_points(health_record_id,metric_name,date,source,qty,quality)
				SELECT receipt.id,points.* FROM receipt CROSS JOIN (VALUES ('respiratory_rate','2026-04-06 08:01:00 +0000','rejected',0,'ok'),
				       ('respiratory_rate','2026-04-06 08:01:00 +0000','mixed',0,'ok'),
				       ('respiratory_rate','2026-04-06 08:02:00 +0000','mixed',18,'ok')) points`)
			if err != nil {
				t.Fatal(err)
			}
			if path == "live" {
				err = db.upsertHourlyAvgForDate(day)
			} else {
				err = db.rebuildHistoricalHourlyMetric(ctx, day, metric)
			}
			if err != nil {
				t.Fatal(err)
			}
			var rejected, mixed, unobserved int
			err = db.pool.QueryRow(ctx, `select count(*) filter(where source='rejected'),
				count(*) filter(where source='mixed' and avg_val=18 and sample_count=1),
				count(*) filter(where source='unobserved' and avg_val=42)
				from hourly_metrics where metric_name=$1 and hour='2026-04-06 08:00'`, metric).Scan(&rejected, &mixed, &unobserved)
			if err != nil || rejected != 0 || mixed != 1 || unobserved != 1 {
				t.Fatalf("rejected=%d mixed=%d unobserved=%d err=%v", rejected, mixed, unobserved, err)
			}
		})
	}
}
