package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestComputeUserVerdictBands_UsesOneLatestEligibleRowPerDate(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	ctx := context.Background()
	for i := 0; i < energyBandsMinPoints; i++ {
		date := bandTestDate(i)
		insertEnergySnapshot(t, db, date, 8, 100, 2, nil)
		insertEnergySnapshot(t, db, date, 20, i, 2, nil)
	}

	bands, err := db.ComputeUserVerdictBands(ctx)
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "personal_latest_formula" {
		t.Fatalf("CalibrationMode = %q, want personal_latest_formula", bands.CalibrationMode)
	}
	if bands.NDataPoints != energyBandsMinPoints || bands.UsedDays != energyBandsMinPoints {
		t.Fatalf("counts = n:%d used:%d, want %d distinct days", bands.NDataPoints, bands.UsedDays, energyBandsMinPoints)
	}
	if bands.Rest != 5 || bands.Recovery != 14 || bands.PushHard != 23 {
		t.Fatalf("bands = %+v, want p20/p50/p80 from day-level banks 0..29", bands)
	}
}

func TestComputeUserVerdictBands_ExcludesFlaggedRowsBeforeSelectingLatest(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	insertEnergySnapshot(t, db, bandTestDate(0), 8, 0, 2, nil)
	insertEnergySnapshot(t, db, bandTestDate(0), 20, 100, 2, []string{"imputed_sleep"})
	for i := 1; i < energyBandsMinPoints; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i, 2, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "personal_latest_formula" {
		t.Fatalf("CalibrationMode = %q, want personal_latest_formula", bands.CalibrationMode)
	}
	if bands.NDataPoints != energyBandsMinPoints {
		t.Fatalf("NDataPoints = %d, want flagged latest row excluded before per-date selection", bands.NDataPoints)
	}
	if bands.Rest != 5 {
		t.Fatalf("Rest = %d, want day-level p20 to include earlier eligible bank=0 row", bands.Rest)
	}
}

