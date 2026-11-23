package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"testing"
	"time"
)

// TestEnergyBankDBTParity reads the same physical inputs as the application.
// Capture rows are immutable: insert a new ID for each late correction.
func TestEnergyBankDBTParity(t *testing.T) {
	if os.Getenv("ENERGY_BANK_PILOT") != "synthetic-only" {
		t.Skip("run experiments/dbt_metrics/scripts/test_energy_bank_shadow.sh")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Place the observed writer near noon, well away from the EOD replay slot.
	offset := time.Now().UTC().Hour() - 12
	tz := fmt.Sprintf("Etc/GMT%+d", offset)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Now().In(loc).Format(time.DateOnly)
	seed := func() {
		t.Helper()
		exec(`DELETE FROM daily_scores`)
		exec(`INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories,sustained_hr_load,stress_flags)
			SELECT ($1::date-n)::text, 6.0+n%3, 0.6+0.2*(n%3),1.1,0.25,250+5*n,1.5,
				   CASE WHEN n=0 THEN ARRAY['sustained_load'] ELSE ARRAY[]::text[] END
			FROM generate_series(0,20) n`, anchor)
	}
	sameFloat := func(name string, a, b float64) {
		t.Helper()
		if math.IsNaN(a) || math.IsNaN(b) || math.Abs(a-b) > 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b))) {
			t.Fatalf("%s: Go %g, dbt %g", name, a, b)
		}
	}
	// Compare before rounding as well as the persisted result. Projected sleep
	// is supplied to both readers when a rollout owns a day's input.
	check := func(profile, id, kind, wantState string, projected map[string]*LocalShadowSleep) BankResult {
		t.Helper()
		cfg := db.GetEnergyConfig()
		var inputs map[string]dailyInputs
		var got BankResult
		var err error
		if projected == nil {
			inputs, err = db.loadDailyEnergyInputs(ctx, subtractDays(anchor, 20), anchor)
			if err == nil {
				got, err = db.ComputeBankForDate(ctx, tz, anchor)
			}
		} else {
			inputs, err = db.loadServedSleepBankInputs(ctx, anchor, projected)
			if err == nil {
				got, err = db.computeServedSleepBank(ctx, anchor, projected, cfg)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		oracle, precise := calculateCalendarEnergyBank(inputs, anchor, cfg)
		if got.Bank != oracle.Bank || got.Display != oracle.Display || got.State != oracle.State ||
			got.TodayDrain != oracle.TodayDrain || got.TodayRestore != oracle.TodayRestore ||
			got.FormulaVersion != oracle.FormulaVersion || !slices.Equal(got.Flags, oracle.Flags) ||
			(got.Components == nil) != (oracle.Components == nil) || len(got.Components) != len(oracle.Components) {
			t.Fatalf("%s/%s runtime SQL %+v, previous Go solver %+v", profile, id, got, oracle)
		}
		sameFloat("oracle_alpha", got.AlphaUsed, oracle.AlphaUsed)
		for key, expected := range oracle.Components {
			value, ok := got.Components[key]
			if !ok {
				t.Fatalf("missing runtime SQL component %s", key)
			}
			sameFloat(key, value, expected)
		}
		exec(`INSERT INTO energy_capture.runs(profile,capture_id,target_day,snapshot_kind,formula_version,alpha,factor,stress_enabled,beta)
			VALUES ($1,$2,$3::text::date,$4,$5,$6,$7,$8,$9)`, profile, id, anchor, kind, cfg.FormulaVersion, cfg.Alpha, cfg.AlphaFactor, cfg.StressDrainEnabled, cfg.Beta)
		// Production pgx reads the daily real-valued columns through the text
		// protocol. Preserve that decimal representation when freezing as float8.
		// Use independent SQL for the ordinary daily cache; overlay the frozen
		// projected fields explicitly to exercise source switches and withdrawals.
		exec(`INSERT INTO energy_capture.days (profile,capture_id,day,sleep_total,deep,rem,awake,active_kcal,hr_load,stress_flags)
			SELECT $1,$2,date::date,sleep_total::text::float8,sleep_deep::text::float8,sleep_rem::text::float8,
                sleep_awake::text::float8,calories::text::float8,sustained_hr_load::text::float8,stress_flags
			FROM daily_scores WHERE date BETWEEN $3 AND $4`, profile, id, subtractDays(anchor, 20), anchor)
		for day, block := range projected {
			exec(`INSERT INTO energy_capture.days(profile,capture_id,day,sleep_total,deep,rem,awake)
				VALUES($1,$2,$3::text::date,$4,$5,$6,$7) ON CONFLICT(profile,capture_id,day)
				DO UPDATE SET sleep_total=excluded.sleep_total, deep=excluded.deep, rem=excluded.rem, awake=excluded.awake`,
				profile, id, day, block.Total, block.Deep, block.REM, block.Awake)
		}
		var state string
		var flags []string
		var bank, display, drain, restore, version int
		var visible *int
		var alpha, bankPrecise float64
		var raw []byte
		err = db.pool.QueryRow(ctx, `SELECT state, flags, formula_version, alpha_used, unrounded_bank,signed_bank,display_bank,
			visible_bank,drain_delta,restore_delta,components FROM dbt_output.semantic_energy_bank_v3
			WHERE profile=$1 AND capture_id=$2`, profile, id).Scan(&state, &flags, &version, &alpha, &bankPrecise, &bank, &display, &visible, &drain, &restore, &raw)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != wantState || state != got.State || !slices.Equal(got.Flags, flags) || version != got.FormulaVersion ||
			got.Bank != bank || got.Display != display || got.TodayDrain != drain || got.TodayRestore != restore ||
			(state == "stale") != (visible == nil) || (visible != nil && *visible != display) {
			t.Fatalf("%s/%s Go %+v; dbt %s %v %d %d %d %d %d", profile, id, got, state, flags, version, bank, display, drain, restore)
		}
		sameFloat("unrounded_bank", precise, bankPrecise)
		sameFloat("alpha_used", got.AlphaUsed, alpha)
		var components map[string]float64
		if raw != nil {
			if err = json.Unmarshal(raw, &components); err != nil {
				t.Fatal(err)
			}
		}
		if (got.Components == nil) != (components == nil) || len(components) != len(got.Components) {
			t.Fatal("component keys differ", id)
		}
		for key, want := range got.Components {
			val, ok := components[key]
			if !ok {
				t.Fatal("missing component", key)
			}
			sameFloat(key, want, val)
		}
		t.Logf("%s/%s: %s, bank %d", profile, id, state, bank)
		return got
	}
	seed()
	if err := db.SaveSettings(map[string]string{"energy.formula_version": "3"}); err != nil {
		t.Fatal(err)
	}
	observed := check("first", "morning", "observed_intraday", "fresh", nil)
	if !NewEnergyV2Orchestrator().Refresh(ctx, db, "synthetic", tz) {
		t.Fatal("live refresh failed")
	}
	assertStored := func(profile, id string, eod bool) {
		t.Helper()
		var same bool
		err := db.pool.QueryRow(ctx, `SELECT s.bank=m.signed_bank AND s.drain_delta=m.drain_delta
			AND s.restore_delta=m.restore_delta AND s.formula_version=m.formula_version AND s.flags=m.persisted_flags
			AND (s.components IS NULL)=(m.components IS NULL)
			AND (s.components IS NULL OR (
			   (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(s.components) k) =
			   (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(m.components) k)
			   AND NOT EXISTS (SELECT FROM jsonb_each_text(s.components) x WHERE
			     abs(x.value::float8-(m.components->>x.key)::float8)>1e-9)))
			FROM energy_snapshots s CROSS JOIN dbt_output.semantic_energy_bank_v3 m
			WHERE m.profile=$1 AND m.capture_id=$2 AND s.date=m.target_day::text
			AND ('backfilled'=ANY(s.flags))=$3`, profile, id, eod).Scan(&same)
		if err != nil || !same {
			t.Fatalf("%s/%s persisted parity: %v, %v", profile, id, same, err)
		}
	}
	assertStored("first", "morning", false)
	exec(`UPDATE daily_scores SET calories=calories+350 WHERE date=$1`, anchor)
	revised := check("first", "eod", "retrospective_eod", "fresh", nil)
	if revised.Bank == observed.Bank {
		t.Fatal("correction did not exercise change")
	}
	progress, err := db.BackfillEnergyRange(ctx, tz, anchor, anchor, false, nil)
	if err != nil || progress.OK != 1 {
		t.Fatalf("backfill %+v: %v", progress, err)
	}
	assertStored("first", "morning", false)
	assertStored("first", "eod", true)
	exec(`UPDATE daily_scores SET sleep_awake=NULL,calories=NULL WHERE date >= $1`, subtractDays(anchor, 1))
	check("first", "two-missing", "observed_intraday", "estimated", nil)
	exec(`UPDATE daily_scores SET sleep_total=NULL WHERE date >= $1`, subtractDays(anchor, 4))
	check("first", "five-missing", "retrospective_eod", "stale", nil)
	progress, err = db.BackfillEnergyRange(ctx, tz, anchor, anchor, false, nil)
	if err != nil || progress.Skipped != 1 {
		t.Fatalf("stale EOD %+v: %v", progress, err)
	}
	assertStored("first", "eod", true)
	check("first", "stale-observation", "observed_intraday", "stale", nil)
	if !NewEnergyV2Orchestrator().Refresh(ctx, db, "synthetic", tz) {
		t.Fatal("stale refresh failed")
	}
	// Multiple five-minute buckets are possible if running across a boundary.
	var staleStored bool
	err = db.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM energy_snapshots s JOIN dbt_output.semantic_energy_bank_v3 m
		ON m.profile='first' AND m.capture_id='stale-observation' WHERE s.flags=m.persisted_flags AND s.components IS NULL)`).Scan(&staleStored)
	if err != nil || !staleStored {
		t.Fatalf("stale observation suppression: %t %v", staleStored, err)
	}
	seed()
	highKcal := 1800.0
	exec(`UPDATE daily_scores SET calories=$1`, highKcal)
	if err = db.SaveSettings(map[string]string{"energy.stress_drain_enabled": "true", "energy.alpha_factor": "1.25"}); err != nil {
		t.Fatal(err)
	}
	other := check("second", "morning", "observed_intraday", "fresh", nil)
	if other.Bank != -50 || other.Display != 0 {
		t.Fatal("signed/display floor not exercised")
	}
	var preserved int
	err = db.pool.QueryRow(ctx, `SELECT signed_bank FROM dbt_output.semantic_energy_bank_v3 WHERE profile='first' AND capture_id='morning'`).Scan(&preserved)
	if err != nil || preserved != observed.Bank {
		t.Fatal("cross-profile or late-correction contamination")
	}
	seed()
	exec(`UPDATE daily_scores SET calories=NULL WHERE date<$1`, subtractDays(anchor, 2))
	check("second", "no-prefix", "observed_intraday", "stale", nil)
	seed()
	exec(`UPDATE daily_scores SET calories=NULL WHERE date >= $1 AND date < $2`, subtractDays(anchor, 8), subtractDays(anchor, 2))
	exec(`UPDATE daily_scores SET calories=NULL WHERE date=$1`, anchor)
	check("second", "prefix-median", "retrospective_eod", "estimated", nil)
	three := 3.0
	part := 0.3
	projected := map[string]*LocalShadowSleep{anchor: {Total: &three, Deep: &part, REM: &part, Awake: &part}}
	check("second", "source-switch", "observed_intraday", "estimated", projected)
	projected[anchor] = &LocalShadowSleep{}
	check("second", "projected-deletion", "observed_intraday", "estimated", projected)
}
