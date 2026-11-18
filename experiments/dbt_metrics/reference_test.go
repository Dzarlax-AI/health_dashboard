package dbtmetrics

import (
	"context"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

const referenceDate = "2026-10-26" // Belgrade's fall DST transition is inside the window.

func referenceNight(day string, hours float64) health.CompletedNightSleep {
	return health.CompletedNightSleep{
		WakeDate: day, DurationHours: hours, Source: "Synthetic Watch", SourceEpoch: "epoch-1",
		AlgorithmVersion: "v1", InputHash: day, CaptureCompleteness: health.NightCaptureComplete,
		DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalFinal,
		ClaimEligibility: health.NightClaimEligible,
	}
}

func TestSleepReferenceParity(t *testing.T) {
	date, _ := time.Parse(time.DateOnly, referenceDate)
	night := func(offset int, hours float64) health.CompletedNightSleep {
		return referenceNight(date.AddDate(0, 0, offset).Format(time.DateOnly), hours)
	}
	current := night(0, 6.8)
	current.FinalizationState = health.NightFinalProvisional
	current.ClaimEligibility = health.NightClaimIneligible
	history := []health.CompletedNightSleep{night(-30, 7.5), night(-20, 7.5), night(-5, 7.5),
		night(-4, 7.5), night(-3, 7.5), night(-2, 7.5), night(-1, 7.5)}
	dates := make([]string, len(history))
	for i, n := range history {
		dates[i] = n.WakeDate
	}
	extra := []health.CompletedNightSleep{night(-31, 19), night(-6, 19), night(-7, 19), night(-8, 19), night(-9, 19), night(-10, 19), night(-11, 19), night(-12, 19)}
	extra[1].Source = "Other device"
	extra[2].SourceEpoch = "other epoch"
	extra[3].AlgorithmVersion = "other version"
	extra[4].ClaimEligibility = health.NightClaimIneligible
	extra[5].FinalizationState = health.NightFinalProvisional
	extra[6].CaptureCompleteness = health.NightCapturePartial
	extra[7].DurationAssessment = health.NightDurationOutlier
	base := append(append([]health.CompletedNightSleep{current}, extra...), history...)
	changed := func(index int, change func(*health.CompletedNightSleep)) []health.CompletedNightSleep {
		result := append([]health.CompletedNightSleep(nil), base...)
		change(&result[index])
		return result
	}
	withProvisionalHistory := changed(len(base)-1, func(n *health.CompletedNightSleep) {
		n.FinalizationState, n.ClaimEligibility = health.NightFinalProvisional, health.NightClaimIneligible
	})
	fixtures := []struct {
		name string
		rows []health.CompletedNightSleep
		days []string
		mean float64
	}{
		{"source changed", changed(0, func(n *health.CompletedNightSleep) { n.Source = "new device" }), []string{}, 0},
		{"epoch changed", changed(0, func(n *health.CompletedNightSleep) { n.SourceEpoch = "new epoch" }), []string{}, 0},
		{"algorithm changed", changed(0, func(n *health.CompletedNightSleep) { n.AlgorithmVersion = "v2" }), []string{}, 0},
		{"six nights", base[:len(base)-1], dates[:6], 0},
		{"late arrival", base, dates, 7.5},
		{"repeat", base, dates, 7.5},
		{"provisional history", withProvisionalHistory, dates[:6], 0},
		{"finalized same hash", base, dates, 7.5},
		{"old correction", changed(len(base)-7, func(n *health.CompletedNightSleep) { n.DurationHours = 8.5 }), dates, 7.5 + 1.0/7},
		{"eligible row removed", base[:len(base)-1], dates[:6], 0},
	}
	ctx := context.Background()
	db := openReferenceLab(t, ctx)
	if db != nil {
		const table = `"source_data"."completed_night_sleep"`
		if _, err := db.Exec(ctx, "create view source_data.reference_health_renderer as "+SleepDurationReferenceSQL(table)); err != nil {
			t.Fatal(err)
		}
		var same bool
		if err := db.QueryRow(ctx, `select pg_get_viewdef('source_data.reference_health_renderer'::regclass, false)
			= pg_get_viewdef('dbt_output.sleep_duration_reference_v1'::regclass, false)`).Scan(&same); err != nil || !same {
			t.Fatalf("Go and dbt render different definitions: equal=%v error=%v", same, err)
		}
	}
	versions := make(map[string]string)
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			old := health.SelectMorningReportSleep(referenceDate, f.rows)
			if old.Date != referenceDate || old.BaselineNights != len(f.days) || !matchesMean(old.BaselineHours, f.mean) {
				t.Fatalf("Go oracle differs from fixture: %+v", old)
			}
			if db == nil {
				return
			}
			writeSnapshot(t, ctx, db, f.rows)
			var low, high, version, source, epoch, algorithm string
			var count int
			var mean *float64
			var days []string
			err := db.QueryRow(ctx, `select window_start, window_end_exclusive, eligible_nights,
				reference_hours, eligible_dates, history_version, source, source_epoch, algorithm_version
				from dbt_output.sleep_duration_reference_v1 where wake_date=$1`, referenceDate).
				Scan(&low, &high, &count, &mean, &days, &version, &source, &epoch, &algorithm)
			if err != nil {
				t.Fatal(err)
			}
			if low != "2026-09-26" || high != referenceDate || count != old.BaselineNights ||
				!matchesMean(mean, f.mean) || !reflect.DeepEqual(days, f.days) ||
				source != f.rows[0].Source || epoch != f.rows[0].SourceEpoch || algorithm != f.rows[0].AlgorithmVersion {
				t.Fatalf("dbt differs: %s %s n=%d mean=%v dates=%v group=%s/%s/%s", low, high, count, mean, days, source, epoch, algorithm)
			}
			fact := health.NewSleepDurationComparison(date)
			fact.SetDurations(f.rows[0], mean)
			if f.mean == 7.5 && (fact.Status != health.SleepComparisonAvailable ||
				*fact.DurationMinutes != 408 || *fact.ReferenceMinutes != 450 || *fact.DeltaMinutes != -42) {
				t.Fatalf("rounded fact: %+v", fact)
			}
			versions[f.name] = version
		})
	}
	if db == nil {
		t.Log("Go oracle only. Run the isolated PostgreSQL driver for dbt parity.")
		return
	}
	if versions["late arrival"] != versions["repeat"] ||
		versions["late arrival"] != versions["finalized same hash"] ||
		versions["late arrival"] == versions["old correction"] ||
		versions["provisional history"] == versions["finalized same hash"] ||
		versions["eligible row removed"] != versions["six nights"] {
		t.Fatal("historical revisions did not track the selected inputs")
	}
	checkReferencePlan(t, ctx, db)
}

