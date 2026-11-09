package health

import (
	"fmt"
	"math"
	"strings"
)

const recordedSleepArchitectureMeaning = "recorded stages, wakefulness and timing of the current sleep episode; omitted measurements are unknown, not zero"
const recordedSleepComparisonMeaning = "current recorded duration compared with the mean of the most recent recorded nights, including this episode"

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
		locale := normalizeDailyInsightLocale(lang)
		unit := map[string]string{"ru": "мин", "sr": "min", "en": "min"}[locale]
		labels := map[string][]string{"ru": {"глубокий сон", "REM", "основной сон", "сон без стадий", "записанное бодрствование"}, "sr": {"duboki san", "REM", "osnovni san", "san bez faza", "zabeležena budnost"}, "en": {"deep sleep", "REM", "core sleep", "unstaged sleep", "recorded wakefulness"}}[normalizeDailyInsightLocale(lang)]
		for i, value := range []*int{e.DeepMinutes, e.REMMinutes, e.CoreMinutes, e.UnspecifiedMinutes, e.AwakeMinutes} {
			if value != nil {
				valueText := fmt.Sprintf("%d %s", *value, unit)
				if *value >= 60 {
					valueText += " (" + readableSleepMinutes(*value, locale) + ")"
				}
				parts = append(parts, labels[i]+": "+valueText)
			}
		}
		if e.AwakeBouts != nil {
			label := map[string]string{"ru": "эпизоды бодрствования", "sr": "intervali budnosti", "en": "recorded awake intervals"}[locale]
			parts = append(parts, fmt.Sprintf("%s: %d", label, *e.AwakeBouts))
		}
		if e.Start != nil && e.Wake != nil {
			label := map[string]string{"ru": "время сна", "sr": "vreme sna", "en": "sleep window"}[locale]
			parts = append(parts, label+": "+e.Start.Format("15:04")+"–"+e.Wake.Format("15:04"))
		}
		if len(parts) > 0 {
			add("sleep_recorded_architecture", recordedSleepArchitectureMeaning, strings.Join(parts, "; ")+".")
		}
	}
	if sleep.RecentRecordedMeanHours != nil && sleep.RecentRecordedDifferenceMinutes != nil && sleep.RecentRecordedNights >= 2 {
		statement := fmt.Sprintf("Sleep: %s; mean of %d recent recorded nights including this one: %s; difference: %+d min.", readableSleepDuration(*sleep.Hours, lang), sleep.RecentRecordedNights, readableSleepDuration(*sleep.RecentRecordedMeanHours, lang), *sleep.RecentRecordedDifferenceMinutes)
		if lang == "ru" {
			statement = fmt.Sprintf("Сон за ночь: %s; среднее %d последних ночей, включая эту: %s; разница: %+d мин.", readableSleepDuration(*sleep.Hours, lang), sleep.RecentRecordedNights, readableSleepDuration(*sleep.RecentRecordedMeanHours, lang), *sleep.RecentRecordedDifferenceMinutes)
		} else if lang == "sr" {
			statement = fmt.Sprintf("San tokom noći: %s; prosek %d poslednjih noći uključujući ovu: %s; razlika: %+d min.", readableSleepDuration(*sleep.Hours, lang), sleep.RecentRecordedNights, readableSleepDuration(*sleep.RecentRecordedMeanHours, lang), *sleep.RecentRecordedDifferenceMinutes)
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
