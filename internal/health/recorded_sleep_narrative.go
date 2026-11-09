package health

import (
	"fmt"
	"math"
	"strings"
)

const recordedSleepArchitectureMeaning = "observed architecture of the current recorded sleep episode; missing fields are unknown, not zero; calendar finalization is independent"
const recordedSleepComparisonMeaning = "current recorded duration versus the recent recorded mean including the latest night; not an older personal baseline or a sustained trend"

// RecordedSleepNarrativeFacts shares the same dated observations between the
// morning explanation and the independent Sleep opinion.
func RecordedSleepNarrativeFacts(sleep MorningReportSleep, lang string) []DailyInsightNarrativeFact {
	if !MorningSleepAnalysisEligible(&sleep, sleep.ReportDate) {
		return nil
	}
	facts := []DailyInsightNarrativeFact{RecordedSleepEpisodeFact(lang, *sleep.Hours)}
	add := func(id, meaning, statement string) {
		facts = append(facts, DailyInsightNarrativeFact{ID: id, Domain: "sleep", Meaning: meaning, Window: "current recorded episode", Authority: "server_derived", Fresh: true, Statement: statement, DisplayValues: narrativeDisplayValues(statement), EvidenceIDs: []string{id}})
	}
	if e := sleep.Episode; e != nil {
		var parts []string
		labels := map[string][]string{"ru": {"глубокий сон", "REM", "основной сон", "сон без стадий", "записанное бодрствование"}, "sr": {"duboki san", "REM", "osnovni san", "san bez faza", "zabeležena budnost"}, "en": {"deep sleep", "REM", "core sleep", "unstaged sleep", "recorded wakefulness"}}[normalizeDailyInsightLocale(lang)]
		for i, value := range []*int{e.DeepMinutes, e.REMMinutes, e.CoreMinutes, e.UnspecifiedMinutes, e.AwakeMinutes} {
			if value != nil {
				parts = append(parts, fmt.Sprintf("%s: %d min", labels[i], *value))
			}
		}
		if e.AwakeBouts != nil {
			parts = append(parts, fmt.Sprintf("recorded awake intervals: %d", *e.AwakeBouts))
		}
		if e.Start != nil && e.Wake != nil {
			parts = append(parts, e.Start.Format("15:04")+"–"+e.Wake.Format("15:04"))
		}
		if len(parts) > 0 {
			add("sleep_recorded_architecture", recordedSleepArchitectureMeaning, strings.Join(parts, "; ")+".")
		}
	}
	if sleep.RecentRecordedMeanHours != nil && sleep.RecentRecordedDifferenceMinutes != nil && sleep.RecentRecordedNights >= 2 {
		statement := fmt.Sprintf("Recorded sleep: %d min; mean of %d recent recorded nights including this one: %d min; difference: %+d min.", int(math.Round(*sleep.Hours*60)), sleep.RecentRecordedNights, int(math.Round(*sleep.RecentRecordedMeanHours*60)), *sleep.RecentRecordedDifferenceMinutes)
		if lang == "ru" {
			statement = fmt.Sprintf("Записанный сон: %d мин; среднее %d последних записей, включая эту: %d мин; разница: %+d мин.", int(math.Round(*sleep.Hours*60)), sleep.RecentRecordedNights, int(math.Round(*sleep.RecentRecordedMeanHours*60)), *sleep.RecentRecordedDifferenceMinutes)
		} else if lang == "sr" {
			statement = fmt.Sprintf("Zabeležen san: %d min; prosek %d skorašnjih zapisa uključujući ovaj: %d min; razlika: %+d min.", int(math.Round(*sleep.Hours*60)), sleep.RecentRecordedNights, int(math.Round(*sleep.RecentRecordedMeanHours*60)), *sleep.RecentRecordedDifferenceMinutes)
		}
		add("sleep_recorded_comparison", recordedSleepComparisonMeaning, statement)
	}
	return facts
}

// RecordedSleepAnalysisText is a factual fallback when model prose is absent.
// It explains the recorded difference; it never substitutes a generic warning.
func RecordedSleepAnalysisText(sleep MorningReportSleep, lang string) string {
	if !MorningSleepAnalysisEligible(&sleep, sleep.ReportDate) {
		return ""
	}
	var parts []string
	if diff := sleep.RecentRecordedDifferenceMinutes; diff != nil && sleep.RecentRecordedNights >= 2 {
		n := int(math.Abs(float64(*diff)))
		if n < 5 {
			format := map[string]string{"ru": "Длительность близка к среднему последних %d записей.", "sr": "Trajanje je blizu proseka poslednjih %d zapisa.", "en": "Duration is close to the mean of the last %d records."}[normalizeDailyInsightLocale(lang)]
			parts = append(parts, fmt.Sprintf(format, sleep.RecentRecordedNights))
		} else {
			switch normalizeDailyInsightLocale(lang) {
			case "ru":
				word := "короче"
				if *diff >= 0 {
					word = "длиннее"
				}
				parts = append(parts, fmt.Sprintf("Ночь на %d мин %s среднего последних %d записей.", n, word, sleep.RecentRecordedNights))
			case "sr":
				word := "kraći"
				if *diff >= 0 {
					word = "duži"
				}
				parts = append(parts, fmt.Sprintf("San je %d min %s od proseka poslednjih %d zapisa.", n, word, sleep.RecentRecordedNights))
			default:
				word := "shorter"
				if *diff >= 0 {
					word = "longer"
				}
				parts = append(parts, fmt.Sprintf("The night was %d min %s than the mean of the last %d records.", n, word, sleep.RecentRecordedNights))
			}
		}
	}
	if sleep.Episode != nil && sleep.Episode.AwakeMinutes != nil {
		format := map[string]string{"ru": "В записи %d мин бодрствования.", "sr": "Zabeleženo je %d min budnosti.", "en": "Recorded wakefulness totals %d min."}[normalizeDailyInsightLocale(lang)]
		parts = append(parts, fmt.Sprintf(format, *sleep.Episode.AwakeMinutes))
	}
	return strings.Join(parts, " ")
}
