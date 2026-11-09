package health

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// NormalizeMorningInsightEvidence creates the canonical provider-facing
// evidence used both for hashing and generation. It removes unsupported sleep
// baselines in every mode, then applies the preliminary-only claim boundary.
func NormalizeMorningInsightEvidence(evidence *MorningInsightEvidence, lang string) bool {
	if evidence == nil {
		return false
	}
	evidence.PreliminaryOptions = nil
	// The activity block is dated yesterday. Do not offer today's activity as
	// a competing source, while retaining today's recovery measurements.
	if len(evidence.Daily) > 0 {
		evidence.Daily = append([]DailyHealthMetrics(nil), evidence.Daily...)
		for i := range evidence.Daily {
			if evidence.Daily[i].Date >= evidence.Date {
				evidence.Daily[i].Steps, evidence.Daily[i].Calories, evidence.Daily[i].Exercise = nil, nil, nil
			}
		}
	}
	if sleep := evidence.NightSleep; sleep != nil {
		baselineEligible := MorningSleepAnalysisEligible(sleep, evidence.Date) && sleep.BaselineNights >= 7 &&
			sleep.BaselineHours != nil && !math.IsNaN(*sleep.BaselineHours) && !math.IsInf(*sleep.BaselineHours, 0) &&
			*sleep.BaselineHours > 0 && *sleep.BaselineHours <= 24
		if !baselineEligible {
			sleep.BaselineHours = nil
			sleep.BaselineNights = 0
		}
	}
	if sleep := evidence.NightSleep; sleep == nil || sleep.Date != evidence.Date || sleep.Capture != NightCaptureComplete || sleep.Assessment != NightDurationPlausible || sleep.Finalization != NightFinalFinal {
		stripMorningSleepContext(evidence)
	}
	return ApplyPreliminaryMorningInsightPolicy(evidence, lang)
}

func stripMorningSleepContext(evidence *MorningInsightEvidence) {
	if len(evidence.Daily) > 0 {
		evidence.Daily = append([]DailyHealthMetrics(nil), evidence.Daily...)
		for i := range evidence.Daily {
			evidence.Daily[i].Sleep, evidence.Daily[i].Deep, evidence.Daily[i].REM = nil, nil, nil
			evidence.Daily[i].Core, evidence.Daily[i].Unspecified, evidence.Daily[i].Awake = nil, nil, nil
		}
	}
	if evidence.FactContext == nil {
		return
	}
	context := *evidence.FactContext
	context.Guidance = AIInsightServerView{}
	context.Facts = make([]DailyInsightNarrativeFact, 0, len(evidence.FactContext.Facts))
	for _, fact := range evidence.FactContext.Facts {
		sleepFact := fact.Domain == "sleep" || strings.Contains(strings.ToLower(fact.ID), "sleep") || strings.Contains(strings.ToLower(fact.Meaning), "sleep")
		for _, id := range fact.EvidenceIDs {
			sleepFact = sleepFact || strings.Contains(strings.ToLower(id), "sleep")
		}
		if !sleepFact {
			context.Facts = append(context.Facts, fact)
		}
	}
	evidence.FactContext = &context
}

// ApplyPreliminaryMorningInsightPolicy constrains provider-facing evidence
// when the current night's plausible duration is not final. It removes raw
// sleep histories, strips sleep baselines, and supplies the only allowed
// activity/recovery explanations for the provider to select.
func ApplyPreliminaryMorningInsightPolicy(evidence *MorningInsightEvidence, lang string) bool {
	if evidence == nil || !PreliminaryMorningSleepExplanationEligible(evidence.NightSleep, evidence.Date) {
		return false
	}

	if !MorningSleepAnalysisEligible(evidence.NightSleep, evidence.Date) {
		evidence.NightSleep.BaselineHours = nil
		evidence.NightSleep.BaselineNights = 0
	}
	if len(evidence.Daily) > 0 {
		evidence.Daily = append([]DailyHealthMetrics(nil), evidence.Daily...)
		for i := range evidence.Daily {
			evidence.Daily[i].Sleep = nil
			evidence.Daily[i].Deep = nil
			evidence.Daily[i].REM = nil
			evidence.Daily[i].Core = nil
			evidence.Daily[i].Unspecified = nil
			evidence.Daily[i].Awake = nil
		}
	}

	evidence.PreliminaryOptions = &PreliminaryMorningInsightOptions{
		Activity: preliminaryOptions(evidence.Sections, []string{"activity", "cardio"}, "activity", fallbackActivityExplanation(lang)),
		Recovery: preliminaryOptions(evidence.Sections, []string{"recovery"}, "recovery", fallbackRecoveryExplanation(lang)),
	}
	if evidence.FactContext != nil {
		if insightContextDomainFinal(*evidence.FactContext, "activity") {
			evidence.PreliminaryOptions.Activity = nil
		}
		if insightContextDomainFinal(*evidence.FactContext, "recovery") {
			evidence.PreliminaryOptions.Recovery = nil
		}
	}
	if activity := evidence.NightSleep.YesterdayActivity; activity != nil && (activity.Steps != nil || activity.Calories != nil || activity.Exercise != nil) {
		if date, err := time.Parse("2006-01-02", evidence.Date); err == nil && activity.Date == date.AddDate(0, 0, -1).Format("2006-01-02") {
			evidence.PreliminaryOptions.Activity = nil
		}
	}
	return true
}

func preliminaryOptions(sections []MorningInsightSection, keys []string, domain, fallback string) []MorningInsightOption {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	var texts []string
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || len(strings.Fields(value)) > 60 {
			return
		}
		seen[value] = true
		texts = append(texts, value)
	}
	for _, key := range keys {
		for _, section := range sections {
			if section.Key != key || !allowed[section.Key] {
				continue
			}
			add(section.Summary)
			for _, detail := range section.Details {
				if label, note := strings.TrimSpace(detail.Label), strings.TrimSpace(detail.Note); label != "" && note != "" {
					add(fmt.Sprintf("%s: %s", label, note))
				}
			}
		}
	}
	if len(texts) == 0 {
		texts = append(texts, fallback)
	}
	options := make([]MorningInsightOption, 0, len(texts))
	for i, text := range texts {
		options = append(options, MorningInsightOption{ID: fmt.Sprintf("%s_%d", domain, i+1), Text: text})
	}
	return options
}

func fallbackActivityExplanation(lang string) string {
	return map[string]string{
		"en": "Current activity and cardio information is limited.",
		"ru": "Данных об активности и кардиопоказателях пока мало.",
		"sr": "Za sada ima malo podataka o aktivnosti i kardio-pokazateljima.",
	}[supportedMorningInsightLanguage(lang)]
}

func fallbackRecoveryExplanation(lang string) string {
	return map[string]string{
		"en": "Current recovery information is limited.",
		"ru": "Данных о восстановлении пока мало.",
		"sr": "Za sada ima malo podataka o oporavku.",
	}[supportedMorningInsightLanguage(lang)]
}

func supportedMorningInsightLanguage(lang string) string {
	if lang == "ru" || lang == "sr" {
		return lang
	}
	return "en"
}
