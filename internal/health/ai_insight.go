package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AIInsightVersion is deliberately separate from the retired explanatory
// narrative contract. Old approved prose cannot be decoded as a second opinion.
const AIInsightVersion = "today-ai-insight-v1"
const AIInsightInputVersion = "today-ai-insight-input-v5.3"

// AIInsightReaderCopyValidationRevision keeps cached/evaluated AI output tied
// to the local reader-copy safety gate as that gate gains observed regressions.
const AIInsightReaderCopyValidationRevision = "today-ai-insight-reader-copy-v5.3"

type DailyInsightAIInsight struct {
	Text              string   `json:"text"`
	Stance            string   `json:"stance"`
	AlternativeAction string   `json:"alternative_action,omitempty"`
	FactIDs           []string `json:"fact_ids"`
	EvidenceIDs       []string `json:"evidence_ids"`
}

type AIInsightServerView struct {
	State       string `json:"state"`
	AnswerKind  string `json:"answer_kind"`
	GapReason   string `json:"gap_reason,omitempty"`
	Observation string `json:"observation"`
	Meaning     string `json:"meaning"`
	Action      string `json:"action,omitempty"`
}

type AIInsightSibling struct {
	Slot              string `json:"slot"`
	Text              string `json:"text,omitempty"`
	AlternativeAction string `json:"alternative_action,omitempty"`
	State             string `json:"state"`
}

type AIInsightDomainState struct {
	Domain     string `json:"domain"`
	DataState  string `json:"data_state"`
	Confidence string `json:"confidence,omitempty"`
}

// AIInsightInput contains bounded, derived evidence and the server's view as
// a comparison point. It never contains raw samples or device provenance.
type AIInsightInput struct {
	Version       string                      `json:"version"`
	Locale        string                      `json:"locale"`
	Slot          string                      `json:"slot"`
	ServerInsight AIInsightServerView         `json:"server_insight"`
	Facts         []DailyInsightNarrativeFact `json:"facts"`
	DomainStates  []AIInsightDomainState      `json:"domain_states"`
	Siblings      []AIInsightSibling          `json:"siblings,omitempty"`
}

type AIInsightSection struct {
	Text              string   `json:"text"`
	FactIDs           []string `json:"fact_ids"`
	Stance            string   `json:"stance"`
	AlternativeAction string   `json:"alternative_action"`
}

type AIInsightSlotResponse struct {
	Version string            `json:"version"`
	Locale  string            `json:"locale"`
	Slot    string            `json:"slot"`
	Insight *AIInsightSection `json:"insight"`
}

// AIInsightValidationError exposes a stable, content-free reason for offline
// evaluation. Err remains internal and must not be copied to public telemetry.
type AIInsightValidationError struct {
	Code string
	Err  error
}

func (e *AIInsightValidationError) Error() string { return e.Err.Error() }
func (e *AIInsightValidationError) Unwrap() error { return e.Err }

func aiInsightValidationError(code, format string, args ...any) error {
	return &AIInsightValidationError{Code: code, Err: fmt.Errorf(format, args...)}
}

// BuildAIInsightInput returns no packet when there is no fresh, derived fact
// for this screen. A missing AI opinion must never hide its server counterpart.
func BuildAIInsightInput(snapshot *DailyInsightSnapshot, locale, slot string, siblings []AIInsightSibling) (AIInsightInput, bool) {
	locale = normalizeDailyInsightLocale(locale)
	if snapshot == nil || !isDailyInsightNarrativeSlot(slot) {
		return AIInsightInput{}, false
	}
	input := AIInsightInput{Version: AIInsightInputVersion, Locale: locale, Slot: slot, Facts: []DailyInsightNarrativeFact{}}
	var server DailyInsight
	if slot == DailyInsightNarrativeOverallSlot {
		server = snapshot.Primary
		input.Facts = append(input.Facts, aiInsightUsableFacts(snapshot)...)
		for _, domain := range snapshot.Domains {
			input.DomainStates = append(input.DomainStates, AIInsightDomainState{Domain: domain.Key, DataState: domain.DataState, Confidence: domain.Confidence})
		}
		input.Siblings = append([]AIInsightSibling(nil), siblings...)
	} else {
		found := false
		for _, domain := range snapshot.Domains {
			if domain.Key != slot {
				continue
			}
			found = true
			if domain.DataState != "fresh" || domain.Insight.State != "insight" {
				return AIInsightInput{}, false
			}
			server = domain.Insight
			input.DomainStates = append(input.DomainStates, AIInsightDomainState{Domain: domain.Key, DataState: domain.DataState, Confidence: domain.Confidence})
			break
		}
		if !found {
			return AIInsightInput{}, false
		}
		energyContextFresh := false
		if slot == "recovery" {
			for _, domain := range snapshot.Domains {
				if domain.Key == "energy" {
					input.DomainStates = append(input.DomainStates, AIInsightDomainState{Domain: domain.Key, DataState: domain.DataState, Confidence: domain.Confidence})
					energyContextFresh = domain.DataState == "fresh" && domain.Confidence == "final"
					break
				}
			}
		}
		for _, fact := range aiInsightUsableFacts(snapshot) {
			if fact.Domain == slot || (slot == "recovery" && energyContextFresh && fact.ID == "energy_authoritative_state" && fact.Domain == "energy") {
				input.Facts = append(input.Facts, fact)
			}
		}
	}
	if len(input.Facts) == 0 || server.State == "" ||
		(slot != DailyInsightNarrativeOverallSlot && (server.State != "insight" || server.Remediation != "")) {
		return AIInsightInput{}, false
	}
	input.ServerInsight = AIInsightServerView{State: server.State, AnswerKind: server.AnswerKind, GapReason: server.GapReason,
		Observation: server.Observation, Meaning: server.Meaning}
	if server.NextStep != nil {
		input.ServerInsight.Action = server.NextStep.Text
	}
	return input, true
}

