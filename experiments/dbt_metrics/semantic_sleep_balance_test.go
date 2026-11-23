package dbtmetrics

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestComplexSleepBalanceParity(t *testing.T) {
	if os.Getenv("SLEEP_DOCKER_TOKEN") == "" {
		t.Skip("requires the synthetic balance fixture")
	}
	ctx := t.Context()
	tx := semanticSleepReadTransaction(t)
	// Encode inputs directly from their typed source rows. The Go oracle
	// independently selects the effective goal, clips episodes and walks the
	// coverage prefix before comparing any model outputs.
	rows, err := tx.Query(ctx, `select c.profile,c.capture,c.wake_date::text,c.zone,
		coalesce((select json_agg(json_build_object('EffectiveDate',effective_date,'Hours',hours,'Version','v1'))
			from source_data.balance_goal g where (g.profile,g.capture)=(c.profile,c.capture)),'[]'),
		coalesce((select json_agg(json_build_object('WakeDate',period_day,'CaptureState',state))
			from source_data.balance_coverage k where (k.profile,k.capture)=(c.profile,c.capture)),'[]'),
		coalesce((select json_agg(json_build_object('ID',id,'Start',start_at,'End',end_at,'Source','watch','InputHash','fixture','DurationAssessment',assessment))
			from source_data.balance_episode e where (e.profile,e.capture)=(c.profile,c.capture)),'[]'),
		m.window_start_date::text,m.calculated_through,m.state,m.confidence,coalesce(m.incomplete_reason,''),
		coalesce(m.last_complete_date::text,''),m.balance_hours,
		(select json_agg(json_build_object('WakeDate',period_day,'Start',starts_at,'End',ends_at,'GoalHours',goal_hours,
			'TotalSleepHours',total_hours,'DeltaHours',total_hours-goal_hours,'CaptureState',coverage,'DurationAssessment',assessment) order by period_day)
			from dbt_output.sleep_balance_period_v1 p where (p.profile,p.capture)=(c.profile,c.capture)
			and p.period_day<=m.last_complete_date)
	from source_data.balance_capture c left join dbt_output.sleep_duration_balance_v1 m using(profile,capture)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		var profile, name, day, zone, start, state, confidence, reason, last string
		var through time.Time
		var hours *float64
		var g, k, e, ps []byte
		if err := rows.Scan(&profile, &name, &day, &zone, &g, &k, &e, &start, &through, &state, &confidence, &reason, &last, &hours, &ps); err != nil {
			t.Fatal(err)
		}
		var goals []health.SleepGoal
		var coverage []health.SleepBalanceCoverage
		var episodes []health.CompletedSleepEpisode
		var periods []health.SleepDurationBalancePeriod
		for _, v := range []struct {
			raw    []byte
			target any
		}{{g, &goals}, {k, &coverage}, {e, &episodes}, {ps, &periods}} {
			if len(v.raw) > 0 {
				if err := json.Unmarshal(v.raw, v.target); err != nil {
					t.Fatal(err)
				}
			}
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		want, err := health.CalculateSleepDurationBalance(day, goals, coverage, episodes, loc)
		if err != nil {
			t.Fatal(err)
		}
		if want.WindowStartDate != start || !through.Equal(want.CalculatedThrough) || want.State != state || want.Confidence != confidence || want.IncompleteReason != reason || want.LastCompleteDate != last ||
			(want.BalanceHours == nil) != (hours == nil) || (hours != nil && math.Abs(*hours-*want.BalanceHours) > 1e-9) || len(periods) != len(want.Periods) {
			t.Fatalf("%s/%s balance mismatch", profile, name)
		}
		for i, p := range periods {
			w := want.Periods[i]
			if !p.Start.Equal(w.Start) || !p.End.Equal(w.End) || p.WakeDate != w.WakeDate || p.GoalHours != w.GoalHours || math.Abs(p.TotalSleepHours-w.TotalSleepHours) > 1e-9 || math.Abs(p.DeltaHours-w.DeltaHours) > 1e-9 || p.CaptureState != w.CaptureState || p.DurationAssessment != w.DurationAssessment {
				t.Fatalf("%s/%s period %s mismatch", profile, name, p.WakeDate)
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("balance captures %d", count)
	}
	t.Logf("balance captures=%d, all period and summary fields matched", count)
}
