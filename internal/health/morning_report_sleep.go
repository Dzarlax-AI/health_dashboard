package health

import (
	"math"
	"time"
)

const (
	MorningSleepChoiceRecordedDuration     = "recorded_duration"
	MorningSleepChoiceAwaitingCompletion   = "awaiting_completion"
	MorningSleepChoiceAwaitingFinalization = "awaiting_finalization"
)

// MorningReportSleep is the dated canonical night used by the morning report.
// Missing or incomplete capture is explicit and never replaced with an average.
type MorningReportSleep struct {
	ReportDate     string   `json:"report_date"`
	Date           string   `json:"date"`
	Hours          *float64 `json:"hours"`
	BaselineHours  *float64 `json:"baseline_hours,omitempty"`
	BaselineNights int      `json:"baseline_nights,omitempty"`
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
	if !latest.IsDefinitiveClaimEligible() {
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

// PreliminaryMorningSleepExplanationEligible permits only a plausible,
// same-day duration whose capture or finalization is still preliminary.
func PreliminaryMorningSleepExplanationEligible(sleep *MorningReportSleep, reportDate string) bool {
	if sleep == nil || sleep.Date == "" || sleep.Date != reportDate || sleep.Assessment != NightDurationPlausible || sleep.Hours == nil || math.IsNaN(*sleep.Hours) || math.IsInf(*sleep.Hours, 0) || *sleep.Hours <= 0 || *sleep.Hours > 24 {
		return false
	}
	return (sleep.Capture == NightCapturePartial && (sleep.Finalization == NightFinalProvisional || sleep.Finalization == NightFinalFinal)) ||
		(sleep.Capture == NightCaptureComplete && sleep.Finalization == NightFinalProvisional)
}

// MorningSleepExplanation localizes a server-approved preliminary sleep
// interpretation. The model supplies only one of these stable choices.
func MorningSleepExplanation(choice, lang string) (string, bool) {
	copy := map[string]map[string]string{
		"en": {
			MorningSleepChoiceRecordedDuration:     "This is a recorded duration; duration alone cannot show sleep quality.",
			MorningSleepChoiceAwaitingCompletion:   "Because the record is incomplete, its duration alone cannot establish a sleep shortfall.",
			MorningSleepChoiceAwaitingFinalization: "Until the record is confirmed, its duration alone cannot establish a sleep shortfall.",
		},
		"ru": {
			MorningSleepChoiceRecordedDuration:     "Это зафиксированная длительность; по ней одной нельзя оценить качество сна.",
			MorningSleepChoiceAwaitingCompletion:   "Из-за неполной записи по одной длительности нельзя уверенно судить о недосыпе.",
			MorningSleepChoiceAwaitingFinalization: "Пока запись не подтверждена, по одной длительности нельзя уверенно судить о недосыпе.",
		},
		"sr": {
			MorningSleepChoiceRecordedDuration:     "Ovo je zabeleženo trajanje; samo trajanje ne pokazuje kvalitet sna.",
			MorningSleepChoiceAwaitingCompletion:   "Pošto je zapis nepotpun, samo trajanje ne može pouzdano da pokaže da li je sna bilo premalo.",
			MorningSleepChoiceAwaitingFinalization: "Dok zapis ne bude potvrđen, samo trajanje ne može pouzdano da pokaže da li je sna bilo premalo.",
		},
	}
	values, ok := copy[lang]
	if !ok {
		values, ok = copy["en"]
	}
	value, ok := values[choice]
	return value, ok
}
