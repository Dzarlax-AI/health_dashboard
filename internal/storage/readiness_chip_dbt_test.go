package storage

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func chipDB(t *testing.T) *DB {
	t.Helper()
	if os.Getenv("READINESS_CHIP_SHADOW_COMPARE") != "1" {
		t.Skip("requires local synthetic dbt chip models")
	}
	db, cleanup := testDB(t)
	t.Cleanup(cleanup)
	return db
}

func chipExec(t *testing.T, db *DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func chipValueEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) <= 1e-9
}

func compareChipTarget(t *testing.T, db *DB, id string, expected targetWriteSpec) {
	t.Helper()
	var value *float64
	var eligible bool
	var reason string
	err := db.pool.QueryRow(context.Background(), "SELECT target_value,eligible,reason FROM dbt_output.chip_targets_v1 WHERE case_id=$1", id).Scan(&value, &eligible, &reason)
	if err != nil || !chipValueEqual(value, expected.Value) || eligible != expected.Eligible || reason != expected.Reason {
		t.Fatalf("%s: sql=%v,%v,%s go=%v,%v,%s err=%v", id, value, eligible, reason, expected.Value, expected.Eligible, expected.Reason, err)
	}
}

func TestChipDBTContinuousTargets(t *testing.T) {
	db := chipDB(t)
	anchor := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"complete", "gap", "mixed_blockers", "immature"} {
		sleepRows := map[string]health.SleepRow{}
		sleeps := map[string]health.SleepEfficiencyResult{}
		walkingRows := map[string]health.WalkingHRRow{}
		walks := map[string]health.WalkingHREligibilityResult{}
		var sleepDays, walkDays []map[string]any
		for i := 0; i <= 4; i++ {
			date := anchor.AddDate(0, 0, i).Format(isoDate)
			if scenario == "immature" && i > 2 || (scenario == "gap" || scenario == "mixed_blockers") && i == 2 {
				continue
			}
			sleep := health.SleepRow{Date: date, Total: ptrFloat(7), Deep: ptrFloat(1), Awake: ptrFloat(float64(i) * 0.15)}
			walk := health.WalkingHRRow{Date: date, Value: ptrFloat(85 + float64(i)*2)}
			if scenario == "mixed_blockers" && i == 1 {
				sleep.Total = nil
				walk.Value = ptrFloat(190)
			}
			sleepRows[date] = sleep
			sleeps[date] = health.ComputeSleepEfficiency(sleep)
			walkingRows[date] = walk
			walks[date] = health.ComputeWalkingHREligibility(walk)
			s := sleeps[date]
			w := walks[date]
			sleepDays = append(sleepDays, map[string]any{"date": date, "value": s.Efficiency, "eligible": s.Eligible, "reason": s.EligibilityReason})
			walkDays = append(walkDays, map[string]any{"date": date, "value": w.Value, "eligible": w.Eligible, "reason": w.EligibilityReason})
		}
		dayOne := anchor.AddDate(0, 0, 1).Format(isoDate)
		latest := anchor.AddDate(0, 0, 4)
		if scenario == "immature" {
			latest = anchor.AddDate(0, 0, 2)
		}
		_, strictCoverage, candidateCoverage := recoveryTargetWindow(anchor, sleeps, sleepRows, nil, latest.Format(isoDate))
		_, passiveCoverage := passiveTargetWindow(anchor, walks, walkingRows)
		if string(strictCoverage) != string(rolling3dTarget(anchor, sleeps, sleepRows, nil).Coverage) ||
			string(candidateCoverage) != string(rolling3dCandidate2of3Target(anchor, sleeps, sleepRows, nil, latest.Format(isoDate)).Coverage) ||
			string(passiveCoverage) != string(passiveRolling3dTarget(anchor, walks, walkingRows).Coverage) {
			t.Fatal("dated target coverage changed", scenario)
		}
		for _, tc := range []struct {
			kind, target string
			days         []map[string]any
			result       targetWriteSpec
		}{
			{SubScoreRecoveryStability, TargetKindDailyPoint, sleepDays, targetFromEff(sleeps[dayOne], sleepRows[dayOne], health.SleepCaptureConfidenceResult{})},
			{SubScoreRecoveryStability, TargetKindRolling3dCandidate2of3, sleepDays, rolling3dCandidate2of3Target(anchor, sleeps, sleepRows, nil, latest.Format(isoDate))},
			{SubScorePassiveEfficiency, TargetKindDailyPoint, walkDays, passiveTargetFromVerdict(dayOne, walks, walkingRows)},
			{SubScoreRecoveryStability, TargetKindRolling3d, sleepDays, rolling3dTarget(anchor, sleeps, sleepRows, nil)},
			{SubScorePassiveEfficiency, TargetKindRolling3d, walkDays, passiveRolling3dTarget(anchor, walks, walkingRows)},
		} {
			days, err := json.Marshal(tc.days)
			if err != nil {
				t.Fatal(err)
			}
			id := tc.kind + "_" + tc.target + "_" + scenario
			chipExec(t, db, "INSERT INTO source_data.chip_target_input VALUES ($1,$2,$3,NULL,NULL,$4,$5,$6)", id, tc.kind, anchor, json.RawMessage(days), tc.target, latest)
			compareChipTarget(t, db, id, tc.result)
		}
	}
}

