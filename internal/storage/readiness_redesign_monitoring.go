package storage

import (
	"fmt"
	"math"
	"sort"
	"time"

	"health-receiver/internal/health"
)

const (
	MonitoringStatusOK           = "ok"
	MonitoringStatusWarn         = "warn"
	MonitoringStatusCritical     = "critical"
	MonitoringStatusInsufficient = "insufficient_data"

	ReadinessMonitoringWindowDays = 14
	ReadinessMonitoringDriftDays  = 30
	ReadinessMonitoringBaseDays   = 90
)

type ReadinessMonitoringSummary struct {
	AsOfDate          string
	WindowDays        int
	DriftDays         int
	BaselineDays      int
	OverallStatus     string
	CoverageRows      []ReadinessCoverageRow
	DriftRows         []ReadinessDriftRow
	UnknownRateRows   []ReadinessUnknownRateRow
	SourceEpochAlerts []ReadinessSourceEpochAlert
}

type ReadinessCoverageRow struct {
	SubScore      string
	TargetKind    string
	ExpectedRows  int
	Rows          int
	MissingRows   int
	Eligible      int
	EligiblePct   float64
	FloorPct      float64
	Status        string
	ReasonCounts  map[string]int
	TopReason     string
	TopReasonRows int
}

type ReadinessDriftRow struct {
	SubScore          string
	TargetKind        string
	RecentEligible    int
	RecentPositives   int
	RecentRate        float64
	BaselineEligible  int
	BaselinePositives int
	BaselineRate      float64
	Delta             float64
	Threshold         float64
	Status            string
}

type ReadinessUnknownRateRow struct {
	SubScore        string
	RecentRows      int
	RecentUnknown   int
	RecentRate      float64
	BaselineRows    int
	BaselineUnknown int
	BaselineRate    float64
	Status          string
}

type ReadinessSourceEpochAlert struct {
	EpochID   string
	Kind      string
	StartDate string
	EndDate   string
	Status    string
	Message   string
}

type monitoringTarget struct {
	SubScore        string
	TargetKind      string
	FloorPct        float64
	CoverageLagDays int
}

var readinessMonitoringTargets = []monitoringTarget{
	{SubScore: SubScoreRecoveryStability, TargetKind: TargetKindDailyPoint, FloorPct: 0.70},
	{SubScore: SubScoreRecoveryStability, TargetKind: TargetKindRolling3d, FloorPct: 0.70},
	{SubScore: SubScorePassiveEfficiency, TargetKind: TargetKindDailyPoint, FloorPct: 0.60},
	{SubScore: SubScorePassiveEfficiency, TargetKind: TargetKindRolling3d, FloorPct: 0.60},
	{SubScore: SubScoreAcuteRisk, TargetKind: TargetKindEventT1T3, FloorPct: 0.70, CoverageLagDays: 3},
	{SubScore: SubScoreAcuteRisk, TargetKind: TargetKindEventStrictT1T3, FloorPct: 0.70, CoverageLagDays: 3},
	{SubScore: SubScoreChronicLoad, TargetKind: TargetKindChronicLabel, FloorPct: 0.10, CoverageLagDays: health.ChronicLoadForwardWindowDays},
	{SubScore: SubScoreChronicLoad, TargetKind: TargetKindChronicAcuteDensity, FloorPct: 0.10, CoverageLagDays: health.ChronicLoadForwardWindowDays},
}

var readinessMonitoringClassifierTargets = []struct {
	SubScore   string
	TargetKind string
}{
	{SubScoreAcuteRisk, TargetKindEventT1T3},
	{SubScoreAcuteRisk, TargetKindEventStrictT1T3},
	{SubScoreChronicLoad, TargetKindChronicLabel},
	{SubScoreChronicLoad, TargetKindChronicAcuteDensity},
}

func (s *DB) LoadReadinessMonitoringSummary(asOfDate string) (*ReadinessMonitoringSummary, error) {
	asOf, err := time.Parse(isoDate, asOfDate)
	if err != nil {
		return nil, fmt.Errorf("LoadReadinessMonitoringSummary: parse asOfDate %q: %w", asOfDate, err)
	}
	out := &ReadinessMonitoringSummary{
		AsOfDate:      asOfDate,
		WindowDays:    ReadinessMonitoringWindowDays,
		DriftDays:     ReadinessMonitoringDriftDays,
		BaselineDays:  ReadinessMonitoringBaseDays,
		OverallStatus: MonitoringStatusOK,
	}
	coverage, err := s.loadReadinessCoverageRows(asOf)
	if err != nil {
		return nil, err
	}
	out.CoverageRows = coverage

	drift, err := s.loadReadinessDriftRows(asOf)
	if err != nil {
		return nil, err
	}
	out.DriftRows = drift

	unknown, err := s.loadReadinessUnknownRateRows(asOf)
	if err != nil {
		return nil, err
	}
	out.UnknownRateRows = unknown

	alerts, err := s.loadReadinessSourceEpochAlerts(asOf.Format(isoDate))
	if err != nil {
		return nil, err
	}
	out.SourceEpochAlerts = alerts

	out.OverallStatus = readinessMonitoringOverallStatus(out)
	return out, nil
}

