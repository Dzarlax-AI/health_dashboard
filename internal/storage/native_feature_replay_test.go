package storage

import (
	"testing"
	"time"

	"health-receiver/internal/health"
)

func checkNativeFeatureReplay(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	day := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	date := day.Format(isoDate)
	epochs, err := db.loadSourceEpochRunCache()
	if err != nil {
		t.Fatal(err)
	}
	sleep := map[string]health.SleepRow{}
	efficiencies := map[string]health.SleepEfficiencyResult{}
	capture := map[string]health.SleepCaptureConfidenceResult{}
	walks := map[string]health.WalkingHRRow{}
	walking := map[string]health.WalkingHREligibilityResult{}
	autonomic := map[string]health.AutonomicRow{}
	recovery := map[string]recoveryRolling3dRow{}
	for n := -14; n <= 181; n++ {
		ds := day.AddDate(0, 0, -n).Format(isoDate)
		x := float64((n + 300) % 13)
		total, awake := 7+x/20, 0.5
		if n == 1 {
			total = 2
		}
		row := health.SleepRow{Date: ds, Total: &total, Core: &total, Awake: &awake}
		sleep[ds] = row
		efficiencies[ds] = health.ComputeSleepEfficiency(row)
		capture[ds] = health.ComputeSleepCaptureConfidence(row)
		hr, hrv, rr := 65+x, 50+x, 0.6+x/100
		walks[ds] = health.WalkingHRRow{Date: ds, Value: &hr}
		walking[ds] = health.ComputeWalkingHREligibility(walks[ds])
		autonomic[ds] = health.AutonomicRow{Date: ds, HRV: &hrv, RHR: &hr}
		recovery[ds] = recoveryRolling3dRow{Date: ds, Value: &rr, Eligible: true}
	}
	if err = db.writeRecoveryStabilityRow(ctx, day, date, epochs, sleep, efficiencies, capture, SleepArchitectureFeatureFields{}, day.AddDate(0, 0, 14).Format(isoDate)); err != nil {
		t.Fatal(err)
	}
	if err = db.writePassiveEfficiencyRow(ctx, day, date, epochs, walks, walking); err != nil {
		t.Fatal(err)
	}
	if err = db.writeAcuteRiskRow(ctx, day, date, epochs, autonomic, autonomicHRVLookup(autonomic), autonomicRHRLookup(autonomic), map[string]int{}, map[string]int{}); err != nil {
		t.Fatal(err)
	}
	if err = db.writeChronicLoadRow(ctx, day, date, epochs, health.DefaultChronicLoadConfig(), recovery, recoveryRolling3dLookup(recovery), map[string]int{}, map[string]int{}, map[string]int{}, SleepArchitectureFeatureFields{}); err != nil {
		t.Fatal(err)
	}
	var compared, known int
	var mismatches []string
	err = db.pool.QueryRow(ctx, `with j as (
        select metric_id,source,value from tenant_native_metric_read_v1 where sample_key->>'date'=$1
          and source in ('native_feature_replay_v1','archived_features_v1'))
        select count(*),count(a.value),array(select a.metric_id from j a join j b using(metric_id)
          where a.source='archived_features_v1' and b.source='native_feature_replay_v1'
            and a.value is distinct from b.value)
        from j a join j b using(metric_id) where a.source='archived_features_v1' and b.source='native_feature_replay_v1'`, date).Scan(&compared, &known, &mismatches)
	if err != nil || compared != 51 || known != 51 || len(mismatches) != 0 {
		t.Fatalf("compared=%d non-null=%d mismatches=%v: %v", compared, known, mismatches, err)
	}
	// A saved projection can change independently of its original input receipt.
	if _, err = db.pool.Exec(ctx, `update feature_snapshots set features=jsonb_set(features,'{prev_walking_hr}','1000')
       where date=$1 and sub_score='passive_efficiency'`, date); err != nil {
		t.Fatal(err)
	}
	var stable bool
	if err = db.pool.QueryRow(ctx, `select count(*)=2 and count(distinct numeric_value)=1 and bool_and(numeric_value < 1000)
        from tenant_native_metric_read_v1 where metric_id='passive_efficiency.prev_walking_hr'
        and source in ('native_feature_replay_v1','recorded_feature_decision_v1') and sample_key->>'date'=$1`, date).Scan(&stable); err != nil || !stable {
		t.Fatalf("feature replay changed after projection mutation: %v %v", stable, err)
	}
}