func TestChipDBTBinaryTargets(t *testing.T) {
	for _, scenario := range []string{"positive", "negative", "missing", "warmup"} {
		t.Run(scenario, func(t *testing.T) {
			db := chipDB(t)
			anchor := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
			date := anchor.Format(isoDate)
			history := 60
			if scenario == "warmup" {
				history = 29
			}
			seedRecoveryHistory(t, db, date, history, 0.93, InitialSourceEpoch)
			seedSteadyHistory(t, db, date, history)
			for i := 1; i <= 14; i++ {
				if scenario == "missing" && i > 1 {
					continue
				}
				day := anchor.AddDate(0, 0, i).Format(isoDate)
				recovery, hrv, rhr := 0.93, 45.0, 60.0
				if scenario == "positive" && i <= 5 {
					recovery = 0.78
					hrv = 35
				}
				seedRecoveryRolling3d(t, db, day, &recovery, InitialSourceEpoch)
				if scenario == "positive" && i == 1 {
					rhr = 75
				}
				seedAutonomicRow(t, db, day, &hrv, &rhr)
				if scenario != "missing" {
					label := 0.0
					if scenario == "positive" && i <= 8 {
						label = 1
					}
					seedAcuteOrEvent(t, db, day, label, InitialSourceEpoch)
				}
			}
			if _, err := db.BackfillAcuteRiskSnapshots(date, date); err != nil {
				t.Fatal(err)
			}
			if _, err := db.BackfillChronicLoadSnapshots(date, date); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ score, kind string }{{SubScoreAcuteRisk, TargetKindEventT1T3}, {SubScoreChronicLoad, TargetKindChronicLabel}, {SubScoreChronicLoad, TargetKindChronicAcuteDensity}, {SubScoreAcuteRisk, TargetKindEventStrictT1T3}} {
				id := tc.score + "_" + tc.kind + "_" + scenario
				var result targetWriteSpec
				var cov struct {
					Days             []map[string]any `json:"per_day"`
					Threshold        int              `json:"breach_threshold"`
					DensityThreshold int              `json:"acute_density_threshold"`
				}
				var payload []byte
				err := db.pool.QueryRow(context.Background(), "SELECT eligible,eligibility_reason,target_value,data_coverage FROM target_snapshots WHERE date=$1 AND sub_score=$2 AND target_kind=$3", date, tc.score, tc.kind).Scan(&result.Eligible, &result.Reason, &result.Value, &payload)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(payload, &cov); err != nil {
					t.Fatal(err)
				}
				if tc.kind == TargetKindChronicAcuteDensity {
					cov.Threshold = cov.DensityThreshold
				}
				days := []map[string]any{}
				for _, cell := range cov.Days {
					days = append(days, map[string]any{"date": cell["date"], "observed": cell["recovery_observed"], "z": cell["z"], "acute_or": cell["acute_or"], "acute_observed": cell["acute_observed"], "hrv_z": cell["hrv_z"], "rhr_z": cell["rhr_z"]})
					if tc.score == SubScoreAcuteRisk {
						days[len(days)-1]["observed"] = cell["hrv"] != nil || cell["rhr"] != nil
					}
				}
				if tc.score == SubScoreAcuteRisk && scenario == "missing" {
					days = append(days, map[string]any{"date": anchor.AddDate(0, 0, 1).Format(isoDate), "observed": true})
				}
				encoded, err := json.Marshal(days)
				if err != nil {
					t.Fatal(err)
				}
				chipExec(t, db, "INSERT INTO source_data.chip_target_input VALUES ($1,$2,$3,$4,$5,$6,$7,NULL)", id, tc.score, anchor, history, cov.Threshold, json.RawMessage(encoded), tc.kind)
				compareChipTarget(t, db, id, result)
			}
		})
	}
}

