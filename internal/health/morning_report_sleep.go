package health

import "time"

// MorningReportSleep is the dated canonical night used by the morning report.
// Missing or incomplete capture is explicit and never replaced with an average.
type MorningReportSleep struct {
	ReportDate     string   `json:"report_date"`
	Date           string   `json:"date"`
	Hours          *float64 `json:"hours"`
	BaselineHours  *float64 `json:"baseline_hours"`
	BaselineNights int      `json:"baseline_nights"`
	Capture        string   `json:"capture"`
	Assessment     string   `json:"assessment"`
	Finalization   string   `json:"finalization"`
	InputHash      string   `json:"input_hash"`
}

// SelectMorningReportSleep selects the latest observed canonical night without
// hiding an incomplete current night behind an older complete one.
func SelectMorningReportSleep(reportDate string, nights []CompletedNightSleep) MorningReportSleep {
	out := MorningReportSleep{ReportDate: reportDate, Capture: NightCaptureUnknown, Assessment: NightDurationUnknown}
	var latest *CompletedNightSleep
	for i := range nights {
		n := &nights[i]
		if n.WakeDate <= reportDate && n.WakeDate != "" && (latest == nil || n.WakeDate > latest.WakeDate) {
			latest = n
		}
	}
	if latest == nil {
		return out
	}
	out.Date, out.Capture, out.Assessment, out.Finalization, out.InputHash = latest.WakeDate, latest.CaptureCompleteness, latest.DurationAssessment, latest.FinalizationState, latest.InputHash
	if latest.DurationHours > 0 {
		hours := latest.DurationHours
		out.Hours = &hours
	}
	date, err := time.Parse("2006-01-02", latest.WakeDate)
	if err != nil {
		return out
	}
	from := date.AddDate(0, 0, -30).Format("2006-01-02")
	total := 0.0
	for _, n := range nights {
		if n.WakeDate < from || n.WakeDate >= latest.WakeDate || !n.IsDefinitiveClaimEligible() || n.SourceEpoch != latest.SourceEpoch || n.AlgorithmVersion != latest.AlgorithmVersion || n.Source != latest.Source {
			continue
		}
		total += n.DurationHours
		out.BaselineNights++
	}
	if out.BaselineNights >= 7 {
		average := total / float64(out.BaselineNights)
		out.BaselineHours = &average
	}
	return out
}