func TestComputeUserVerdictBands_UsesLatestFormulaWhenMature(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < energyBandsMinPoints; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 30; i < 70; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i-30, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "personal_latest_formula" {
		t.Fatalf("CalibrationMode = %q, want personal_latest_formula", bands.CalibrationMode)
	}
	if bands.UsedDays != 30 || bands.LatestFormulaDays != 30 || bands.CompatibleFormulaDays != 70 {
		t.Fatalf("counts = used:%d latest:%d compatible:%d, want 30/30/70", bands.UsedDays, bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
	if bands.Rest < 70 {
		t.Fatalf("Rest = %d, latest-formula bands should ignore lower compatible v1 history", bands.Rest)
	}
}

func TestComputeUserVerdictBands_UsesCompatibleFormulaWarmup(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < 35; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i-10, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "personal_mixed_formula_warmup" {
		t.Fatalf("CalibrationMode = %q, want personal_mixed_formula_warmup", bands.CalibrationMode)
	}
	if bands.UsedDays != 35 || bands.NDataPoints != 35 || bands.LatestFormulaDays != 10 || bands.CompatibleFormulaDays != 35 {
		t.Fatalf("counts = n:%d used:%d latest:%d compatible:%d, want 35/35/10/35",
			bands.NDataPoints, bands.UsedDays, bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_CountsOneDateAcrossCompatibleFormulas(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < 15; i++ {
		date := bandTestDate(i)
		insertEnergySnapshot(t, db, date, 8, i, 1, nil)
		insertEnergySnapshot(t, db, date, 20, 70+i, 2, nil)
	}
	for i := 15; i < 25; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "provisional_compatible_formula_warmup" {
		t.Fatalf("CalibrationMode = %q, want provisional_compatible_formula_warmup because compatible sample has 25 dates", bands.CalibrationMode)
	}
	if bands.NDataPoints != 25 || bands.UsedDays != 25 {
		t.Fatalf("provisional counts = n:%d used:%d, want 25/25", bands.NDataPoints, bands.UsedDays)
	}
	if bands.LatestFormulaDays != 15 || bands.CompatibleFormulaDays != 25 {
		t.Fatalf("diagnostic counts = latest:%d compatible:%d, want 15/25", bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_ProvisionalDoesNotActivateAt19CompatibleDays(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < energyBandsProvisionalMinPoints-1; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want default_warmup below provisional threshold", bands.CalibrationMode)
	}
	if bands.NDataPoints != 0 || bands.UsedDays != 0 {
		t.Fatalf("default warmup counts = n:%d used:%d, want 0/0", bands.NDataPoints, bands.UsedDays)
	}
	if bands.CompatibleFormulaDays != energyBandsProvisionalMinPoints-1 {
		t.Fatalf("CompatibleFormulaDays = %d, want %d", bands.CompatibleFormulaDays, energyBandsProvisionalMinPoints-1)
	}
}

func TestComputeUserVerdictBands_UsesProvisionalCompatibleWarmupAt20Days(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < energyBandsProvisionalMinPoints; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 40+i, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "provisional_compatible_formula_warmup" {
		t.Fatalf("CalibrationMode = %q, want provisional_compatible_formula_warmup", bands.CalibrationMode)
	}
	if bands.UsedDays != energyBandsProvisionalMinPoints || bands.NDataPoints != energyBandsProvisionalMinPoints ||
		bands.LatestFormulaDays != 10 || bands.CompatibleFormulaDays != energyBandsProvisionalMinPoints {
		t.Fatalf("counts = n:%d used:%d latest:%d compatible:%d, want 20/20/10/20",
			bands.NDataPoints, bands.UsedDays, bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_ProvisionalClampsAllBoundariesToDefaults(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < energyBandsProvisionalMinPoints; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i, 2, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	defaults := health.DefaultV2VerdictBands()
	if bands.CalibrationMode != "provisional_compatible_formula_warmup" {
		t.Fatalf("CalibrationMode = %q, want provisional_compatible_formula_warmup", bands.CalibrationMode)
	}
	if bands.Rest != defaults.Rest || bands.Recovery != defaults.Recovery || bands.PushHard != defaults.PushHard {
		t.Fatalf("bands = %+v, want component-wise clamp to defaults %+v", bands, defaults)
	}
}

func TestComputeUserVerdictBands_ProvisionalFallsBackWhenClampBreaksMonotonicity(t *testing.T) {
	sample := verdictBandSample{
		p20: floatPtr(80),
		p50: floatPtr(80),
		p80: floatPtr(80),
		n:   energyBandsProvisionalMinPoints,
	}
	if bands, ok := provisionalVerdictBandsFromSample(sample, 10, energyBandsProvisionalMinPoints); ok {
		t.Fatalf("provisional ok=true with non-strict recovery/push monotonicity, bands=%+v", bands)
	}
}

func TestComputeUserVerdictBands_MatureCompatibleStillWinsAt30Days(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < energyBandsMinPoints; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 40+i, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "personal_mixed_formula_warmup" {
		t.Fatalf("CalibrationMode = %q, want mature compatible warmup at 30 compatible days", bands.CalibrationMode)
	}
}

func TestComputeUserVerdictBands_ProvisionalUsesConfiguredWindowOnly(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < energyBandsProvisionalMinPoints-1; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	oldDate := time.Now().AddDate(0, 0, -(energyBandsWindowDays + 5)).Format("2006-01-02")
	insertEnergySnapshot(t, db, oldDate, 20, 100, 2, nil)

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want old row outside 180d window ignored", bands.CalibrationMode)
	}
	if bands.CompatibleFormulaDays != energyBandsProvisionalMinPoints-1 {
		t.Fatalf("CompatibleFormulaDays = %d, want old row outside window ignored", bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_UnknownFormulaExcludedFromWarmup(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < 35; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i-10, 9, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want default_warmup", bands.CalibrationMode)
	}
	if bands.CompatibleFormulaDays != 10 {
		t.Fatalf("CompatibleFormulaDays = %d, want only current formula dates", bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_UnknownFormulaExcludedFromProvisionalWarmup(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < energyBandsProvisionalMinPoints; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 40+i, 9, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want default_warmup", bands.CalibrationMode)
	}
	if bands.CompatibleFormulaDays != 10 {
		t.Fatalf("CompatibleFormulaDays = %d, want unknown formula excluded", bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_StressDrainDisablesV1Compatibility(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()
	if err := db.SaveSettings(map[string]string{"energy.stress_drain_enabled": "true"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < 35; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i-10, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want default_warmup when stress drain makes v1 incompatible", bands.CalibrationMode)
	}
	if bands.LatestFormulaDays != 10 || bands.CompatibleFormulaDays != 10 {
		t.Fatalf("counts = latest:%d compatible:%d, want 10/10", bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_StressDrainIgnoresPreToggleV2Rows(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	beforeToggle := time.Now().Add(-2 * time.Hour)
	for i := 0; i < energyBandsMinPoints; i++ {
		insertEnergySnapshotAt(t, db, bandTestDate(i), 20, 70+i, 2, nil, beforeToggle)
	}
	if err := db.SaveSettings(map[string]string{"energy.stress_drain_enabled": "true"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want default_warmup until post-toggle v2 dates accumulate", bands.CalibrationMode)
	}
	if bands.LatestFormulaDays != 0 || bands.CompatibleFormulaDays != 0 {
		t.Fatalf("counts = latest:%d compatible:%d, want 0/0 after excluding pre-toggle rows", bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_AlphaChangeDisablesV1Compatibility(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()
	if err := db.SaveSettings(map[string]string{"energy.alpha_factor": "1.2"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	for i := 0; i < 10; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, 70+i, 2, nil)
	}
	for i := 10; i < 35; i++ {
		insertEnergySnapshot(t, db, bandTestDate(i), 20, i-10, 1, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.CalibrationMode != "default_warmup" {
		t.Fatalf("CalibrationMode = %q, want default_warmup when effective alpha changes", bands.CalibrationMode)
	}
	if bands.LatestFormulaDays != 10 || bands.CompatibleFormulaDays != 10 {
		t.Fatalf("counts = latest:%d compatible:%d, want 10/10", bands.LatestFormulaDays, bands.CompatibleFormulaDays)
	}
}

func TestComputeUserVerdictBands_ClampsDisplayBankBeforePercentiles(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	db.EnsureEnergySnapshotsTable()

	for i := 0; i < energyBandsMinPoints; i++ {
		bank := i - 20
		insertEnergySnapshot(t, db, bandTestDate(i), 20, bank, 2, nil)
	}

	bands, err := db.ComputeUserVerdictBands(context.Background())
	if err != nil {
		t.Fatalf("ComputeUserVerdictBands: %v", err)
	}
	if bands.Rest != 0 {
		t.Fatalf("Rest = %d, want negative raw bank values clamped to display scale before percentile", bands.Rest)
	}
}

func bandTestDate(daysAgo int) string {
	return time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")
}

func insertEnergySnapshot(t *testing.T, db *DB, date string, hour, bank, formulaVersion int, flags []string) {
	t.Helper()
	insertEnergySnapshotAt(t, db, date, hour, bank, formulaVersion, flags, time.Now())
}

func insertEnergySnapshotAt(t *testing.T, db *DB, date string, hour, bank, formulaVersion int, flags []string, computedAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if flags == nil {
		flags = []string{}
	}

	ts, err := time.ParseInLocation("2006-01-02 15", fmt.Sprintf("%s %02d", date, hour), time.UTC)
	if err != nil {
		t.Fatalf("parse test ts: %v", err)
	}
	_, err = db.pool.Exec(ctx, `
		INSERT INTO energy_snapshots
			(ts_bucket, date, bank, drain_delta, restore_delta, formula_version, components, flags, computed_at)
		VALUES ($1, $2, $3, 0, 0, $4, '{}'::jsonb, $5, $6)`,
		ts, date, bank, formulaVersion, flags, computedAt)
	if err != nil {
		t.Fatalf("insert energy snapshot date=%s hour=%d bank=%d version=%d flags=%v: %v",
			date, hour, bank, formulaVersion, flags, err)
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