// A date-aligned sleep sample can still be an incomplete sync. In that state,
// its current-night value and any trend containing it cannot support a claim
// about a completed night. Keep other domains available to overall instead of
// withholding the whole insight.
func aiInsightUsableFacts(snapshot *DailyInsightSnapshot) []DailyInsightNarrativeFact {
	domainStates := make(map[string]string, len(snapshot.Domains))
	for _, domain := range snapshot.Domains {
		domainStates[domain.Key] = domain.DataState
	}
	fresh := dailyInsightFreshNarrativeFacts(snapshot)
	facts := make([]DailyInsightNarrativeFact, 0, len(fresh))
	for _, original := range fresh {
		if original.Domain == "sleep" && domainStates["sleep"] != "fresh" {
			continue
		}
		if aiInsightHeadlineDuplicatesComponent(original, fresh) {
			continue
		}
		fact := original
		switch fact.ID {
		case "sleep_canonical_comparison":
			fact.Meaning = "latest recorded sleep duration versus the mean of up to seven most recent recorded nights, including latest; not the older personal baseline"
			fact.Window = "latest night and up to seven recent recorded nights including latest"
		case "headline_sleep_total":
			fact.Meaning = "latest sleep duration versus an older personal baseline from day eight onward; not the recent seven-night mean"
			fact.Window = "latest night versus recorded days eight and older"
		case "sleep_recent_four_day_pattern":
			fact.Meaning = "the newest two days versus the preceding two within a four-day window; distinct from the seven-night mean and older baseline"
		}
		facts = append(facts, fact)
	}
	return facts
}

// A confirmed component already carries the same current value and, when
// available, personal baseline. Keep a headline only if it adds a comparison
// that the component does not contain.
func aiInsightHeadlineDuplicatesComponent(headline DailyInsightNarrativeFact, facts []DailyInsightNarrativeFact) bool {
	componentID := ""
	switch headline.ID {
	case "headline_heart_rate_variability":
		componentID = "readiness_hrv_current"
	case "headline_resting_heart_rate":
		componentID = "readiness_rhr_current"
	default:
		return false
	}
	for _, component := range facts {
		if component.ID != componentID || component.Domain != headline.Domain || len(headline.DisplayValues) == 0 {
			continue
		}
		if !containsDailyInsightID(component.DisplayValues, headline.DisplayValues[0]) {
			continue
		}
		if len(headline.DisplayValues) == 1 || (len(headline.DisplayValues) > 1 && containsDailyInsightID(component.DisplayValues, headline.DisplayValues[1])) {
			return true
		}
	}
	return false
}

