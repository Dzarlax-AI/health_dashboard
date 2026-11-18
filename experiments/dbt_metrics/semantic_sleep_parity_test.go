package dbtmetrics

import (
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// Run against the already-loaded, single-profile shadow. The oracle reads a
// single snapshot and prints aggregate differences only, including for profiles.
func TestSemanticSleepParity(t *testing.T) {
	socket := os.Getenv("SEMANTIC_SHADOW_PG_SOCKET")
	if socket == "" {
		t.Skip("shadow socket not configured")
	}
	if !filepath.IsAbs(socket) {
		t.Fatal("local PostgreSQL socket directory is required")
	}
	port, err := strconv.ParseUint(os.Getenv("SEMANTIC_SHADOW_PG_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid shadow port")
	}
	params := url.Values{"host": {socket}, "port": {strconv.Itoa(int(port))}, "sslmode": {"disable"}}
	target := url.URL{Scheme: "postgres", Host: "localhost", User: url.User(os.Getenv("SEMANTIC_SHADOW_PG_USER")), Path: "/" + os.Getenv("SEMANTIC_SHADOW_PG_DB"), RawQuery: params.Encode()}
	ctx := t.Context()
	db, err := pgx.Connect(ctx, target.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var local bool
	if err := tx.QueryRow(ctx, "select inet_server_addr() is null").Scan(&local); err != nil || !local {
		t.Fatal("expected local socket")
	}
	rows, err := tx.Query(ctx, `select wake_date,duration_hours,source,source_epoch,algorithm_version,input_hash,
        capture_completeness,duration_assessment,finalization_state,claim_eligibility
        from source_data.completed_night_sleep order by wake_date`)
	if err != nil {
		t.Fatal(err)
	}
	var nights []health.CompletedNightSleep
	exact := map[string]health.CompletedNightSleep{}
	for rows.Next() {
		var n health.CompletedNightSleep
		if err := rows.Scan(&n.WakeDate, &n.DurationHours, &n.Source, &n.SourceEpoch, &n.AlgorithmVersion, &n.InputHash,
			&n.CaptureCompleteness, &n.DurationAssessment, &n.FinalizationState, &n.ClaimEligibility); err != nil {
			t.Fatal(err)
		}
		nights = append(nights, n)
		exact[n.WakeDate] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows, err = tx.Query(ctx, `select wake_date,status,duration_minutes,eligible_nights,reference_hours,reference_minutes,delta_minutes
        from dbt_output.semantic_sleep_day_v1 order by wake_date`)
	if err != nil {
		t.Fatal(err)
	}
	var days, selected, numeric, carry, comparableCounts, differentInputs, differentStatus, differentRounded int
	var maxHours float64
	for rows.Next() {
		var day, status string
		var minutes, count, refMinutes, delta *int
		var mean *float64
		if err := rows.Scan(&day, &status, &minutes, &count, &mean, &refMinutes, &delta); err != nil {
			t.Fatal(err)
		}
		days++
		date, err := time.Parse(time.DateOnly, day)
		if err != nil {
			t.Fatal(err)
		}
		fact := health.NewSleepDurationComparison(date)
		fact.Status = health.SleepComparisonNoNight
		// Morning selection can use the previous observed night on a gap.
		// The dated comparison deliberately leaves the calendar date empty.
		morning := health.SelectMorningReportSleep(day, nights)
		if n, ok := exact[day]; ok {
			selected++
			if morning.Date != day {
				t.Fatal("Go selected a different date for a present night")
			}
			if n.CaptureCompleteness == health.NightCaptureComplete && n.DurationAssessment == health.NightDurationPlausible {
				comparableCounts++
				if count == nil || *count != morning.BaselineNights || (mean == nil) != (morning.BaselineHours == nil) {
					differentInputs++
				}
				if mean != nil && morning.BaselineHours != nil {
					maxHours = math.Max(maxHours, math.Abs(*mean-*morning.BaselineHours))
				}
			}
			fact.SetDurations(n, morning.BaselineHours)
		} else if morning.Date != "" {
			carry++
		}
		if fact.Status != status {
			differentStatus++
		}
		if fact.DeltaMinutes != nil {
			numeric++
		}
		if !reflect.DeepEqual(minutes, fact.DurationMinutes) || !reflect.DeepEqual(refMinutes, fact.ReferenceMinutes) || !reflect.DeepEqual(delta, fact.DeltaMinutes) {
			differentRounded++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("dates=%d exact_nights=%d comparable_counts=%d numeric_pairs=%d historical_fallback_on_gaps=%d count_or_presence_mismatches=%d max_abs_reference_hours=%.12g status_mismatches=%d rounded_mismatches=%d", days, selected, comparableCounts, numeric, carry, differentInputs, maxHours, differentStatus, differentRounded)
	if days != 90 || differentInputs+differentStatus+differentRounded != 0 || maxHours > 1e-9 {
		t.Fatalf("sleep parity failed across %d dates", days)
	}
}