func matchesMean(actual *float64, expected float64) bool {
	if expected == 0 {
		return actual == nil
	}
	return actual != nil && math.Abs(*actual-expected) <= 1e-9
}

func openReferenceLab(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	port := os.Getenv("SLEEP_REFERENCE_PILOT_PORT")
	if port == "" {
		return nil
	}
	db, err := pgx.Connect(ctx, fmt.Sprintf("postgresql://synthetic_admin@127.0.0.1:%s/sleep_reference_lab?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(ctx) })
	var marker string
	err = db.QueryRow(ctx, "select token from public.synthetic_sleep_reference").Scan(&marker)
	if err != nil || marker == "" || marker != os.Getenv("SLEEP_REFERENCE_PILOT_MARKER") {
		t.Fatalf("synthetic lab identity mismatch: %v", err)
	}
	return db
}

func writeSnapshot(t *testing.T, ctx context.Context, db *pgx.Conn, rows []health.CompletedNightSleep) {
	t.Helper()
	if _, err := db.Exec(ctx, "truncate source_data.completed_night_sleep"); err != nil {
		t.Fatal(err)
	}
	for _, n := range rows {
		_, err := db.Exec(ctx, `insert into source_data.completed_night_sleep values
			($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, n.WakeDate, n.DurationHours, n.Source, n.SourceEpoch,
			n.AlgorithmVersion, n.InputHash, n.CaptureCompleteness, n.DurationAssessment, n.FinalizationState, n.ClaimEligibility)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func checkReferencePlan(t *testing.T, ctx context.Context, db *pgx.Conn) {
	t.Helper()
	for _, size := range []int{365, 10000} {
		_, err := db.Exec(ctx, fmt.Sprintf(`truncate source_data.completed_night_sleep;
			insert into source_data.completed_night_sleep
			select to_char(date '1999-01-01' + day, 'YYYY-MM-DD'), 7.5, 'Watch', 'initial',
			       'v1', 'synthetic-' || day, 'complete', 'plausible', 'final', 'eligible'
			  from generate_series(0,%d) as day;
			analyze source_data.completed_night_sleep`, size-1))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(ctx, `explain (analyze, buffers) select * from dbt_output.sleep_duration_reference_v1 where wake_date='1999-12-15'`)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		t.Logf("%d nights:\n%s", size, plan)
		if !strings.Contains(plan, "Index Cond:") || strings.Contains(plan, "Seq Scan") {
			t.Fatalf("unbounded query plan on %d nights", size)
		}
	}
}
