package storage

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// Check full evidence presentation as well as confidence and pattern. Source
// threshold cases use the same signal adapter as the briefing and prompt.
func TestIllnessClassificationShadow(t *testing.T) {
	if os.Getenv("BRIEFING_STORAGE_PILOT") != "1" {
		t.Skip("requires isolated synthetic briefing models")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	n := 0
	confidences, patterns := map[string]bool{}, map[string]bool{}
	check := func(in health.IllnessEvidenceInput) {
		t.Helper()
		in.Date = "2026-11-18"
		want := health.ComputeIllnessSuspicion(in)
		got, err := unit.classifyIllness(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: classifier %s/%s vs Go %s/%s or evidence differs", n, got.Confidence, got.Pattern, want.Confidence, want.Pattern)
		}
		confidences[got.Confidence] = true
		patterns[got.Pattern] = true
		_, rules := health.IllnessClassificationInputs(in)
		data, err := json.Marshal(map[string]any{"illness_signals": rules})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "insert into briefing_pilot.captured_inputs values('illness-test',$1,$2)", n, json.RawMessage(data)); err != nil {
			t.Fatal(err)
		}
		var confidence, pattern string
		if err := tx.QueryRow(ctx, "select confidence,pattern from derived.illness_suspicion_v1 where profile='illness-test' and capture=$1", n).Scan(&confidence, &pattern); err != nil {
			t.Fatal(err)
		}
		if confidence != want.Confidence || pattern != want.Pattern {
			t.Fatal("dbt model differs from Go", n)
		}
		n++
	}
	metric := func(z float64) *health.MetricEvidenceInput {
		return &health.MetricEvidenceInput{Status: "ok", ZScore: z}
	}
	for bits := 0; bits < 256; bits++ {
		in := health.IllnessEvidenceInput{}
		if bits&1 != 0 {
			in.RespiratoryRate = metric(2)
		}
		if bits&2 != 0 {
			in.SpO2Average = metric(-2)
		}
		if bits&4 != 0 {
			in.WristTempDeviation = metric(1.5)
		}
		if bits&8 != 0 {
			in.SleepDisruption = metric(-1.5)
		}
		if bits&16 != 0 {
			in.SubjectiveCheckin = &health.SubjectiveCheckinSummary{Answer: "sick"}
		}
		if bits&32 != 0 {
			in.ObjectivePatternDays = 2
		}
		if bits&64 != 0 {
			in.SustainedHRLoad = metric(2)
			in.SustainedHRLoad.ActivityContext = "high"
		}
		if bits&128 != 0 {
			in.StressFlags = []string{"recovery_debt", "illness_signature", "illness_signature"}
		}
		check(in)
	}
	for _, value := range []float64{0.8, 1, 1.5, 2} {
		for _, z := range []float64{math.Nextafter(value, 0), value, math.Nextafter(value, 3)} {
			for _, days := range []int{1, 2, 3} {
				for _, activity := range []string{"normal", "high", "unknown"} {
					in := health.IllnessEvidenceInput{RHR: metric(z), HRV: metric(-z), RespiratoryRate: metric(z), SustainedHRLoad: metric(2), AutonomicPatternDays: days}
					in.SustainedHRLoad.ActivityContext = activity
					check(in)
				}
			}
		}
	}
	for _, status := range []string{"ok", "missing", "warmup"} {
		for _, count := range []int{0, 1, 2, 3} {
			in := health.IllnessEvidenceInput{SpO2LowCluster: &health.SpO2ClusterEvidence{Status: status, ValidReadings: 8, Below94Count: count, Below92Count: count}, SpO2Average: metric(-1.5), SubjectiveCheckin: &health.SubjectiveCheckinSummary{Answer: "fine"}}
			in.SpO2Average.Status = status
			check(in)
		}
	}
	for _, boundary := range []float64{0.7, 1.0} {
		for _, drop := range []float64{math.Nextafter(boundary, 0), boundary, math.Nextafter(boundary, 2)} {
			check(health.IllnessEvidenceInput{SpO2Average: &health.MetricEvidenceInput{Status: "ok", Baseline: drop, Value: 0}, SubjectiveCheckin: &health.SubjectiveCheckinSummary{Answer: "sick"}})
		}
	}
	for _, boundary := range []float64{1, 1.5, 2} {
		for _, v := range []float64{math.Nextafter(boundary, 0), boundary, math.Nextafter(boundary, 3)} {
			check(health.IllnessEvidenceInput{RespiratoryRate: metric(2), SleepDisruption: metric(-v), WristTempDeviation: metric(v), SpO2Average: metric(-v)})
			for _, days := range []int{1, 2, 3} {
				load := metric(v)
				load.ActivityContext = "normal"
				check(health.IllnessEvidenceInput{RHR: metric(1), SustainedHRLoad: load, AutonomicPatternDays: days, ObjectivePatternDays: days})
			}
		}
	}
	if _, err := tx.Exec(ctx, `insert into briefing_pilot.captured_inputs values('illness-missing',0,'{}'),('illness-missing',1,'{"illness_signals":null}')`); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := tx.QueryRow(ctx, "select count(*) from derived.illness_suspicion_v1 where profile='illness-missing'").Scan(&missing); err != nil || missing != 0 {
		t.Fatal("unavailable captures must produce no decision", err)
	}
	t.Logf("%d synthetic input combinations matched the previous Go classifier and the dbt view", n)
	if len(confidences) != 4 || len(patterns) != 5 {
		t.Fatal("coverage of confidence or pattern incomplete", confidences, patterns)
	}
}

