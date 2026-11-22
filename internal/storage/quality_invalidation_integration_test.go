package storage

import "testing"

func TestQualityReclassificationInvalidatesAndRebuildsDerivedState(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	date := "2026-07-12"
	ctx, cancel := queryCtx()
	defer cancel()
	recordID := insertTestRawRecord(t, db, "quality-invalidation")
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES($1,'blood_oxygen_saturation','%',$2,10,'Synthetic Watch','ok')`, recordID, date+" 08:00:00 +0000"); err != nil {
		t.Fatal(err)
	}
	db.UpsertRecentCache([]string{date}, true)
	if _, err := db.pool.Exec(ctx, `INSERT INTO ai_briefing_blocks(date,lang,block,text,inputs_hash) VALUES($1,'en','RECOVERY','stale','hash')`, date); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version) VALUES('2026-07-12 23:55:00+00',$1,50,10,10,2)`, date); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,flags) VALUES('2026-07-12 23:50:00+00',$1,50,10,10,2,ARRAY['backfilled'])`, date); err != nil {
		t.Fatal(err)
	}
	flagged, err := db.MarkExistingImpossible()
	if err != nil || flagged != 1 {
		t.Fatalf("flagged=%d err=%v", flagged, err)
	}
	var quality string
	if err = db.pool.QueryRow(ctx, `SELECT quality FROM metric_points WHERE health_record_id=$1`, recordID).Scan(&quality); err != nil || quality != "impossible" {
		t.Fatalf("quality=%q err=%v", quality, err)
	}
	var blocks, snapshots int
	if err = db.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ai_briefing_blocks WHERE date=$1),(SELECT count(*) FROM energy_snapshots WHERE date=$1)`, date).Scan(&blocks, &snapshots); err != nil {
		t.Fatal(err)
	}
	if blocks != 0 || snapshots != 1 {
		t.Fatalf("derived rows remain or live energy snapshot was lost: blocks=%d snapshots=%d", blocks, snapshots)
	}
	var spo2 *float64
	if err = db.pool.QueryRow(ctx, `SELECT spo2_avg FROM daily_scores WHERE date=$1`, date).Scan(&spo2); err != nil || spo2 != nil {
		t.Fatalf("daily spo2=%v err=%v", spo2, err)
	}
}

func TestQualityAuditClearsHRVOnlyForDatesWithWithdrawnHRV(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := longCtx()
	defer cancel()
	recordID := insertTestRawRecord(t, db, "disjoint-quality-dates")
	const hrvDay, pulseDay = "2026-07-12", "2026-07-13"
	_, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,date,qty,source,quality) VALUES
		($1,'heart_rate_variability',$2,400,'watch','ok'),
		($1,'resting_heart_rate',$3,999,'watch','ok')`, recordID, hrvDay+" 08:00:00 +0000", pulseDay+" 08:00:00 +0000")
	if err != nil {
		t.Fatal(err)
	}
	// Only the second day has a cached HRV with no fresh HRV samples.
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,hrv_avg,computed_at)
		VALUES($1,400,NOW()::text),($2,58,NOW()::text)`, hrvDay, pulseDay); err != nil {
		t.Fatal(err)
	}
	if flagged, err := db.MarkExistingImpossible(); err != nil || flagged != 2 {
		t.Fatalf("reclassified %d points: %v", flagged, err)
	}
	var withdrawn, cached *float64
	err = db.pool.QueryRow(ctx, `SELECT (SELECT hrv_avg FROM daily_scores WHERE date=$1),
		(SELECT hrv_avg FROM daily_scores WHERE date=$2)`, hrvDay, pulseDay).Scan(&withdrawn, &cached)
	if err != nil || withdrawn != nil || cached == nil || *cached != 58 {
		t.Fatalf("withdrawn HRV=%v, unaffected cached HRV=%v: %v", withdrawn, cached, err)
	}
}