func (s *DB) loadReadinessCoverageRows(asOf time.Time) ([]ReadinessCoverageRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	out := make([]ReadinessCoverageRow, 0, len(readinessMonitoringTargets))
	for _, target := range readinessMonitoringTargets {
		row := ReadinessCoverageRow{
			SubScore:     target.SubScore,
			TargetKind:   target.TargetKind,
			ExpectedRows: ReadinessMonitoringWindowDays,
			MissingRows:  ReadinessMonitoringWindowDays,
			FloorPct:     target.FloorPct,
			Status:       MonitoringStatusInsufficient,
			ReasonCounts: map[string]int{},
		}

		from, to := monitoringCoverageWindow(asOf, target)
		rows, err := s.pool.Query(ctx, `
			SELECT eligible, eligibility_reason
			  FROM target_snapshots
			 WHERE sub_score = $1
			   AND target_kind = $2
			   AND date BETWEEN $3 AND $4
		`, target.SubScore, target.TargetKind, from, to)
		if err != nil {
			return nil, fmt.Errorf("loadReadinessCoverageRows %s/%s: %w",
				target.SubScore, target.TargetKind, err)
		}

		for rows.Next() {
			var reason string
			var eligible bool
			if err := rows.Scan(&eligible, &reason); err != nil {
				rows.Close()
				return nil, fmt.Errorf("loadReadinessCoverageRows scan %s/%s: %w",
					target.SubScore, target.TargetKind, err)
			}
			row.Rows++
			if eligible {
				row.Eligible++
			}
			if reason == "" {
				reason = "unknown"
			}
			row.ReasonCounts[reason]++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("loadReadinessCoverageRows rows %s/%s: %w",
				target.SubScore, target.TargetKind, err)
		}
		rows.Close()

		if row.Rows < row.ExpectedRows {
			row.MissingRows = row.ExpectedRows - row.Rows
			row.ReasonCounts["missing_rows"] = row.MissingRows
		} else {
			row.MissingRows = 0
		}
		if row.Rows > 0 {
			row.EligiblePct = float64(row.Eligible) / float64(row.Rows)
			row.Status = MonitoringStatusOK
			if row.MissingRows > 0 {
				row.Status = MonitoringStatusInsufficient
			}
			if row.EligiblePct < target.FloorPct {
				row.Status = MonitoringStatusWarn
			}
		}
		row.TopReason, row.TopReasonRows = topReason(row.ReasonCounts)
		out = append(out, row)
	}
	return out, nil
}

func monitoringCoverageWindow(asOf time.Time, target monitoringTarget) (from, to string) {
	windowEnd := asOf.AddDate(0, 0, -target.CoverageLagDays)
	windowStart := windowEnd.AddDate(0, 0, -(ReadinessMonitoringWindowDays - 1))
	return windowStart.Format(isoDate), windowEnd.Format(isoDate)
}

