package storage

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"health-receiver/internal/health"
)

func TestOxygenClusterShadow(t *testing.T) {
	if os.Getenv("BRIEFING_STORAGE_PILOT") != "1" {
		t.Skip("synthetic dbt capture required")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	id := insertTestRawRecord(t, db, "oxygen-comparison")
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, a...); err != nil {
			t.Fatal(err)
		}
	}
	const day = "2000-01-02"
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
        select $1,'blood_oxygen_saturation','%',d,v,s,'ok' from (values
        ('2000-01-02 09:00:00 +0000',90,'Ring'),('2000-01-02 09:01:00 +0000',92,'Ring'),
        ('2000-01-02 09:02:00 +0000',94,'Ring'),('2000-01-02 09:00:00 +0000',95,'Watch'),
        ('2000-01-01 23:55:00 +0000',86,'Watch')) samples(d,v,s)`, id)
	for capture := 0; capture < 3; capture++ {
		switch capture {
		case 1:
			exec(`update metric_points set qty=97 where qty=90`)
		case 2:
			exec(`delete from metric_points where left(date,10)=$1`, day)
		}
		exec(`insert into briefing_pilot.oxygen_samples select 'profile-0',$1,source,qty,quality from metric_points where left(date,10)=$2 and metric_name='blood_oxygen_saturation'`, capture, day)
		var data []byte
		if err := db.pool.QueryRow(ctx, `select evidence from derived.oxygen_cluster_v1 where profile='profile-0' and capture=$1`, capture).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var got health.SpO2ClusterEvidence
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if capture == 0 && (got.ValidReadings != 4 || got.Below94Count != 2 || got.Below92Count != 1 || got.Min != 90 || got.Avg != 92.75) {
			t.Fatalf("unexpected initial oxygen evidence: %+v", got)
		}
		if want := db.spO2ClusterEvidence(day); !reflect.DeepEqual(&got, want) {
			t.Fatalf("capture %d: got %+v want %+v", capture, got, want)
		}
	}
}