// AIInsightInputHash binds cache rows to exact evidence, server view, accepted
// sibling opinions, and version. A late sibling invalidates only overall.
func AIInsightInputHash(input AIInsightInput) string {
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func ValidateAIInsightSlot(input AIInsightInput, candidate AIInsightSlotResponse) (*DailyInsightAIInsight, error) {
	if candidate.Version != AIInsightVersion || candidate.Locale != input.Locale || candidate.Slot != input.Slot {
		return nil, aiInsightValidationError("identity_mismatch", "AI insight response identity does not match input")
	}
	if candidate.Insight == nil {
		return nil, nil
	}
	section := candidate.Insight
	text := strings.TrimSpace(section.Text)
	action := strings.TrimSpace(section.AlternativeAction)
	if text == "" || utf8.RuneCountInString(text) > 4000 || utf8.RuneCountInString(action) > 300 {
		return nil, aiInsightValidationError("text_bounds", "AI insight text exceeds technical bounds or is empty")
	}
	switch section.Stance {
	case "agree", "qualify", "disagree":
	default:
		return nil, aiInsightValidationError("invalid_stance", "unsupported AI insight stance %q", section.Stance)
	}
	// Each visible field is a separate utterance. Never infer grammar from a
	// synthetic sentence spanning the text/action boundary.
	for _, field := range []string{text, action} {
		if field != "" {
			if err := validateAIInsightLocale(field, input.Locale); err != nil {
				return nil, &AIInsightValidationError{Code: "locale_or_reader_copy", Err: err}
			}
		}
	}
	if len(section.FactIDs) == 0 {
		return nil, aiInsightValidationError("missing_fact_ids", "AI insight must cite at least one supplied fact")
	}
	facts := make(map[string]DailyInsightNarrativeFact, len(input.Facts))
	for _, fact := range input.Facts {
		facts[fact.ID] = fact
	}
	seen := map[string]bool{}
	evidence := []string{}
	hasOwnDomainFact := false
	for _, id := range section.FactIDs {
		fact, ok := facts[id]
		if !ok || seen[id] {
			return nil, aiInsightValidationError("invalid_fact_id", "unknown or duplicate AI insight fact ID %q", id)
		}
		seen[id] = true
		if fact.Domain == input.Slot {
			hasOwnDomainFact = true
		}
		for _, evidenceID := range fact.EvidenceIDs {
			if !containsDailyInsightID(evidence, evidenceID) {
				evidence = append(evidence, evidenceID)
			}
		}
	}
	if input.Slot != DailyInsightNarrativeOverallSlot && !hasOwnDomainFact {
		return nil, aiInsightValidationError("missing_domain_fact", "domain AI insight must cite its own domain")
	}
	if err := validateNarrativeNumbers(text+" "+action, input.Facts); err != nil {
		return nil, &AIInsightValidationError{Code: "unsupported_number", Err: err}
	}
	return &DailyInsightAIInsight{Text: text, Stance: section.Stance, AlternativeAction: action,
		FactIDs: append([]string(nil), section.FactIDs...), EvidenceIDs: evidence}, nil
}

func validateAIInsightLocale(text, locale string) error {
	if err := validateDailyInsightNarrativeLocale(text, locale); err != nil {
		return err
	}
	for _, r := range text {
		if unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("AI insight contains an invisible formatting character")
		}
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.In(r, unicode.Latin) {
			continue
		}
		if normalizeDailyInsightLocale(locale) == "ru" && unicode.In(r, unicode.Cyrillic) {
			continue
		}
		return fmt.Errorf("AI insight contains a letter outside the requested writing system")
	}
	if err := validateAIInsightReaderCopy(text, normalizeDailyInsightLocale(locale)); err != nil {
		return err
	}
	return nil
}

var (
	aiInsightParentheticalEnding = regexp.MustCompile(`\p{L}+\(\p{L}{1,4}\)`)
	aiInsightSlashedWords        = regexp.MustCompile(`\p{L}+/\p{L}+`)
	// Past tense itself is gender-marked, but a suffix-only rule mistakes
	// neutral nouns such as "сигнал" for a past-tense verb. Keep this to the
	// observed reader-address forms until more corpus evidence warrants a form.
	aiInsightRussianPastAddress = regexp.MustCompile(`(?i)(?:^|[^\p{L}])ты(?:\s+\p{L}+){0,3}\s+(?:спал|спала|восстановился|восстановилась)(?:$|[^\p{L}])`)
	// Keep this to direct, observed reader-state forms. It does not reject
	// ordinary informal Russian or every use of "ты".
	aiInsightRussianSecondPersonGenderedState         = regexp.MustCompile(`(?i)(?:^|[^\p{L}])ты(?:\s+\p{L}+){0,3}\s+(?:готов|готова|уверен|уверена|устал|устала|отдохнувший|отдохнувшая)(?:$|[^\p{L}])`)
	aiInsightRussianInvertedSecondPersonGenderedState = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:готов|готова|уверен|уверена|устал|устала|отдохнувший|отдохнувшая)\s+ли\s+ты(?:$|[^\p{L}])`)
	aiInsightRussianReadyGenderAlternative            = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:готов|готова|готовым|готовой)\s+или\s+(?:готов|готова|готовым|готовой)(?:$|[^\p{L}])`)
	aiInsightSerbianPastAddress                       = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:si(?:\s+\p{L}+){0,2}\s+\p{L}+(?:ao|ala|eo|ela|io|ila)|\p{L}+(?:ao|ala|eo|ela|io|ila)\s+si)(?:$|[^\p{L}])`)
	// These are observed model voice forms: the model speaks about itself in a
	// gender-marked past tense. Keep the guard narrow so ordinary Serbian prose
	// and gender-neutral first-person wording remain available.
	aiInsightSerbianFirstPersonPast = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:(?:ja\s+)?sam\s+(?:primetio|primetila|zaključio|zaključila|procenio|procenila|mislio|mislila|video|videla)|(?:primetio|primetila|zaključio|zaključila|procenio|procenila|mislio|mislila|video|videla)\s+sam)(?:$|[^\p{L}])`)
	// Likewise, "bih" marks the AI's conditional first person. Accept a
	// bounded clause only when it contains a gender-marked participle, rather
	// than banning first person or ordinary conditional/impersonal Serbian.
	aiInsightSerbianFirstPersonConditional = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:bih(?:[^\p{L}.!?]+\p{L}+){0,12}[^\p{L}.!?]+(?:izbegao|izbegla|držao|držala|menjao|menjala|shvatio|shvatila|ublažio|ublažila|zadržao|zadržala|uzeo|uzela|preporučio|preporučila)|(?:izbegao|izbegla|držao|držala|menjao|menjala|shvatio|shvatila|ublažio|ublažila|zadržao|zadržala|uzeo|uzela|preporučio|preporučila)\s+bih)(?:$|[^\p{L}])`)
	// These are direct reader states, distinct from the existing second-person
	// past-tense guard. The closed observed forms prevent an AI from assigning
	// the reader a grammatical gender without treating every "si" clause as an
	// error.
	aiInsightSerbianSecondPersonGenderedState = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:si(?:\s+\p{L}+){0,3}\s+(?:spreman|spremna|umoran|umorna|oporavljen|oporavljena|siguran|sigurna|aktivan|aktivna|odmoran|odmorna)|(?:spreman|spremna|umoran|umorna|oporavljen|oporavljena|siguran|sigurna|aktivan|aktivna|odmoran|odmorna)\s+si)(?:$|[^\p{L}])`)
)

