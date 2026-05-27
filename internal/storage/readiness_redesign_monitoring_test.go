package storage

import (
	"context"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestLoadReadinessMonitoringSummary_CoverageAndDrift(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx := context.Background()
	asOf := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	v := 0.5

	// Recovery rolling_3d recent coverage: 4 eligible, 6 ineligible,
	// and 4 missing in the mature 14-day window ending at asOf-3.
	// This should warn against the 70% floor and surface
	// sleep_data_missing as the dominant reason.
	recoveryWindowEnd := asOf.AddDate(0, 0, -3)
	for i := 0; i < 10; i++ {
		date := recoveryWindowEnd.AddDate(0, 0, -i).Format(isoDate)
		eligible := i < 4
		reason := EligibilitySleepDataMissing
		if eligible {
			reason = EligibilityOK
		}
		if err := db.SaveTargetSnapshot(TargetSnapshot{
			Date:              date,
			SubScore:          SubScoreRecoveryStability,
			TargetKind:        TargetKindRolling3d,
			TargetValue:       &v,
			Eligible:          eligible,
			EligibilityReason: reason,
			SourceEpoch:       InitialSourceEpoch,
			FormulaVersion:    1,
		}); err != nil {
			t.Fatalf("seed recovery target %s: %v", date, err)
		}
	}

	// Acute OR-event drift: 90-day baseline has enough positives, and
	// the last 30 days are materially hotter than the baseline.
	for i := 0; i < 90; i++ {
		date := asOf.AddDate(0, 0, -i).Format(isoDate)
		label := 0.0
		if i < 15 || (i >= 30 && i < 35) {
			label = 1
		}
		tv := label
		if err := db.SaveTargetSnapshot(TargetSnapshot{
			Date:              date,
			SubScore:          SubScoreAcuteRisk,
			TargetKind:        TargetKindEventT1T3,
			TargetValue:       &tv,
			Eligible:          true,
			EligibilityReason: EligibilityOK,
			SourceEpoch:       InitialSourceEpoch,
			FormulaVersion:    1,
		}); err != nil {
			t.Fatalf("seed acute target %s: %v", date, err)
		}
		if err := db.SaveNaiveBaseline(NaiveBaseline{
			Date:           date,
			SubScore:       SubScoreAcuteRisk,
			TargetKind:     TargetKindEventT1T3,
			BaselineKind:   BaselineKindEventBaseRate,
			PredictedValue: &v,
			SourceEpoch:    InitialSourceEpoch,
			FormulaVersion: 1,
		}); err != nil {
			t.Fatalf("seed acute baseline %s: %v", date, err)
		}
	}

	summary, err := db.LoadReadinessMonitoringSummary("2026-05-26")
	if err != nil {
		t.Fatalf("LoadReadinessMonitoringSummary: %v", err)
	}
	if summary.OverallStatus != MonitoringStatusWarn {
		t.Fatalf("OverallStatus = %q, want warn", summary.OverallStatus)
	}

	var recovery *ReadinessCoverageRow
	for i := range summary.CoverageRows {
		r := &summary.CoverageRows[i]
		if r.SubScore == SubScoreRecoveryStability && r.TargetKind == TargetKindRolling3d {
			recovery = r
			break
		}
	}
	if recovery == nil {
		t.Fatalf("missing recovery rolling_3d coverage row")
	}
	if recovery.Status != MonitoringStatusWarn {
		t.Errorf("recovery status = %q, want warn", recovery.Status)
	}
	if recovery.Eligible != 4 || recovery.Rows != 10 {
		t.Errorf("recovery counts = %d/%d, want 4/10", recovery.Eligible, recovery.Rows)
	}
	if recovery.ExpectedRows != ReadinessMonitoringWindowDays || recovery.MissingRows != 4 {
		t.Errorf("recovery expected/missing rows = %d/%d, want %d/4",
			recovery.ExpectedRows, recovery.MissingRows, ReadinessMonitoringWindowDays)
	}
	if recovery.TopReason != EligibilitySleepDataMissing {
		t.Errorf("recovery top reason = %q, want %q", recovery.TopReason, EligibilitySleepDataMissing)
	}
	if recovery.WindowTo != "2026-05-23" || recovery.InputStableTo != "2026-05-23" {
		t.Errorf("recovery window/stable = %s/%s, want 2026-05-23/2026-05-23",
			recovery.WindowTo, recovery.InputStableTo)
	}
	if len(recovery.IssueSamples) == 0 {
		t.Fatalf("recovery issue samples empty; want concrete warning dates")
	}
	if got := recovery.IssueSamples[0]; got.Reason != EligibilitySleepDataMissing || got.Date != "2026-05-14" {
		t.Fatalf("first recovery issue = %+v, want 2026-05-14/%s", got, EligibilitySleepDataMissing)
	}

	var acute *ReadinessDriftRow
	for i := range summary.DriftRows {
		r := &summary.DriftRows[i]
		if r.SubScore == SubScoreAcuteRisk && r.TargetKind == TargetKindEventT1T3 {
			acute = r
			break
		}
	}
	if acute == nil {
		t.Fatalf("missing acute drift row")
	}
	if acute.Status != MonitoringStatusWarn {
		t.Errorf("acute drift status = %q, want warn (row=%+v)", acute.Status, *acute)
	}
	if acute.RecentPositives != 15 || acute.RecentEligible != 30 {
		t.Errorf("acute recent = %d/%d, want 15/30", acute.RecentPositives, acute.RecentEligible)
	}

	// A confirmed ended source epoch without a successor is critical.
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO source_epochs
			(epoch_id, start_date, end_date, kind, description, detected_by, confirmed)
		VALUES ('ended_without_successor', '2026-01-01', '2026-05-01', $1, '', $2, TRUE)
	`, SourceEpochKindPhysiology, DetectedByManual); err != nil {
		t.Fatalf("seed source epoch gap: %v", err)
	}
	summary, err = db.LoadReadinessMonitoringSummary("2026-05-26")
	if err != nil {
		t.Fatalf("LoadReadinessMonitoringSummary after epoch seed: %v", err)
	}
	if summary.OverallStatus != MonitoringStatusCritical {
		t.Fatalf("OverallStatus = %q, want critical", summary.OverallStatus)
	}
	if len(summary.SourceEpochAlerts) != 1 {
		t.Fatalf("SourceEpochAlerts len = %d, want 1", len(summary.SourceEpochAlerts))
	}
}

func TestLoadReadinessMonitoringSummary_SparseCoverageCannotBeOK(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	v := 0.5
	if err := db.SaveTargetSnapshot(TargetSnapshot{
		Date:              "2026-05-26",
		SubScore:          SubScoreRecoveryStability,
		TargetKind:        TargetKindDailyPoint,
		TargetValue:       &v,
		Eligible:          true,
		EligibilityReason: EligibilityOK,
		SourceEpoch:       InitialSourceEpoch,
		FormulaVersion:    1,
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	summary, err := db.LoadReadinessMonitoringSummary("2026-05-26")
	if err != nil {
		t.Fatalf("LoadReadinessMonitoringSummary: %v", err)
	}
	for _, row := range summary.CoverageRows {
		if row.SubScore == SubScoreRecoveryStability && row.TargetKind == TargetKindDailyPoint {
			if row.Status == MonitoringStatusOK {
				t.Fatalf("sparse coverage status = ok for %+v; want non-ok", row)
			}
			if row.Rows != 1 || row.ExpectedRows != ReadinessMonitoringWindowDays || row.MissingRows != ReadinessMonitoringWindowDays-1 {
				t.Fatalf("row counts = rows:%d expected:%d missing:%d, want 1/%d/%d",
					row.Rows, row.ExpectedRows, row.MissingRows,
					ReadinessMonitoringWindowDays, ReadinessMonitoringWindowDays-1)
			}
			if row.ReasonCounts["missing_rows"] != ReadinessMonitoringWindowDays-1 {
				t.Fatalf("missing_rows reason = %d, want %d",
					row.ReasonCounts["missing_rows"], ReadinessMonitoringWindowDays-1)
			}
			return
		}
	}
	t.Fatalf("missing recovery daily_point coverage row")
}

func TestLoadReadinessMonitoringSummary_CoverageUsesMatureWindows(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	v := 0.0
	asOf := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	// Fresh-edge chronic rows are expected to be ineligible because
	// their t+1..t+14 label window has not fully matured yet. They
	// should not drive the monitoring coverage warning.
	for i := 0; i < ReadinessMonitoringWindowDays; i++ {
		date := asOf.AddDate(0, 0, -i).Format(isoDate)
		if err := db.SaveTargetSnapshot(TargetSnapshot{
			Date:              date,
			SubScore:          SubScoreChronicLoad,
			TargetKind:        TargetKindChronicAcuteDensity,
			Eligible:          false,
			EligibilityReason: EligibilityEventWindowDataMissing,
			SourceEpoch:       InitialSourceEpoch,
			FormulaVersion:    1,
		}); err != nil {
			t.Fatalf("seed fresh chronic target %s: %v", date, err)
		}
	}

	// The mature window ending at asOf-14d is complete and eligible.
	for i := 0; i < ReadinessMonitoringWindowDays; i++ {
		date := asOf.AddDate(0, 0, -health.ChronicLoadForwardWindowDays-i).Format(isoDate)
		if err := db.SaveTargetSnapshot(TargetSnapshot{
			Date:              date,
			SubScore:          SubScoreAcuteRisk,
			TargetKind:        TargetKindEventT1T3,
			TargetValue:       &v,
			Eligible:          true,
			EligibilityReason: EligibilityOK,
			SourceEpoch:       InitialSourceEpoch,
			FormulaVersion:    1,
		}); err != nil {
			t.Fatalf("seed mature acute input %s: %v", date, err)
		}
		if err := db.SaveTargetSnapshot(TargetSnapshot{
			Date:              date,
			SubScore:          SubScoreChronicLoad,
			TargetKind:        TargetKindChronicAcuteDensity,
			TargetValue:       &v,
			Eligible:          true,
			EligibilityReason: EligibilityOK,
			SourceEpoch:       InitialSourceEpoch,
			FormulaVersion:    1,
		}); err != nil {
			t.Fatalf("seed mature chronic target %s: %v", date, err)
		}
	}

	summary, err := db.LoadReadinessMonitoringSummary("2026-05-26")
	if err != nil {
		t.Fatalf("LoadReadinessMonitoringSummary: %v", err)
	}
	for _, row := range summary.CoverageRows {
		if row.SubScore == SubScoreChronicLoad && row.TargetKind == TargetKindChronicAcuteDensity {
			if row.Status != MonitoringStatusOK {
				t.Fatalf("chronic coverage status = %q for %+v; want ok", row.Status, row)
			}
			if row.Rows != ReadinessMonitoringWindowDays || row.Eligible != ReadinessMonitoringWindowDays {
				t.Fatalf("chronic mature counts = %d/%d, want %d/%d",
					row.Eligible, row.Rows,
					ReadinessMonitoringWindowDays, ReadinessMonitoringWindowDays)
			}
			if row.WindowTo != "2026-05-12" || row.InputStableTo != "2026-05-12" {
				t.Fatalf("chronic window/stable = %s/%s, want 2026-05-12/2026-05-12",
					row.WindowTo, row.InputStableTo)
			}
			return
		}
	}
	t.Fatalf("missing chronic acute-density coverage row")
}

func TestLoadReadinessMonitoringSummary_RollingTargetsUseThreeDayLag(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	v := 0.5
	asOf := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	matureEnd := asOf.AddDate(0, 0, -3)

	for _, subScore := range []string{SubScoreRecoveryStability, SubScorePassiveEfficiency} {
		// Fresh-edge rows are not yet contract-mature for rolling_3d and
		// must not affect the monitoring window.
		for i := 0; i < 3; i++ {
			date := asOf.AddDate(0, 0, -i).Format(isoDate)
			if err := db.SaveTargetSnapshot(TargetSnapshot{
				Date:              date,
				SubScore:          subScore,
				TargetKind:        TargetKindRolling3d,
				Eligible:          false,
				EligibilityReason: EligibilityEventWindowDataMissing,
				SourceEpoch:       InitialSourceEpoch,
				FormulaVersion:    1,
			}); err != nil {
				t.Fatalf("seed fresh rolling target %s/%s: %v", subScore, date, err)
			}
		}

		for i := 0; i < ReadinessMonitoringWindowDays; i++ {
			date := matureEnd.AddDate(0, 0, -i).Format(isoDate)
			if err := db.SaveTargetSnapshot(TargetSnapshot{
				Date:              date,
				SubScore:          subScore,
				TargetKind:        TargetKindRolling3d,
				TargetValue:       &v,
				Eligible:          true,
				EligibilityReason: EligibilityOK,
				SourceEpoch:       InitialSourceEpoch,
				FormulaVersion:    1,
			}); err != nil {
				t.Fatalf("seed mature rolling target %s/%s: %v", subScore, date, err)
			}
		}
	}

	summary, err := db.LoadReadinessMonitoringSummary("2026-05-26")
	if err != nil {
		t.Fatalf("LoadReadinessMonitoringSummary: %v", err)
	}

	for _, subScore := range []string{SubScoreRecoveryStability, SubScorePassiveEfficiency} {
		var row *ReadinessCoverageRow
		for i := range summary.CoverageRows {
			candidate := &summary.CoverageRows[i]
			if candidate.SubScore == subScore && candidate.TargetKind == TargetKindRolling3d {
				row = candidate
				break
			}
		}
		if row == nil {
			t.Fatalf("missing %s rolling_3d coverage row", subScore)
		}
		if row.ContractLagDays != 3 {
			t.Fatalf("%s contract lag = %d, want 3", subScore, row.ContractLagDays)
		}
		if row.WindowTo != "2026-05-23" || row.InputStableTo != "2026-05-23" {
			t.Fatalf("%s window/stable = %s/%s, want 2026-05-23/2026-05-23",
				subScore, row.WindowTo, row.InputStableTo)
		}
		if row.Status != MonitoringStatusOK {
			t.Fatalf("%s rolling status = %q for %+v, want ok", subScore, row.Status, *row)
		}
		if row.Rows != ReadinessMonitoringWindowDays || row.Eligible != ReadinessMonitoringWindowDays || row.MissingRows != 0 {
			t.Fatalf("%s rolling counts = eligible:%d rows:%d missing:%d, want %d/%d/0",
				subScore, row.Eligible, row.Rows, row.MissingRows,
				ReadinessMonitoringWindowDays, ReadinessMonitoringWindowDays)
		}
		if len(row.IssueSamples) != 0 {
			t.Fatalf("%s rolling issue samples = %+v, want none", subScore, row.IssueSamples)
		}
	}
}

func TestLoadReadinessMonitoringSummary_StaleInputWarnsSeparately(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	v := 0.5
	asOf := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	stableEnd := asOf.AddDate(0, 0, -5)
	for i := 0; i < ReadinessMonitoringWindowDays; i++ {
		date := stableEnd.AddDate(0, 0, -i).Format(isoDate)
		if err := db.SaveTargetSnapshot(TargetSnapshot{
			Date:              date,
			SubScore:          SubScoreRecoveryStability,
			TargetKind:        TargetKindDailyPoint,
			TargetValue:       &v,
			Eligible:          true,
			EligibilityReason: EligibilityOK,
			SourceEpoch:       InitialSourceEpoch,
			FormulaVersion:    1,
		}); err != nil {
			t.Fatalf("seed recovery target %s: %v", date, err)
		}
	}

	summary, err := db.LoadReadinessMonitoringSummary("2026-05-26")
	if err != nil {
		t.Fatalf("LoadReadinessMonitoringSummary: %v", err)
	}
	for _, row := range summary.CoverageRows {
		if row.SubScore == SubScoreRecoveryStability && row.TargetKind == TargetKindDailyPoint {
			if row.InputStalenessStatus != MonitoringStatusWarn {
				t.Fatalf("input staleness status = %q for %+v, want warn", row.InputStalenessStatus, row)
			}
			if row.Status != MonitoringStatusWarn {
				t.Fatalf("effective status = %q for %+v, want warn", row.Status, row)
			}
			if row.InputStalenessDays != 5 || row.InputStableTo != "2026-05-21" {
				t.Fatalf("staleness = %dd stable=%s, want 5d stable=2026-05-21",
					row.InputStalenessDays, row.InputStableTo)
			}
			if row.Rows != ReadinessMonitoringWindowDays || row.Eligible != ReadinessMonitoringWindowDays {
				t.Fatalf("coverage counts = %d/%d, want %d/%d",
					row.Eligible, row.Rows,
					ReadinessMonitoringWindowDays, ReadinessMonitoringWindowDays)
			}
			return
		}
	}
	t.Fatalf("missing recovery daily_point coverage row")
}
