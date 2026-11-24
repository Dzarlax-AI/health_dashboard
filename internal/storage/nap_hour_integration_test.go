package storage

import "testing"

func TestNapHourCorrection(t *testing.T) {
	const day = "2026-04-06"
	for _, path := range []string{"sync", "history"} {
		t.Run(path, func(t *testing.T) {
			db, done := testDB(t)
			defer done()
			ctx := t.Context()
			exec := func(query string) {
				t.Helper()
				if _, err := db.pool.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			refresh := func() {
				t.Helper()
				if path == "sync" {
					if err := db.UpsertRecentCache([]string{day}, false); err != nil {
						t.Fatal(err)
					}
				} else if err := db.RebuildHistoricalCacheDate(ctx, day); err != nil {
					t.Fatal(err)
				}
			}
			check := func(watchCount int, watchTotal float64) {
				t.Helper()
				var count, ring, legacy int
				var total float64
				err := db.pool.QueryRow(ctx, `select count(*) filter(where source='Watch'),
					coalesce(sum(avg_val) filter(where source='Watch'),0),
					count(*) filter(where source='Ring' and avg_val=0.25),
					count(*) filter(where source='Legacy' and avg_val=1)
					from hourly_metrics where metric_name='nap_total' and hour='2026-04-06 13:00'`).Scan(&count, &total, &ring, &legacy)
				if err != nil || count != watchCount || total != watchTotal || ring != 1 || legacy != 1 {
					t.Fatalf("watch=(%d,%v) ring=%d legacy=%d err=%v", count, total, ring, legacy, err)
				}
			}
			exec(`insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
				values('nap_total','2026-04-06 13:00','Legacy',1,1,1,1);
				with receipt as (insert into health_records default values returning id)
				insert into metric_points(health_record_id,metric_name,date,source,qty,quality)
				select receipt.id,'nap_total',p.*, 'ok' from receipt cross join (values
				  ('2026-04-06 13:01:00 +0000','Watch',0.5),
				  ('2026-04-06 13:02:00 +0000','Watch',0.25),
				  ('2026-04-06 13:02:00 +0000','Ring',0.25)) p`)
			refresh()
			check(1, 0.75)
			exec(`update metric_points set qty=0 where metric_name='nap_total' and source='Watch' and date='2026-04-06 13:01:00 +0000'`)
			refresh()
			check(1, 0.25)
			exec(`update metric_points set quality='suspect' where metric_name='nap_total' and source='Watch' and qty>0`)
			refresh()
			check(0, 0)
			refresh()
			check(0, 0)
		})
	}
}