func TestChipDBTCalibration(t *testing.T) {
	for _, scenario := range []struct {
		id, epoch string
		n, pos    int
		scale     float64
	}{
		{"epoch_unknown", "unknown", 90, 10, 1}, {"eligible_89", "device", 89, 10, 1}, {"positive_9", "device", 90, 9, 1}, {"p80", "device", 90, 10, 1}, {"rate_floor", "device", 90, 40, 0.1},
	} {
		t.Run(scenario.id, func(t *testing.T) {
			db := chipDB(t)
			anchor := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
			id := scenario.id
			epoch := scenario.epoch
			chipExec(t, db, "INSERT INTO source_data.chip_calibration_input VALUES ($1,'acute_risk','event_t1_t3',$2,$3)", id, epoch, anchor)
			chipExec(t, db, `INSERT INTO source_data.chip_pairs
    SELECT $1,$2::date-n,'device','device','event_base_rate',(n::float8/100)*$5::float8,(n<$4::int)::int,true
    FROM generate_series(0,$3::int-1)n`, id, anchor, scenario.n, scenario.pos, scenario.scale)
			chipExec(t, db, `INSERT INTO source_data.chip_pairs SELECT $1,$2::date-n,'device',label_epoch,kind,predicted,label,eligible
    FROM (VALUES (100,'other','event_base_rate',0.2,1,true),(101,'device','other',0.2,1,true),
     (102,'device','event_base_rate',null,1,true),(103,'device','event_base_rate',0.2,null,true),
     (104,'device','event_base_rate',0.2,1,false),(-1,'device','event_base_rate',0.2,1,true),
     (180,'device','event_base_rate',0.2,1,true))v(n,label_epoch,kind,predicted,label,eligible)`, id, anchor)
			chipExec(t, db, `INSERT INTO naive_baselines(date,sub_score,target_kind,baseline_kind,predicted_value,source_epoch,formula_version)
    SELECT day::text,'acute_risk','event_t1_t3',baseline_kind,predicted,prediction_epoch,1 FROM source_data.chip_pairs WHERE case_id=$1`, id)
			chipExec(t, db, `INSERT INTO target_snapshots(date,sub_score,target_kind,target_value,eligible,eligibility_reason,source_epoch,formula_version)
    SELECT day::text,'acute_risk','event_t1_t3',label,eligible,'ok',label_epoch,1 FROM source_data.chip_pairs WHERE case_id=$1`, id)
			goCal, err := db.recomputeOneChipCalibration(SubScoreAcuteRisk, TargetKindEventT1T3, epoch, anchor)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.epoch != "unknown" && scenario.n >= 90 && scenario.pos >= 10 {
				preds := make([]float64, scenario.n)
				for i := range preds {
					preds[i] = float64(float32(float64(i) / 100 * scenario.scale))
				}
				wantP80 := percentile(preds, 80)
				wantRate := float64(scenario.pos) / float64(scenario.n)
				wantCutoff := math.Max(wantP80, wantRate)
				if !chipValueEqual(goCal.P80, &wantP80) || !chipValueEqual(goCal.BaseRate, &wantRate) || !chipValueEqual(goCal.Cutoff, &wantCutoff) {
					t.Fatal("persisted source calibration differs from independent percentile and label rate")
				}
			}
			var n, pos int
			var status string
			var p80, rate, cutoff *float64
			err = db.pool.QueryRow(context.Background(), "SELECT n_eligible,n_positives,calibration_status,p80,base_rate,cutoff FROM dbt_output.chip_calibration_v1 WHERE case_id=$1", id).Scan(&n, &pos, &status, &p80, &rate, &cutoff)
			if err != nil || n != goCal.NEligible || pos != goCal.NPositives || status != goCal.Status || !chipValueEqual(p80, goCal.P80) || !chipValueEqual(rate, goCal.BaseRate) || !chipValueEqual(cutoff, goCal.Cutoff) {
				t.Fatalf("%s: dbt %d/%d %s vs Go %d/%d %s: %v", id, pos, n, status, goCal.NPositives, goCal.NEligible, goCal.Status, err)
			}
		})
	}
}

