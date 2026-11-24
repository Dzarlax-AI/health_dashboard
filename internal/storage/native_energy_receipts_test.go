package storage

import (
	"testing"

	"health-receiver/internal/health"
)

func checkNativeEnergyReceipts(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into settings(key,value) values($1,'true') on conflict(key) do update set value='true'", SettingDecisionCapture)
	const date = "2026-03-31"
	exec(`insert into energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,flags,computed_at)
 select (date '2026-03-31'-n)::timestamp at time zone 'UTC',(date '2026-03-31'-n)::text,51,0,0,3,'{}',now()
 from generate_series(0,29) n`)
	bands, err := db.ComputeUserVerdictBandsThrough(ctx, date)
	if err != nil || bands.UsedDays != 30 || bands.Rest != 51 {
		t.Fatalf("initial band selection: %+v %v", bands, err)
	}
	exec("update energy_snapshots set bank=99 where date between '2026-03-02' and '2026-03-31'")
	raw := health.RawMetrics{LastDate: date, StepsToday: 1200, StepsChronic28d: 5200, ActiveEnergyToday: 65, ActiveEnergyChronic28d: 430}
	for i := 0; i < 35; i++ {
		raw.HRV = append(raw.HRV, 40+float64(i%4))
		raw.RHR = append(raw.RHR, 63+float64(i%2))
		raw.Sleep = append(raw.Sleep, 8)
	}
	projection, err := db.morningReadinessProjection(ctx, &raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := db.computeBriefingSQL(ctx, &raw, "en", &projection)
	if err != nil || response.EnergyBank == nil {
		t.Fatalf("legacy energy missing: %v", err)
	}
	check := func(id string, want float64) {
		t.Helper()
		var ok bool
		err := db.pool.QueryRow(ctx, `select count(*)=1 and coalesce(bool_and(numeric_value=$2),false)
       from tenant_native_metric_read_v1 where metric_id=$1 and sample_key->>'capture_id' like $3`, id, want, date+":%").Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("%s mismatch, wanted %g: %v", id, want, err)
		}
	}
	check("verdict_bands.rest", 51)
	check("verdict_bands.used_days", 30)
	check("verdict_bands.latest_formula_days", 30)
	check("verdict_bands.compatible_formula_days", 30)
	check("energy_bank.capacity", float64(response.EnergyBank.Capacity))
	check("energy_bank.current", float64(response.EnergyBank.Current))
	check("energy_bank.strain", float64(response.EnergyBank.Strain))
}
