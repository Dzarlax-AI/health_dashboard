package health

import (
	"strings"
	"unicode"
)

// InsightFactContext is the bounded derived evidence shared by independent
// opinions and morning explanations. It excludes raw records and provenance.
type InsightFactContext struct {
	Date         string                      `json:"date"`
	Guidance     AIInsightServerView         `json:"guidance"`
	DomainStates []AIInsightDomainState      `json:"domain_states"`
	Facts        []DailyInsightNarrativeFact `json:"facts"`
}

func insightServerView(server DailyInsight) AIInsightServerView {
	view := AIInsightServerView{State: server.State, AnswerKind: server.AnswerKind,
		GapReason: server.GapReason, Observation: server.Observation, Meaning: server.Meaning}
	if server.NextStep != nil {
		view.Action = server.NextStep.Text
	}
	return view
}

// When sleep is unconfirmed, server prose may contain an unsupported sleep
// comparison even when it appears in overall, recovery, or energy guidance.
// Keep only the closed, server-selected decision mode and structural state.
func sleepSafeInsightServerView(server DailyInsight, unconfirmed bool) AIInsightServerView {
	view := insightServerView(server)
	if !unconfirmed {
		return view
	}
	view.Observation, view.Meaning, view.Action = "", "", ""
	if server.NextStep != nil {
		switch server.NextStep.ID {
		case "daily-decision-rest", "daily-decision-active_recovery", "daily-decision-moderate", "daily-decision-push_hard":
			view.ActionMode = strings.TrimPrefix(server.NextStep.ID, "daily-decision-")
		}
	}
	return view
}

func insightSleepUnconfirmed(snapshot *DailyInsightSnapshot) bool {
	for _, domain := range snapshot.Domains {
		if domain.Key == "sleep" {
			return domain.DataState != "fresh" || (domain.Confidence != "" && domain.Confidence != "final")
		}
	}
	return true
}

// Facts from other domains may repeat a sleep-derived headline or reason.
// Suppress those cross-domain facts while the night is unconfirmed.
func factMentionsSleep(fact DailyInsightNarrativeFact) bool {
	text := strings.ToLower(strings.Join(append([]string{fact.ID, fact.Meaning, fact.Window, fact.Statement}, fact.EvidenceIDs...), " "))
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if strings.Contains(word, "sleep") || strings.Contains(word, "night") || strings.HasPrefix(word, "сон") || strings.HasPrefix(word, "ноч") || strings.HasPrefix(word, "спа") || strings.HasPrefix(word, "spav") || strings.HasPrefix(word, "noć") || strings.HasPrefix(word, "noc") || word == "сна" || word == "сну" || word == "san" || word == "sna" || word == "snu" || word == "snom" {
			return true
		}
	}
	return false
}

func BuildInsightFactContext(snapshot *DailyInsightSnapshot) InsightFactContext {
	context := InsightFactContext{Facts: []DailyInsightNarrativeFact{}, DomainStates: []AIInsightDomainState{}}
	if snapshot == nil {
		return context
	}
	context.Date = snapshot.Date
	context.Guidance = sleepSafeInsightServerView(snapshot.Primary, insightSleepUnconfirmed(snapshot))
	context.Facts = aiInsightUsableFacts(snapshot)
	for _, domain := range snapshot.Domains {
		context.DomainStates = append(context.DomainStates, AIInsightDomainState{Domain: domain.Key, DataState: domain.DataState, Confidence: domain.Confidence})
	}
	return context
}

// Decision context is shared without turning a domain opinion into a general
// metric dump. Incomplete sleep can contribute no completed-night comparison.
func insightContextFactForSlot(fact DailyInsightNarrativeFact, slot string) bool {
	if fact.Domain == slot {
		return true
	}
	switch fact.ID {
	case "readiness_current", "readiness_hrv_current", "readiness_rhr_current", "energy_authoritative_state":
		return true
	default:
		return false
	}
}

func insightContextDomainFinal(context InsightFactContext, key string) bool {
	for _, state := range context.DomainStates {
		if state.Domain == key {
			return state.DataState == "fresh" && state.Confidence == "final"
		}
	}
	return false
}

func insightContextCrossFactEligible(context InsightFactContext, fact DailyInsightNarrativeFact) bool {
	// These producers separately require exact-day final component coverage.
	// A provisional overall recovery score must not hide confirmed HRV/RHR.
	if fact.Fresh && (fact.ID == "readiness_hrv_current" || fact.ID == "readiness_rhr_current") {
		return true
	}
	return insightContextDomainFinal(context, fact.Domain)
}

func localizedRecoveryAssessment(locale, band string) string {
	words := map[string]map[string]string{
		"ru": {"optimal": "Сегодня показатели восстановления высокие.", "fair": "Сегодня показатели восстановления умеренные.", "low": "Сегодня показатели восстановления ниже обычного."},
		"sr": {"optimal": "Današnji pokazatelji oporavka su visoki.", "fair": "Današnji pokazatelji oporavka su umereni.", "low": "Današnji pokazatelji oporavka su niži nego obično."},
		"en": {"optimal": "Today's recovery indicators are high.", "fair": "Today's recovery indicators are moderate.", "low": "Today's recovery indicators are below usual."},
	}
	return words[normalizeDailyInsightLocale(locale)][band]
}