// Production baseline builders read only eligible daily history within the
// selected source epoch. The binary reference uses strictly preceding labels.
func TestChipDBTNaiveBaselines(t *testing.T) {
	db := chipDB(t)
	anchor := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"history", "epoch_boundary", "empty", "sparse"} {
		epoch := ""
		var sqlEpoch any
		if scenario == "epoch_boundary" {
			epoch = anchor.AddDate(0, 0, -4).Format(isoDate)
			sqlEpoch = epoch
		}
		for _, score := range []string{SubScoreRecoveryStability, SubScorePassiveEfficiency, SubScoreAcuteRisk, SubScoreChronicLoad} {
			days := []map[string]any{}
			eff := map[string]health.SleepEfficiencyResult{}
			walk := map[string]health.WalkingHREligibilityResult{}
			labels := map[string]int{}
			for offset := -136; offset <= 1; offset++ {
				d := anchor.AddDate(0, 0, offset).Format(isoDate)
				eligible := scenario != "empty" && offset%7 != 0
				if scenario == "sparse" {
					eligible = offset == -135 || offset == 0 || offset == 1
				}
				if scenario == "epoch_boundary" {
					eligible = offset < -4 || offset == 1
				}
				v := 0.7 + float64((offset+136)%15)*0.01
				if score == SubScorePassiveEfficiency {
					v *= 100
				}
				if score == SubScoreAcuteRisk || score == SubScoreChronicLoad {
					v = 0
					if offset%4 == 0 || offset >= 0 {
						v = 1
					}
					if eligible {
						labels[d] = int(v)
					}
				}
				days = append(days, map[string]any{"date": d, "value": v, "eligible": eligible})
				eff[d] = health.SleepEfficiencyResult{Eligible: eligible, Efficiency: ptrFloat(v)}
				walk[d] = health.WalkingHREligibilityResult{Eligible: eligible, Value: ptrFloat(v)}
			}
			var expected []naiveBaselineRow
			switch score {
			case SubScoreRecoveryStability:
				expected = buildRecoveryNaiveBaselines(anchor, epoch, eff)
			case SubScorePassiveEfficiency:
				expected = buildPassiveEfficiencyNaiveBaselines(anchor, epoch, walk)
			default:
				kinds := []string{TargetKindEventT1T3, TargetKindEventStrictT1T3}
				if score == SubScoreChronicLoad {
					kinds = []string{TargetKindChronicLabel, TargetKindChronicAcuteDensity}
				}
				for _, kind := range kinds {
					if err := db.saveEventBaseRateBaseline(context.Background(), anchor, anchor.Format(isoDate), score, kind, labels, InitialSourceEpoch, epoch, 1); err != nil {
						t.Fatal(err)
					}
					var row naiveBaselineRow
					var reason *string
					err := db.pool.QueryRow(context.Background(), "SELECT target_kind,baseline_kind,predicted_value::float8,reason FROM naive_baselines WHERE date=$1 AND sub_score=$2 AND target_kind=$3", anchor.Format(isoDate), score, kind).Scan(&row.TargetKind, &row.BaselineKind, &row.Value, &reason)
					if err != nil {
						t.Fatal(err)
					}
					if reason != nil {
						row.Reason = *reason
					}
					expected = append(expected, row)
				}
			}
			if score == SubScoreRecoveryStability || score == SubScorePassiveEfficiency {
				lookup := walkingHRLookup(walk)
				targets := []string{TargetKindDailyPoint, TargetKindRolling3d}
				if score == SubScoreRecoveryStability {
					lookup = sleepEfficiencyLookup(eff)
					targets = append(targets, TargetKindRolling3dCandidate2of3)
				}
				live, err := db.chipBaselines(context.Background(), anchor, epoch, score, lookup, targets...)
				if err != nil || len(live) != len(expected) {
					t.Fatalf("live baseline count %d vs %d: %v", len(live), len(expected), err)
				}
				byKey := map[[2]string]naiveBaselineRow{}
				for _, r := range live {
					byKey[[2]string{r.TargetKind, r.BaselineKind}] = r
				}
				for _, w := range expected {
					r, ok := byKey[[2]string{w.TargetKind, w.BaselineKind}]
					if !ok || r.Reason != w.Reason || !chipValueEqual(r.Value, w.Value) {
						t.Fatal("continuous SQL baseline differs from Go oracle", score, w.TargetKind, w.BaselineKind, scenario)
					}
				}
			} else {
				want := priorEventBaseRate(anchor, 90, epoch, labels)
				if want != nil {
					*want = float64(float32(*want))
				}
				for _, r := range expected {
					if !chipValueEqual(r.Value, want) {
						t.Fatal("persisted binary baseline differs from Go oracle", score, scenario)
					}
				}
			}
			data, err := json.Marshal(days)
			if err != nil {
				t.Fatal(err)
			}
			seeded := map[string]bool{}
			for _, want := range expected {
				id := score + "_" + want.TargetKind + "_" + scenario
				if !seeded[id] {
					chipExec(t, db, "INSERT INTO source_data.chip_baseline_input VALUES ($1,$2,$3,$4,$5,$6)", id, score, want.TargetKind, anchor, sqlEpoch, json.RawMessage(data))
					seeded[id] = true
				}
				var value *float64
				var reason string
				err := db.pool.QueryRow(context.Background(), "SELECT predicted,reason FROM dbt_output.chip_baseline_v1 WHERE case_id=$1 AND baseline_kind=$2", id, want.BaselineKind).Scan(&value, &reason)
				// The binary writer persists REAL; compare at that same precision.
				if value != nil && want.BaselineKind == BaselineKindEventBaseRate {
					*value = float64(float32(*value))
				}
				if err != nil || reason != want.Reason || !chipValueEqual(value, want.Value) {
					gotJSON, _ := json.Marshal(value)
					wantJSON, _ := json.Marshal(want.Value)
					t.Fatalf("%s/%s value=%s reason=%s want=%s/%s: %v", id, want.BaselineKind, gotJSON, reason, wantJSON, want.Reason, err)
				}
			}
		}
	}
}

