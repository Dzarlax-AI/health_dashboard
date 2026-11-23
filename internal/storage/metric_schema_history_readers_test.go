package storage

import (
	"context"
	"os"
	"testing"
)

func TestMetricSchemaHistoryReaders(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" {
		t.Skip("synthetic audit directory required")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	const date = "2026-03-31"
	id := insertTestRawRecord(t, db, "history-reader-fixture")
	_, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality)
 SELECT $1,v.metric,v.units,to_char('2026-03-31'::date-n,'YYYY-MM-DD')||' 07:00:00 +0000',v.qty,'Synthetic Watch','ok'
 FROM generate_series(0,14) n CROSS JOIN LATERAL (VALUES
 ('heart_rate_variability','ms',(50+n%5)::float8),('resting_heart_rate','bpm',(60+n%4)::float8),('sleep_total','hr',(7+(n%3)*0.25)::float8)
 ) v(metric,units,qty)`, id)
	if err != nil {
		t.Fatal(err)
	}
	history, err := db.computeReadinessHistoryAt(ctx, 1, date)
	if err != nil || len(history) != 1 || history[0].Date != date {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	fillReadinessBands(history)
	if history[0].Band == "" {
		t.Fatal("history band missing")
	}
	cutoff, p80, base := .7, .7, .2
	calibration := ChipCalibration{SubScore: SubScoreAcuteRisk, TargetKind: TargetKindEventT1T3, SourceEpoch: "synthetic", Status: ChipCalibrationStatusActive, Method: ChipCalibrationMethodPercentileP80, Cutoff: &cutoff, P80: &p80, BaseRate: &base, CalibrationWindowDays: 90, NEligible: 50, NPositives: 10}
	if err := db.SaveChipCalibration(calibration); err != nil {
		t.Fatal(err)
	}
	stored, err := db.LoadChipCalibration(calibration.SubScore, calibration.TargetKind, calibration.SourceEpoch)
	if err != nil || stored == nil || stored.Cutoff == nil || *stored.Cutoff < .699 || *stored.Cutoff > .701 {
		t.Fatalf("calibration=%+v err=%v", stored, err)
	}
	_, err = db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES
 ($1,'blood_oxygen_saturation','%', '2026-03-31 09:00:00 +0000',90,'Synthetic Ring','ok'),
 ($1,'blood_oxygen_saturation','%', '2026-03-31 09:01:00 +0000',94,'Synthetic Ring','ok'),
 ($1,'blood_oxygen_saturation','%', '2026-03-31 09:02:00 +0000',98,'Synthetic Watch','ok')`, id)
	if err != nil {
		t.Fatal(err)
	}
	oxygen := db.spO2ClusterEvidence(date)
	if oxygen == nil || oxygen.ValidReadings != 3 || oxygen.Below94Count != 1 || oxygen.Below92Count != 1 {
		t.Fatalf("oxygen=%+v", oxygen)
	}
	writeMetricRuntimeCapture(t, dir, "history_readers.json",
		metricRuntimeCapture{"readiness_history", "storage.computeReadinessHistoryAt + fillReadinessBands", history[0], map[string]any{"date": date}},
		metricRuntimeCapture{"chip_calibration", "storage.SaveChipCalibration + LoadChipCalibration", stored, map[string]any{"sub_score": calibration.SubScore, "target_kind": calibration.TargetKind, "source_epoch": calibration.SourceEpoch}},
		metricRuntimeCapture{"oxygen_cluster", "storage.spO2ClusterEvidence", oxygen, map[string]any{"date": date}},
	)
}
