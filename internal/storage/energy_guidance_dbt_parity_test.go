package storage

import (
	"context"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestEnergyGuidanceDBTParity(t *testing.T) {
	if os.Getenv("ENERGY_BANK_PILOT") != "synthetic-only" {
		t.Skip("requires the synthetic Energy pilot")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsureEnergySnapshotsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const day = "2026-10-15"
	cases := []struct {
		id                  string
		n, latest, bank     int
		z                   float64
		flags               []string
		confidence, pattern string
		flat, tuned         bool
		mode                string
	}{
		{"cold", 19, 19, 15, 1, []string{}, "", "", false, false, "default_warmup"},
		{"provisional", 20, 12, 90, 0.5, []string{"recovery_debt"}, "", "", false, false, "provisional_compatible_formula_warmup"},
		{"flat-provisional", 29, 29, 60, -1, []string{"parasympathetic_rebound"}, "", "", true, false, "default_warmup"},
		{"mature", 30, 30, 90, 0.5, []string{}, "moderate", "autonomic_prodrome", false, false, "personal_latest_formula"},
		{"mixed", 30, 10, 85, 0.9, []string{"recovery_debt", "illness_signature", "parasympathetic_rebound"}, "high", "", false, false, "personal_mixed_formula_warmup"},
		{"tuned", 32, 31, 80, 0, []string{"parasympathetic_rebound"}, "high", "", false, true, "default_warmup"},
		{"hrv-boundary", 0, 0, 90, -0.5, []string{"recovery_debt", "parasympathetic_rebound"}, "", "", false, false, "default_warmup"},
		{"bank-boundary", 0, 0, 41, 1, []string{}, "", "", false, false, "default_warmup"},
		{"push-boundary", 0, 0, 55, 0.5, []string{}, "", "", false, false, "default_warmup"},
		{"moderate-cap", 0, 0, 55, 0, []string{}, "moderate", "other", false, false, "default_warmup"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			exec("delete from energy_snapshots")
			factor := "1"
			if c.tuned {
				factor = "1.2"
			}
			if err := db.SaveSettings(map[string]string{"energy.formula_version": "2", "energy.alpha_factor": factor}); err != nil {
				t.Fatal(err)
			}
			exec(`update settings set updated_at='2026-10-01 00:00:00+00' where key='energy.alpha_factor'`)
			// Three clocks: local event date, within-day order, and coefficient-change time.
			exec(`insert into energy_snapshots(date,ts_bucket,computed_at,bank,drain_delta,restore_delta,formula_version,flags)
 select d::text,d+hour*interval '1 hour',d+interval '1 day',
   case when $4 then 80 when hour=10 then 100 else i*4-12 end,0,0,case when i<$3 then 2 else 1 end,
   case when hour=23 then array['imputed_activity'] else array[]::text[] end
 from generate_series(0,$2::int-1) i cross join (values(10),(20),(23)) hours(hour)
 cross join lateral (select $1::date-i as d) dates`, day, c.n, c.latest, c.flat)
			exec(`insert into energy_snapshots(date,ts_bucket,bank,drain_delta,restore_delta,formula_version)
 select d::text,d,100,0,0,2 from (values($1::date+1),($1::date-181)) dates(d)`, day)
			cfg := db.GetEnergyConfig()
			exec(`insert into energy_capture.decisions values('guidance',$1,$2::date,$3,$4,$5,$6,'2026-10-01 00:00+00',$7,$8,$9,$10,$11)`,
				c.id, day, cfg.FormulaVersion, cfg.EffectiveAlpha(), cfg.EffectiveBeta(), cfg.StressDrainEnabled, c.bank, c.z, c.flags, c.confidence, c.pattern)
			exec(`insert into energy_capture.band_history select 'guidance',$1,date::date,ts_bucket,computed_at,formula_version,bank,flags from energy_snapshots`, c.id)
			want, err := db.ComputeUserVerdictBandsThrough(ctx, day)
			if err != nil {
				t.Fatal(err)
			}
			var got health.VerdictBands
			var verdict, reason string
			var rebound bool
			err = db.pool.QueryRow(ctx, `select mode,latest_days,compatible_days,used_days,rest,recovery,push,verdict,reason,rebound from dbt_output.semantic_energy_guidance_v1 where profile='guidance' and capture_id=$1`, c.id).Scan(&got.CalibrationMode, &got.LatestFormulaDays, &got.CompatibleFormulaDays, &got.UsedDays, &got.Rest, &got.Recovery, &got.PushHard, &verdict, &reason, &rebound)
			if err != nil {
				t.Fatal(err)
			}
			if got.CalibrationMode != c.mode || got.CalibrationMode != want.CalibrationMode || got.Rest != want.Rest || got.Recovery != want.Recovery || got.PushHard != want.PushHard || got.LatestFormulaDays != want.LatestFormulaDays || got.CompatibleFormulaDays != want.CompatibleFormulaDays || got.UsedDays != want.UsedDays {
				t.Fatalf("bands Go %+v, SQL %+v", want, got)
			}
			ls := health.GetStrings("en")
			v := health.ChooseVerdictV2(c.z, c.bank, want)
			r := health.BuildVerdictReasonV2(v, c.bank, c.z, ls)
			v, r, _ = health.ApplyStressFlagVerdictOverride(v, r, c.flags, ls)
			resp := &health.BriefingResponse{EnergyBank: &health.EnergyBank{ActionVerdict: v, VerdictReason: r}, IllnessSuspicion: &health.IllnessSuspicion{Confidence: c.confidence, Pattern: c.pattern}}
			health.ApplyIllnessSafetyCap(resp, ls)
			live := &health.BriefingResponse{EnergyBank: &health.EnergyBank{Current: c.bank, HRVZRaw: c.z, Flags: c.flags}, IllnessSuspicion: resp.IllnessSuspicion}
			if err := db.applySnapshotGuidanceSQL(ctx, live, want, ls); err != nil {
				t.Fatal(err)
			}
			if live.EnergyBank.ActionVerdict != resp.EnergyBank.ActionVerdict || live.EnergyBank.VerdictReason != resp.EnergyBank.VerdictReason {
				t.Fatal("live SQL verdict differs from Go oracle")
			}
			rendered := energyParityReason(reason, c.z, 0, 0)
			if rebound {
				rendered += " " + ls["energy_reason_rebound_addon"]
			}
			if resp.EnergyBank.ActionVerdict != verdict || resp.EnergyBank.VerdictReason != rendered {
				t.Fatalf("Go %s %q; SQL %s %q", resp.EnergyBank.ActionVerdict, resp.EnergyBank.VerdictReason, verdict, rendered)
			}
		})
	}
}

func energyParityReason(key string, z float64, stress, strain int) string {
	template := health.GetStrings("en")["energy_reason_"+key]
	switch key {
	case "high_stress":
		return fmt.Sprintf(template, z, stress)
	case "acwr_spike":
		return fmt.Sprintf(template, float64(strain))
	}
	return template
}

func TestEnergyLegacyDBTParity(t *testing.T) {
	if os.Getenv("ENERGY_BANK_PILOT") != "synthetic-only" {
		t.Skip("requires the synthetic Energy pilot")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	for _, name := range []string{"ordinary", "heavy", "hrv-low", "hrv-high", "flat", "missing-sleep", "short-series", "missing-rhr"} {
		t.Run(name, func(t *testing.T) {
			d := health.RawMetrics{LastDate: "2026-10-15", StepsToday: 1200, StepsChronic28d: 5200, ActiveEnergyToday: 65, ActiveEnergyChronic28d: 430}
			for i := 0; i < 35; i++ {
				d.HRV = append(d.HRV, 40+float64(i%4))
				d.RHR = append(d.RHR, 63+float64(i%2))
				d.Sleep = append(d.Sleep, 8)
			}
			switch name {
			case "heavy":
				d.StepsToday = 20000
			case "hrv-low":
				d.HRV[0] = 32
			case "hrv-high":
				d.HRV[0] = 55
				d.ActiveEnergyToday = 0
				d.StepsToday = 0
			case "flat":
				for i := range d.HRV {
					d.HRV[i] = 42
				}
				d.StepsChronic28d = 1
			case "missing-sleep":
				d.Sleep[0] = 0
			case "short-series":
				d.HRV = d.HRV[:6]
			case "missing-rhr":
				d.RHR = []float64{}
			}
			readiness := health.ComputeReadinessBriefing(d, "en")
			headline := ""
			if readiness.Headline != nil {
				headline = readiness.Headline.Key
			}
			if _, err := db.pool.Exec(ctx, `insert into energy_capture.legacy values('legacy',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, name, readiness.ReadinessScore, headline, d.Sleep, d.HRV, d.RHR, d.StepsToday, d.StepsChronic28d, d.ActiveEnergyToday, d.ActiveEnergyChronic28d); err != nil {
				t.Fatal(err)
			}
			var state string
			var cap, current, drain, strain, stress *int
			var z *float64
			var v, r *string
			if err := db.pool.QueryRow(ctx, `select state,capacity,current,drained,strain,stress,hrv_z,verdict,reason from dbt_output.semantic_energy_legacy_v1 where profile='legacy' and capture_id=$1`, name).Scan(&state, &cap, &current, &drain, &strain, &stress, &z, &v, &r); err != nil {
				t.Fatal(err)
			}
			briefing := health.ComputeBriefing(d, "en")
			got := briefing.EnergyBank
			p, err := db.morningReadinessProjection(ctx, &d)
			if err != nil {
				t.Fatal(err)
			}
			live, err := db.computeBriefingSQL(ctx, &d, "en", &p)
			if err != nil {
				t.Fatal(err)
			}
			if live.EnergyBank != nil && got != nil && math.Abs(live.EnergyBank.HRVZRaw-got.HRVZRaw) <= 1e-9 {
				live.EnergyBank.HRVZRaw = got.HRVZRaw
			}
			if !reflect.DeepEqual(live.EnergyBank, got) {
				t.Fatalf("live fallback %+v; Go oracle %+v", live.EnergyBank, got)
			}
			if name == "missing-rhr" && (got != nil || state != "unavailable" || briefing.ReadinessScore <= 0) {
				t.Fatalf("missing resting HR must suppress only the fallback: %+v, state %s", got, state)
			}
			if got == nil {
				if state != "unavailable" || current != nil {
					t.Fatalf("expected unavailable, got %s", state)
				}
				return
			}
			if state != "available" || cap == nil || current == nil || drain == nil || strain == nil || stress == nil || z == nil || v == nil || r == nil {
				t.Fatal("incomplete legacy output", state)
			}
			if *cap != got.Capacity || *current != got.Current || *drain != got.DrainSoFar || *strain != got.Strain || *stress != got.Stress || math.Abs(*z-got.HRVZRaw) > 1e-9 || *v != got.ActionVerdict || energyParityReason(*r, *z, *stress, *strain) != got.VerdictReason {
				t.Fatalf("Go %+v; SQL %d %d %d %d %d %g %s %s", got, *cap, *current, *drain, *strain, *stress, *z, *v, *r)
			}
		})
	}
}

func TestEnergyMissingRHRBriefing(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	const day = "2026-08-12"
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,hrv_avg,sleep_total,sleep_deep,sleep_rem,sleep_awake)
		SELECT ($1::date-i)::text,42+i%3,7.5,1,1.5,0.2 FROM generate_series(0,29) i`, day); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source) VALUES('step_count',$1,'fixture')`, day+" 07:00:00"); err != nil {
		t.Fatal(err)
	}
	check := func(want *int, status string) {
		t.Helper()
		b, err := db.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		if b.EnergyBank != nil || b.ReadinessScore <= 0 || b.EnergyBankStatus != status || (want == nil) != (b.EnergyBankCurrent == nil) || (want != nil && *b.EnergyBankCurrent != *want) {
			t.Fatalf("readiness=%d legacy=%+v reserve=%v status=%q, want reserve=%v status=%q", b.ReadinessScore, b.EnergyBank, b.EnergyBankCurrent, b.EnergyBankStatus, want, status)
		}
	}
	check(nil, "")
	at, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	result := BankResult{Bank: 64, State: "fresh", FormulaVersion: 3}
	if err := db.UpsertEnergySnapshotAt(ctx, "UTC", at, result); err != nil {
		t.Fatal(err)
	}
	check(&result.Bank, "")
	result.State = "stale"
	if err := db.UpsertEnergySnapshotAt(ctx, "UTC", at, result); err != nil {
		t.Fatal(err)
	}
	check(nil, "stale")
}

// A final HRV correction can drive the fallback readiness to zero while a
// same-date observed reserve remains available. A retrospective estimate
// alone cannot turn this previously withheld card into an observed reserve.
func TestEnergyObservedReserveWithoutLegacyVerdict(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	day := "2026-08-12"
	at, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `insert into daily_scores(date,hrv_avg,rhr_avg,sleep_total,sleep_core,sleep_deep,sleep_rem,sleep_awake)
        select ($1::date-n)::text,case when n=0 then 42 else 90+n%4 end,60,8,5,1,2,0.4 from generate_series(0,14) n`, day); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `insert into hourly_metrics(metric_name,hour,source) values('resting_heart_rate',$1,'fixture')`, day+" 00:00"); err != nil {
		t.Fatal(err)
	}
	check := func(want *int, status string) {
		t.Helper()
		b, err := db.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		if b.Date != day || b.EnergyBank != nil || b.EnergyBankStatus != status || (b.EnergyBankCurrent == nil) != (want == nil) || (want != nil && *want != *b.EnergyBankCurrent) {
			t.Fatalf("date=%s readiness=%d bank=%+v standalone=%v status=%q, want %v %q", b.Date, b.ReadinessScore, b.EnergyBank, b.EnergyBankCurrent, b.EnergyBankStatus, want, status)
		}
	}
	write := func(ts time.Time, b int, state string, flags []string) {
		t.Helper()
		if err := db.UpsertEnergySnapshotAt(ctx, "UTC", ts, BankResult{Bank: b, State: state, Flags: flags, FormulaVersion: 3}); err != nil {
			t.Fatal(err)
		}
	}
	write(at.AddDate(0, 0, -1), 81, "fresh", nil)
	check(nil, "")
	write(at, 55, "fresh", []string{"backfilled"})
	check(nil, "")
	reserve := 76
	write(at, reserve, "fresh", nil)
	check(&reserve, "")
	historical := &health.BriefingResponse{Date: day}
	if err := db.applyHistoricalEnergySnapshot(ctx, historical, day, "en"); err != nil || historical.EnergyBank != nil || historical.EnergyBankCurrent != nil {
		t.Fatalf("historical replay without a verdict exposed an observed bank: %+v, %v", historical.EnergyBankCurrent, err)
	}
	zero := 0
	write(at, -5, "fresh", nil)
	check(&zero, "")
	write(at, 0, "stale", nil)
	check(nil, "stale")
}