// Forward corrections and observations outside the epoch must never enter features.
func TestChipDBTFeatureWindows(t *testing.T) {
	db := chipDB(t)
	ctx := context.Background()
	date := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"full", "sparse", "empty", "flat", "epoch", "no_anchor"} {
		t.Run(scenario, func(t *testing.T) {
			vals := map[string]float64{}
			for n := -1; n <= 541; n++ {
				if scenario == "empty" || (scenario == "sparse" && n%17 != 0) || (scenario == "no_anchor" && n == 0) {
					continue
				}
				v := 43 + float64((n+3)%13)/5
				if scenario == "flat" {
					v = 42
				}
				vals[date.AddDate(0, 0, -n).Format(isoDate)] = v
			}
			lookup := func(day string) (*float64, bool) {
				v, ok := vals[day]
				if !ok {
					return nil, false
				}
				return &v, true
			}
			epoch := ""
			if scenario == "epoch" {
				epoch = date.AddDate(0, 0, -6).Format(isoDate)
			}
			got, err := db.chipFeatureWindow(ctx, date, epoch, 540, lookup)
			if err != nil {
				t.Fatal(err)
			}
			m7, n7 := windowMean(date, 7, epoch, lookup)
			m30, _ := windowMean(date, 30, epoch, lookup)
			m45, sd, n45 := windowStatsBefore(date.AddDate(0, 0, 1), 45, epoch, lookup)
			_, _, n180 := windowStatsBefore(date.AddDate(0, 0, 1), 180, epoch, lookup)
			adaptive, nAdaptive := windowEWMA(date, 45, epoch, lookup)
			slow, nSlow := windowEWMA(date, 180, epoch, lookup)
			today, _ := lookup(date.Format(isoDate))
			var z, dAdaptive, dSlow *float64
			if today != nil {
				z = zScoreOrNil(*today, m45, sd)
				if adaptive != nil {
					dAdaptive = ptrFloat(*today - *adaptive)
				}
				if slow != nil {
					dSlow = ptrFloat(*today - *slow)
				}
			}
			for _, pair := range [][2]*float64{{got.Today, today}, {got.Mean7, m7}, {got.Mean30, m30}, {got.Mean45, m45}, {got.SD45, sd},
				{got.Z45, z}, {got.Adaptive, adaptive}, {got.Slow, slow}, {got.DeltaAdaptive, dAdaptive}, {got.DeltaSlow, dSlow}} {
				if !chipValueEqual(pair[0], pair[1]) {
					t.Fatal("SQL window differs from independent Go formula", scenario)
				}
			}
			if got.N7 != n7 || got.N45 != n45 || got.N180 != n180 || got.AdaptiveN != nAdaptive || got.SlowN != nSlow {
				t.Fatal("SQL window counts differ", scenario)
			}
			// The dbt model reads the same dated observations with both outside bounds supplied.
			var days []chipBaselineDay
			for day, v := range vals {
				days = append(days, chipBaselineDay{Date: day, Value: v, Eligible: true})
			}
			if days == nil {
				days = []chipBaselineDay{}
			}
			payload, err := json.Marshal(days)
			if err != nil {
				t.Fatal(err)
			}
			chipExec(t, db, "insert into source_data.chip_baseline_input values($1,'passive_efficiency','daily_point',$2,nullif($3,'')::date,$4)", scenario, date, epoch, json.RawMessage(payload))
			var ref *float64
			if err := db.pool.QueryRow(ctx, "select slow_value from dbt_output.chip_feature_windows_v1 where case_id=$1", scenario).Scan(&ref); err != nil || !chipValueEqual(ref, got.Slow) {
				t.Fatalf("dbt full input comparison: %v", err)
			}
		})
	}
}