func TestIllnessClassificationReaders(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	const date = "2026-10-08"
	if err := db.EnsureSubjectiveCheckinsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureContextPromptInteractionsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, "insert into hourly_metrics(metric_name,hour,source) values('step_count',$1,'fixture')", date+" 06:00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, "insert into daily_scores(date,sleep_total,stress_flags) values($1,4,array['illness_signature'])", date); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		gate, err := db.EvaluateLowSleepPromptUsefulness(date, strongLowSleepCandidate())
		if err != nil {
			t.Fatal(err)
		}
		if gate.IllnessConfidence != want || gate.Eligible != (want == "none") {
			t.Fatalf("prompt confidence %s eligible=%v reason=%s", gate.IllnessConfidence, gate.Eligible, gate.Reason)
		}
		brief, err := db.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		if brief.IllnessSuspicion == nil || brief.IllnessSuspicion.Confidence != want {
			t.Fatalf("briefing confidence differs for %s", want)
		}
		if want == "high" && (brief.ReadinessDisplayScore > 45 || gate.Reason != "active_illness_flow") {
			t.Fatal("existing illness safety caps changed")
		}
	}
	check("high")
	if _, err := db.pool.Exec(ctx, "update daily_scores set stress_flags='{}' where date=$1", date); err != nil {
		t.Fatal(err)
	}
	check("none")
}

func TestIllnessObjectivePatternUsesPersonalRHRForCurrentDate(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	const date = "2026-10-08"
	if _, err := db.pool.Exec(ctx, `insert into daily_scores(date,rhr_avg,resp_avg)
		select to_char(date '2026-10-08'-n,'YYYY-MM-DD'), 60+n%3, 14+n%3
		from generate_series(1,14) n;
		insert into daily_scores(date,rhr_avg,resp_avg) values('2026-10-08',90,23)`); err != nil {
		t.Fatal(err)
	}
	evidence := db.BuildIllnessEvidenceInput(date, nil)
	if !metricHigh(evidence.RespiratoryRate, 1.5) || !metricHigh(evidence.RHR, 1) || evidence.ObjectivePatternDays != 0 {
		t.Fatalf("daily RHR must not count as personal support: %+v", evidence)
	}
	if _, err := db.pool.Exec(ctx, `update daily_scores set baseline_hr_overnight=rhr_avg`); err != nil {
		t.Fatal(err)
	}
	evidence = db.BuildIllnessEvidenceInput(date, nil)
	if evidence.ObjectivePatternDays != 1 {
		t.Fatalf("expected current-day personal RHR to support the pattern: %+v", evidence)
	}
}
