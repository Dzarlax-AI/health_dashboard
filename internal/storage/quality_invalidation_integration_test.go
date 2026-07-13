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
	if blocks != 0 || snapshots != 0 {
		t.Fatalf("derived rows remain: blocks=%d snapshots=%d", blocks, snapshots)
	}
	var spo2 *float64
	if err = db.pool.QueryRow(ctx, `SELECT spo2_avg FROM daily_scores WHERE date=$1`, date).Scan(&spo2); err != nil || spo2 != nil {
		t.Fatalf("daily spo2=%v err=%v", spo2, err)
	}
}