// Reject obvious draft artifacts and gendered second-person shorthand, not
// opinion length or substance. These existing patterns are an early backstop,
// not a grammar classifier: the mandatory field language review in internal/ai
// handles implicit subjects and novel forms. Do not grow these word lists.
func validateAIInsightReaderCopy(text, locale string) error {
	if strings.ContainsAny(text, "[]") || strings.Contains(text, "```") {
		return fmt.Errorf("AI insight contains a drafting or markup artifact")
	}
	if locale != "ru" && locale != "sr" {
		return nil
	}
	if aiInsightParentheticalEnding.MatchString(text) || aiInsightSlashedWords.MatchString(text) {
		return fmt.Errorf("AI insight contains a gender-alternative shorthand")
	}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }) {
		word = strings.ToLower(word)
		if locale == "ru" && (word == "вы" || word == "вам" || word == "вас" || word == "вами" || strings.HasPrefix(word, "ваш")) {
			return fmt.Errorf("AI insight uses formal address instead of informal singular")
		}
		if locale == "sr" && (word == "vi" || word == "vas" || word == "vama" || strings.HasPrefix(word, "vaš")) {
			return fmt.Errorf("AI insight uses formal address instead of informal singular")
		}
	}
	if (locale == "ru" && (aiInsightRussianPastAddress.MatchString(text) || aiInsightRussianSecondPersonGenderedState.MatchString(text) || aiInsightRussianInvertedSecondPersonGenderedState.MatchString(text) || aiInsightRussianReadyGenderAlternative.MatchString(text))) || (locale == "sr" && aiInsightSerbianPastAddress.MatchString(text)) {
		return fmt.Errorf("AI insight assumes the reader's grammatical gender")
	}
	if locale == "sr" && (aiInsightSerbianFirstPersonPast.MatchString(text) || aiInsightSerbianFirstPersonConditional.MatchString(text) || aiInsightSerbianSecondPersonGenderedState.MatchString(text)) {
		return fmt.Errorf("AI insight assumes Serbian grammatical gender")
	}
	return nil
}

func ApplyAIInsightSlot(snapshot *DailyInsightSnapshot, slot string, insight *DailyInsightAIInsight) (*DailyInsightSnapshot, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("AI insight snapshot is nil")
	}
	out := cloneDailyInsightSnapshot(snapshot)
	if insight == nil {
		return out, nil
	}
	copyInsight := *insight
	copyInsight.FactIDs = append([]string(nil), insight.FactIDs...)
	copyInsight.EvidenceIDs = append([]string(nil), insight.EvidenceIDs...)
	if slot == DailyInsightNarrativeOverallSlot {
		out.AIInsight = &copyInsight
		return out, nil
	}
	for index := range out.Domains {
		if out.Domains[index].Key == slot {
			out.Domains[index].AIInsight = &copyInsight
			return out, nil
		}
	}
	return nil, fmt.Errorf("snapshot lacks AI insight slot %q", slot)
}
