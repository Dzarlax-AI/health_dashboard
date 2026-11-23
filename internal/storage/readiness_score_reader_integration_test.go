package storage

import (
	"context"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// Compare snapshots with the actual history reader and the same cache-backed
// date reader + briefing formula called by Today. The two inputs intentionally
// disagree on non-anchor dates. A second device supplies the more recent rows.
func TestReadinessScoreReaders(t *testing.T) {
	if os.Getenv("READINESS_SCORE_SHADOW_COMPARE") != "1" {
		t.Skip("local synthetic dbt score comparison")
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
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	const target = "2026-03-31"
	exec(`CREATE TEMP TABLE readiness_reader_days AS
	SELECT n, ('2026-03-31'::date-n)::text AS day,
		(50+n%5)::float8 AS hrv, (60+n%4)::float8 AS rhr, (7+(n%3)*0.25)::float8 AS sleep,
		(46+n%3)::float8 AS cached_hrv, (62+n%6)::float8 AS cached_rhr,
		(6+(n%5)*0.25)::float8 AS cached_sleep
	FROM generate_series(0,14) n WHERE n<>5`)
	id := insertTestRawRecord(t, db, "readiness-composite")
	exec(`INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality)
	SELECT $1,v.metric,v.units,day||' 07:00:00 +0000',v.qty,
		CASE WHEN n<6 THEN 'Apple Watch' ELSE 'RingConn' END,'ok'
	FROM readiness_reader_days CROSS JOIN LATERAL (VALUES
		('heart_rate_variability','ms',hrv),('resting_heart_rate','bpm',rhr),('sleep_total','hr',sleep)
	) v(metric,units,qty)`, id)
	exec(`INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total)
		SELECT day,cached_hrv,cached_rhr,cached_sleep FROM readiness_reader_days`)
	for _, reader := range []string{"history", "morning"} {
		path := "history"
		var rawScore, display int
		var band, reason, confidence, headline string
		var warm *health.RawMetrics
		var samples int
		var provisional, lowQuality bool
		if reader == "history" {
			h, e := unit.computeReadinessHistoryAt(ctx, 1, target)
			if e != nil || len(h) != 1 || h[0].Date != target {
				t.Fatalf("history %v: %v", h, e)
			}
			rawScore, display, band = h[0].Score, h[0].Score, health.ReadinessBand(h[0].Score)
		} else {
			path = "morning_warm"
			warm = unit.rawMetricsFromDailyScoresAt(target, true)
			if warm == nil || warm.ReadinessEvidence == nil {
				t.Fatal("morning window missing")
			}
			evidence := warm.ReadinessEvidence
			if !evidence.HRV.Present || !evidence.RHR.Present || !evidence.SleepDuration.Present {
				t.Fatal("morning evidence missing")
			}
			provisional = evidence.HRV.Confidence == health.ReadinessConfidenceProvisional
			samples = evidence.HRV.SampleCount
			lowQuality = evidence.SleepQuality.Present && evidence.SleepQuality.Confidence == health.ReadinessConfidenceLow
			projection, err := unit.morningReadinessProjection(ctx, warm)
			if err != nil {
				t.Fatal(err)
			}
			resp := health.ComputeReadinessBriefingWithProjection(*warm, "en", &projection)
			want := health.ComputeReadinessBriefing(*warm, "en")
			if !reflect.DeepEqual(resp, want) {
				t.Fatal("SQL-backed morning briefing differs from historical Go formula")
			}
			rawScore, display, band, confidence, reason = resp.ReadinessRawScore, resp.ReadinessDisplayScore, resp.ReadinessBand, resp.ReadinessConfidence, resp.ReadinessCapReason
			if resp.Headline != nil {
				headline = resp.Headline.Key
			}
		}
		exec(`INSERT INTO source_data.readiness_hrv_snapshot_inputs
			(snapshot_id,profile,target_date,reader,reader_path,snapshot_state,observed_date,raw_ms,cached_ms,repair_pending,cache_row_eligible,evidence_present)
			SELECT 'reader-hrv','full-readiness-test',$1,$2,$3,'captured',day,hrv,cached_hrv,false,true,true FROM readiness_reader_days`, target, reader, path)
		exec(`INSERT INTO source_data.readiness_score_inputs
			(case_id,profile,target_date,reader,hrv_snapshot_id,rhr,sleep,rhr_present,sleep_present,low_sleep_quality,hrv_provisional,hrv_samples,headline,illness)
			SELECT $2,'full-readiness-test',$1::text::date,$2,'reader-hrv',
			array_agg(CASE WHEN n=0 OR $2='history' THEN rhr ELSE cached_rhr END ORDER BY n),
			array_agg(CASE WHEN n=0 OR $2='history' THEN sleep ELSE cached_sleep END ORDER BY n),
			true,true,$3,$4,$5,$6,'' FROM readiness_reader_days`, target, reader, lowQuality, provisional, samples, headline)
		if warm != nil {
			var rhr, sleep []float64
			if err := tx.QueryRow(ctx, `SELECT rhr,sleep FROM source_data.readiness_score_inputs WHERE profile='full-readiness-test' AND case_id=$1`, reader).Scan(&rhr, &sleep); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(warm.RHR, rhr) || !slices.Equal(warm.Sleep, sleep) {
				t.Fatalf("reader-selected window differs: RHR %v / %v, sleep %v / %v", warm.RHR, rhr, warm.Sleep, sleep)
			}
		}
		var modelRaw, modelDisplay int
		var modelBand, modelReason, modelConfidence string
		if err := tx.QueryRow(ctx, `SELECT raw_score,display_score,band,coalesce(cap_reason,''),coalesce(confidence,'')
			FROM dbt_output.semantic_readiness_score_v1 WHERE profile='full-readiness-test' AND case_id=$1`, reader).Scan(&modelRaw, &modelDisplay, &modelBand, &modelReason, &modelConfidence); err != nil {
			t.Fatal(err)
		}
		if rawScore != modelRaw || display != modelDisplay || band != modelBand || reason != modelReason || confidence != modelConfidence {
			t.Fatalf("%s: Go %d/%d %q %q %q; dbt %d/%d %q %q %q", reader, rawScore, display, band, reason, confidence, modelRaw, modelDisplay, modelBand, modelReason, modelConfidence)
		}
	}
	present := health.ReadinessComponentEvidence{Present: true, SourceDate: target, Freshness: health.ReadinessFreshnessOK}
	base := health.ReadinessEvidenceInput{Date: target, HRV: present, RHR: present, SleepDuration: present}
	for _, tc := range []struct {
		name   string
		change func(*health.ReadinessEvidenceInput)
	}{
		{"complete", func(*health.ReadinessEvidenceInput) {}},
		{"sparse", func(e *health.ReadinessEvidenceInput) { e.HRV.SampleCount = 3 }},
		{"provisional", func(e *health.ReadinessEvidenceInput) { e.HRV.Confidence = health.ReadinessConfidenceProvisional }},
		{"missing_rhr_with_overnight", func(e *health.ReadinessEvidenceInput) { e.RHR.Present = false; e.OvernightHR = present }},
		{"withdrawn_hrv", func(e *health.ReadinessEvidenceInput) { e.HRV.Present = false }},
		{"low_sleep_quality", func(e *health.ReadinessEvidenceInput) {
			e.SleepQuality = present
			e.SleepQuality.Confidence = health.ReadinessConfidenceLow
		}},
		{"missing_sleep_and_illness", func(e *health.ReadinessEvidenceInput) {
			e.SleepDuration.Present = false
			e.IllnessConfidence = health.IllnessConfidenceHigh
		}},
		{"moderate_illness", func(e *health.ReadinessEvidenceInput) { e.IllnessConfidence = health.IllnessConfidenceModerate }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.change(&e)
			d := health.RawMetrics{LastDate: target, HRV: []float64{71, 56, 52, 52, 51, 53, 52, 50, 48, 49},
				RHR: []float64{52, 55, 55, 54, 56, 56, 57, 57, 58, 57}, Sleep: []float64{8, 8, 8, 7, 8, 7, 8, 7, 6, 7},
				ReadinessEvidence: &e}
			p, err := unit.morningReadinessProjection(ctx, &d)
			if err != nil {
				t.Fatal(err)
			}
			got, err := unit.computeBriefingSQL(ctx, &d, "en", &p)
			if err != nil {
				t.Fatal(err)
			}
			want := health.ComputeBriefing(d, "en")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("morning scores %d/%d, reasons %q/%q or downstream briefing differ", got.ReadinessScore, want.ReadinessScore, got.ReadinessCapReason, want.ReadinessCapReason)
			}
		})
	}
}
