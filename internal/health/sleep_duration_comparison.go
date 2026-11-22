package health

import (
	"fmt"
	"math"
	"time"
)

const (
	SleepComparisonRuleVersion  = "sleep_duration_reference_v1"
	SleepComparisonAvailable    = "available"
	SleepComparisonNoNight      = "no_night"
	SleepComparisonIneligible   = "ineligible_night"
	SleepComparisonInsufficient = "insufficient_history"
	SleepComparisonUnavailable  = "unavailable"
)

type SleepDurationComparison struct {
	WakeDate              string     `json:"wake_date"`
	Status                string     `json:"status" jsonschema:"enum=available,enum=no_night,enum=ineligible_night,enum=insufficient_history,enum=unavailable"`
	DurationMinutes       *int       `json:"duration_minutes" jsonschema:"nullable"`
	Source                string     `json:"source,omitempty"`
	PreferredSleepSource  string     `json:"preferred_sleep_source,omitempty"`
	SourceSelectionReason string     `json:"source_selection_reason,omitempty"`
	Capture               string     `json:"capture,omitempty"`
	Assessment            string     `json:"assessment,omitempty"`
	Finalization          string     `json:"finalization,omitempty"`
	WindowStart           string     `json:"window_start"`
	WindowEndExclusive    string     `json:"window_end_exclusive"`
	EligibleNights        *int       `json:"eligible_nights" jsonschema:"nullable"`
	ReferenceMinutes      *int       `json:"reference_minutes" jsonschema:"nullable"`
	DeltaMinutes          *int       `json:"delta_minutes" jsonschema:"nullable"`
	RuleVersion           string     `json:"rule_version"`
	ObservedAt            *time.Time `json:"observed_at,omitempty"`
	FactVersion           string     `json:"fact_version,omitempty"`
}

// ParseSleepComparisonDate requires a calendar date whose 30-day window can
// also be encoded with four-digit years.
func ParseSleepComparisonDate(value string) (time.Time, error) {
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || date.Format(time.DateOnly) != value || date.AddDate(0, 0, -30).Year() < 1 {
		return time.Time{}, fmt.Errorf("date must be a valid YYYY-MM-DD wake date")
	}
	return date, nil
}

func NewSleepDurationComparison(date time.Time) SleepDurationComparison {
	wake := date.Format(time.DateOnly)
	return SleepDurationComparison{
		WakeDate: wake, Status: SleepComparisonUnavailable, RuleVersion: SleepComparisonRuleVersion,
		WindowStart: date.AddDate(0, 0, -30).Format(time.DateOnly), WindowEndExclusive: wake,
	}
}

// SetDurations applies the same rounding independently to both values before
// computing a signed difference. Invalid recorded values have no numeric output.
func (c *SleepDurationComparison) SetDurations(n CompletedNightSleep, reference *float64) {
	c.Status = SleepComparisonIneligible
	c.Source, c.Capture, c.Assessment, c.Finalization = n.Source, n.CaptureCompleteness, n.DurationAssessment, n.FinalizationState
	observedAt := n.ObservedAt.UTC()
	c.ObservedAt = &observedAt
	if n.DurationHours > 0 && n.DurationHours <= 24 {
		minutes := int(math.Round(n.DurationHours * 60))
		c.DurationMinutes = &minutes
	}
	if c.DurationMinutes == nil || !(n.IsDefinitiveClaimEligible() || n.IsProvisionalObservationEligible()) {
		return
	}
	c.Status = SleepComparisonInsufficient
	if reference != nil && *reference > 0 && *reference <= 24 {
		minutes := int(math.Round(*reference * 60))
		delta := *c.DurationMinutes - minutes
		c.ReferenceMinutes, c.DeltaMinutes = &minutes, &delta
		c.Status = SleepComparisonAvailable
	}
}

// SleepComparisonText is used by both report formats and the legacy Sleep page.
func SleepComparisonText(c SleepDurationComparison, lang string) (title, text string) {
	type labels struct {
		title, duration, normal, window, preliminary, noNight, ineligible, insufficient, unavailable, hour, minute string
	}
	copy := map[string]labels{
		"en": {"Compared with your usual sleep", "Recorded %s.", "Usual %s (%+d min).", "%d comparable nights · %s to %s, excluding the wake date.", "Preliminary record.", "No canonical night recorded for this date.", "This record does not qualify for comparison.", "At least 7 comparable nights are needed.", "Comparison temporarily unavailable.", "h", "min"},
		"ru": {"Сравнение с личной нормой", "Записано %s.", "Норма %s (%+d мин).", "Сопоставимых ночей: %d · с %s до %s, без даты пробуждения.", "Предварительная запись.", "За эту дату нет подтверждённой записи ночи.", "Эта запись пока не подходит для сравнения.", "Нужно не менее 7 сопоставимых ночей.", "Сравнение временно недоступно.", "ч", "мин"},
		"sr": {"Poređenje sa uobičajenim snom", "Zabeleženo %s.", "Uobičajeno %s (%+d min).", "Uporedivih noći: %d · od %s do %s, bez datuma buđenja.", "Preliminarni zapis.", "Nema potvrđenog zapisa za ovaj datum.", "Ovaj zapis nije pogodan za poređenje.", "Potrebno je najmanje 7 uporedivih noći.", "Poređenje je privremeno nedostupno.", "h", "min"},
	}
	l, ok := copy[lang]
	if !ok {
		l = copy["en"]
	}
	format := func(minutes int) string {
		return fmt.Sprintf("%d %s %02d %s", minutes/60, l.hour, minutes%60, l.minute)
	}
	if c.DurationMinutes != nil {
		text = fmt.Sprintf(l.duration, format(*c.DurationMinutes)) + " "
	}
	switch c.Status {
	case SleepComparisonAvailable:
		if c.ReferenceMinutes != nil && c.DeltaMinutes != nil {
			text += fmt.Sprintf(l.normal, format(*c.ReferenceMinutes), *c.DeltaMinutes)
		} else {
			text += l.unavailable
		}
	case SleepComparisonNoNight:
		text += l.noNight
	case SleepComparisonIneligible:
		text += l.ineligible
	case SleepComparisonInsufficient:
		text += l.insufficient
	default:
		text += l.unavailable
	}
	if c.EligibleNights != nil {
		text += " " + fmt.Sprintf(l.window, *c.EligibleNights, c.WindowStart, c.WindowEndExclusive)
	}
	if c.Finalization == NightFinalProvisional {
		text += " " + l.preliminary
	}
	return l.title, text
}
