package storage

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestWorkoutDBTParity(t *testing.T) {
	if os.Getenv("WORKOUT_STATS_PILOT") != "synthetic-only" {
		t.Skip("requires synthetic workout fixture")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for _, profile := range []string{"first", "second", "empty"} {
		t.Run(profile, func(t *testing.T) {
			// The two synthetic profiles share external IDs, names and instants. Freeze each
			// fixture before loading the next tenant's inputs into the Go storage schema.
			exec("delete from workouts")
			if profile != "empty" {
				exec(`insert into workouts(external_id,name,start_time,end_time,duration_sec,avg_hr_bpm,max_hr_bpm,
       distance_km,energy_kcal,hr_z1_sec,hr_z2_sec,hr_z3_sec,hr_z4_sec,hr_z5_sec)
     select id,name,ts,ts+duration*interval '1 second',duration,a,m,d,k,z1,z2,z3,z4,z5
     from (values('a','Walk',$1::timestamptz-interval '1 second',3600,104,125,4,150,15,30,5,0,0),
       ('b','Ride',$1::timestamptz,3600,170,189,25,400,10,20,50,null,0),
       ('c','Ride',$2::timestamptz,60,100,112,1,0,40,0,0,null,0),
       ('d','Yoga',$1::timestamptz+interval '1 second',600,null,null,null,null,null,null,null,null,null),
       ('e','Walk',$2::timestamptz+interval '1 second',180,115,121,1,70,null,null,null,null,null)
       ) w(id,name,ts,duration,a,m,d,k,z1,z2,z3,z4,z5)`, start, end)
				if profile == "second" {
					exec(`delete from workouts where external_id in ('a','c','e')`)
					exec(`update workouts set duration_sec=30,avg_hr_bpm=55 where name='Ride'`)
				}
			}
			exec(`insert into workout_capture.sessions select $1,external_id,name,start_time,duration_sec,distance_km,
   energy_kcal,avg_hr_bpm,max_hr_bpm,hr_z1_sec,hr_z2_sec,hr_z3_sec,hr_z4_sec,hr_z5_sec from workouts`, profile)
			for _, p := range []struct {
				id, filter string
				from, to   time.Time
			}{
				{"all", "", start, end}, {"filtered", "Ride", start, end}, {"null-signals", "Yoga", start, end}, {"case-sensitive", "ride", start, end},
				{"point", "", start, start}, {"empty-period", "", end.Add(time.Hour), end.Add(2 * time.Hour)},
			} {
				exec(`insert into workout_capture.periods values($1,$2,$3,$4,$5)`, profile, p.id, p.from, p.to, p.filter)
				want, err := db.WorkoutStats(p.from, p.to, p.filter)
				if err != nil {
					t.Fatal(err)
				}
				// Independent expectations still catch formula drift when both consumers
				// execute the shared dbt definition.
				if profile == "first" && p.id == "filtered" {
					if want.Count != 2 || want.TotalDurationSec != 3660 ||
						want.AvgHRBPM == nil || *want.AvgHRBPM != 135 ||
						want.TotalZ1Sec == nil || *want.TotalZ1Sec != 50 || want.TotalZ4Sec != nil {
						t.Fatalf("filtered workout summary: %+v", want)
					}
				}
				if p.id == "empty-period" && (want.Count != 0 || want.TotalDurationSec != 0 || want.AvgHRBPM != nil) {
					t.Fatalf("empty workout summary: %+v", want)
				}
				var got WorkoutStats
				err = db.pool.QueryRow(ctx, `select count,total_duration_sec,total_distance_km,total_energy_kcal,avg_hr_bpm,max_hr_bpm,
       total_hr_z1_sec,total_hr_z2_sec,total_hr_z3_sec,total_hr_z4_sec,total_hr_z5_sec
       from dbt_output.workout_period_v1 where profile=$1 and period_id=$2`, profile, p.id).Scan(&got.Count, &got.TotalDurationSec,
					&got.TotalDistanceKm, &got.TotalEnergyKcal, &got.AvgHRBPM, &got.MaxHRBPM, &got.TotalZ1Sec, &got.TotalZ2Sec, &got.TotalZ3Sec, &got.TotalZ4Sec, &got.TotalZ5Sec)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s Go %+v; SQL %+v; err %v", p.id, want, got, err)
				}
			}
			want, err := db.ListWorkoutTypes()
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.pool.Query(ctx, `select name,count,first_seen,last_seen from dbt_output.workout_type_v1 where profile=$1 order by sort_order`, profile)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []WorkoutTypeCount
			for rows.Next() {
				var w WorkoutTypeCount
				if err := rows.Scan(&w.Name, &w.Count, &w.FirstSeen, &w.LastSeen); err != nil {
					t.Fatal(err)
				}
				got = append(got, w)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("Go %+v; SQL %+v", want, got)
			}
		})
	}
}