func (s *DB) loadReadinessDriftRows(asOf time.Time) ([]ReadinessDriftRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	recentFrom := asOf.AddDate(0, 0, -(ReadinessMonitoringDriftDays - 1)).Format(isoDate)
	baseFrom := asOf.AddDate(0, 0, -(ReadinessMonitoringBaseDays - 1)).Format(isoDate)
	to := asOf.Format(isoDate)

	out := make([]ReadinessDriftRow, 0, len(readinessMonitoringClassifierTargets))
	for _, target := range readinessMonitoringClassifierTargets {
		row := ReadinessDriftRow{
			SubScore:   target.SubScore,
			TargetKind: target.TargetKind,
			Status:     MonitoringStatusInsufficient,
		}
		if err := s.pool.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE date BETWEEN $3 AND $4),
				COUNT(*) FILTER (WHERE date BETWEEN $3 AND $4 AND target_value >= 0.5),
				COUNT(*),
				COUNT(*) FILTER (WHERE target_value >= 0.5)
			  FROM target_snapshots
			 WHERE sub_score = $1
			   AND target_kind = $2
			   AND eligible = TRUE
			   AND target_value IS NOT NULL
			   AND date BETWEEN $5 AND $4
		`, target.SubScore, target.TargetKind, recentFrom, to, baseFrom).
			Scan(&row.RecentEligible, &row.RecentPositives, &row.BaselineEligible, &row.BaselinePositives); err != nil {
			return nil, fmt.Errorf("loadReadinessDriftRows %s/%s: %w", target.SubScore, target.TargetKind, err)
		}
		if row.RecentEligible >= 20 && row.BaselineEligible >= 60 && row.BaselinePositives >= 5 {
			row.RecentRate = float64(row.RecentPositives) / float64(row.RecentEligible)
			row.BaselineRate = float64(row.BaselinePositives) / float64(row.BaselineEligible)
			row.Delta = row.RecentRate - row.BaselineRate
			row.Threshold = 2 * math.Sqrt(row.BaselineRate*(1-row.BaselineRate)/float64(row.RecentEligible))
			row.Status = MonitoringStatusOK
			if row.Threshold > 0 && math.Abs(row.Delta) > row.Threshold {
				row.Status = MonitoringStatusWarn
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *DB) loadReadinessUnknownRateRows(asOf time.Time) ([]ReadinessUnknownRateRow, error) {
	baselineFrom := asOf.AddDate(0, 0, -(ReadinessMonitoringBaseDays - 1)).Format(isoDate)
	recentFrom := asOf.AddDate(0, 0, -(ReadinessMonitoringWindowDays - 1)).Format(isoDate)
	to := asOf.Format(isoDate)
	rows, err := s.LoadOperationalContractRows(baselineFrom, to)
	if err != nil {
		return nil, fmt.Errorf("loadReadinessUnknownRateRows: %w", err)
	}
	recentByScore := map[string]*ReadinessUnknownRateRow{}
	for _, c := range chipConfigs {
		recentByScore[c.SubScore] = &ReadinessUnknownRateRow{
			SubScore: c.SubScore,
			Status:   MonitoringStatusInsufficient,
		}
	}
	for _, r := range rows {
		ur := recentByScore[r.SubScore]
		if ur == nil {
			continue
		}
		unknown := r.SourceEpochChanged || r.PredictedValue == nil
		ur.BaselineRows++
		if unknown {
			ur.BaselineUnknown++
		}
		if r.Date >= recentFrom {
			ur.RecentRows++
			if unknown {
				ur.RecentUnknown++
			}
		}
	}
	out := make([]ReadinessUnknownRateRow, 0, len(chipConfigs))
	for _, c := range chipConfigs {
		row := *recentByScore[c.SubScore]
		if row.RecentRows > 0 && row.BaselineRows >= ReadinessMonitoringWindowDays {
			row.RecentRate = float64(row.RecentUnknown) / float64(row.RecentRows)
			row.BaselineRate = float64(row.BaselineUnknown) / float64(row.BaselineRows)
			row.Status = MonitoringStatusOK
			if row.RecentRows >= 7 && row.RecentRate >= 0.25 && row.RecentRate > row.BaselineRate+0.20 {
				row.Status = MonitoringStatusWarn
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *DB) loadReadinessSourceEpochAlerts(asOfDate string) ([]ReadinessSourceEpochAlert, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT epoch_id, kind, start_date, end_date
		  FROM source_epochs e
		 WHERE confirmed = TRUE
		   AND end_date IS NOT NULL
		   AND end_date <= $1
		   AND NOT EXISTS (
			 SELECT 1
			   FROM source_epochs next
			  WHERE next.kind = e.kind
			    AND next.confirmed = TRUE
			    AND next.start_date > e.end_date
		   )
		 ORDER BY kind, end_date DESC
	`, asOfDate)
	if err != nil {
		return nil, fmt.Errorf("loadReadinessSourceEpochAlerts: %w", err)
	}
	defer rows.Close()
	var out []ReadinessSourceEpochAlert
	for rows.Next() {
		var a ReadinessSourceEpochAlert
		if err := rows.Scan(&a.EpochID, &a.Kind, &a.StartDate, &a.EndDate); err != nil {
			return nil, fmt.Errorf("loadReadinessSourceEpochAlerts scan: %w", err)
		}
		a.Status = MonitoringStatusCritical
		a.Message = "source epoch ended without a confirmed successor"
		out = append(out, a)
	}
	return out, rows.Err()
}

func readinessMonitoringOverallStatus(s *ReadinessMonitoringSummary) string {
	status := MonitoringStatusOK
	for _, a := range s.SourceEpochAlerts {
		if a.Status == MonitoringStatusCritical {
			return MonitoringStatusCritical
		}
	}
	for _, row := range s.CoverageRows {
		status = maxMonitoringStatus(status, row.Status)
	}
	for _, row := range s.DriftRows {
		status = maxMonitoringStatus(status, row.Status)
	}
	for _, row := range s.UnknownRateRows {
		status = maxMonitoringStatus(status, row.Status)
	}
	return status
}

func maxMonitoringStatus(a, b string) string {
	rank := func(s string) int {
		switch s {
		case MonitoringStatusCritical:
			return 3
		case MonitoringStatusWarn:
			return 2
		case MonitoringStatusInsufficient:
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

func topReason(reasons map[string]int) (string, int) {
	if len(reasons) == 0 {
		return "", 0
	}
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	top := keys[0]
	for _, k := range keys[1:] {
		if reasons[k] > reasons[top] {
			top = k
		}
	}
	return top, reasons[top]
}
