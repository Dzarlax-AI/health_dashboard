package health

import (
	"fmt"
	"math"
	"strings"
)

// NormalizeMorningInsightEvidence creates the canonical provider-facing
// evidence used both for hashing and generation. It removes unsupported sleep
// baselines in every mode, then applies the preliminary-only claim boundary.
func NormalizeMorningInsightEvidence(evidence *MorningInsightEvidence, lang string) bool {
	if evidence == nil {
		return false
	}
	evidence.PreliminaryOptions = nil
	if sleep := evidence.NightSleep; sleep != nil {
		baselineEligible := sleep.Date != "" && sleep.Date == evidence.Date &&
			sleep.Capture == NightCaptureComplete && sleep.Assessment == NightDurationPlausible &&
			sleep.Finalization == NightFinalFinal && sleep.BaselineNights >= 7 &&
			sleep.BaselineHours != nil && !math.IsNaN(*sleep.BaselineHours) && !math.IsInf(*sleep.BaselineHours, 0) &&
			*sleep.BaselineHours > 0 && *sleep.BaselineHours <= 24
		if !baselineEligible {
			sleep.BaselineHours = nil
			sleep.BaselineNights = 0
		}
	}
	return ApplyPreliminaryMorningInsightPolicy(evidence, lang)
}

// ApplyPreliminaryMorningInsightPolicy constrains provider-facing evidence
// when the current night's plausible duration is not final. It removes raw
// sleep histories, strips sleep baselines, and supplies the only allowed
// activity/recovery explanations for the provider to select.
func ApplyPreliminaryMorningInsightPolicy(evidence *MorningInsightEvidence, lang string) bool {
	if evidence == nil || !PreliminaryMorningSleepExplanationEligible(evidence.NightSleep, evidence.Date) {
		return false
	}

	evidence.NightSleep.BaselineHours = nil
	evidence.NightSleep.BaselineNights = 0
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
