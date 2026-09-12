package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

// DailyInsightSnapshot is the deterministic, client-safe basis for Today.
// It deliberately contains no model output: policy chooses the domain,
// evidence, and allowed action before any narrative provider is involved.
const DailyInsightSnapshotVersion = "daily-insight-v2"

// These versions are part of the material contract. Changing policy or the
// action catalogue must invalidate a previously generated narrative even if
// the visible health values happen to be unchanged.
const (
	DailyInsightPolicyVersion         = "daily-insight-policy-v2"
	DailyInsightActionCatalogVersion  = "daily-insight-actions-v1"
	DailyInsightPromptRevision        = "daily-insight-prompt-v4"
	DailyInsightNarrativeInputVersion = "today-domain-prose-input-v1"
	DailyInsightNarrativeVersion      = "today-domain-prose-v1"
)

const (
	DailyInsightAnswerConfirmedPersonal = "confirmed_personal"
	DailyInsightAnswerProvisional       = "provisional_pattern"
	DailyInsightAnswerFactual           = "factual_context"
	DailyInsightAnswerDataGuidance      = "data_guidance"
)

type insightCopy struct {
	primaryTitle, primaryMeaning, sleepTitle, sleepMissing, sleepIncomplete, sleepAvailable, sleepMeaning, recoveryTitle, recoveryMeaning, energyTitle, energyMissing, energyIncomplete, energyMeaning, locale string
}

func dailyInsightCopy(lang string) insightCopy {
	if lang == "ru" {
		return insightCopy{"Главное на сегодня", "Это текущая наиболее осторожная рекомендация на день.", "Сон", "Данные сна пока недоступны.", "Контекст сна на сегодня неполный.", "Данные сна доступны для оценки восстановления.", "Прошлая ночь влияет на сегодняшнюю готовность.", "Восстановление", "Готовность и восстановление задают границы дня.", "Энергия", "Данные об энергии пока недоступны.", "Рекомендация по энергии на день пока недоступна.", "EnergyBank отражает текущий запас и расход энергии.", "ru"}
	}
	if lang == "sr" {
		return insightCopy{"Glavno za danas", "Ovo je trenutno najopreznija preporuka za dan.", "San", "Podaci o snu još nisu dostupni.", "Kontekst sna za danas je nepotpun.", "Podaci o snu su dostupni za procenu oporavka.", "Prošla noć utiče na današnju spremnost.", "Oporavak", "Spremnost i oporavak postavljaju granice dana.", "Energija", "Podaci o energiji još nisu dostupni.", "Preporuka za dnevnu energiju nije dostupna.", "EnergyBank pokazuje trenutnu rezervu i potrošnju energije.", "sr"}
	}
	return insightCopy{"Today’s focus", "This is the most conservative current-day guidance.", "Sleep", "Sleep data is not available yet.", "Today’s sleep context is incomplete.", "Sleep data is available for recovery context.", "Last night contributes to today’s readiness.", "Recovery", "Readiness and recovery signals set today’s guardrails.", "Energy", "Energy data is not available yet.", "A daily energy recommendation is unavailable.", "EnergyBank reflects the current reserve and drain.", "en"}
}

type DailyInsightDestination struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type DailyInsightEvidence struct {
	ID               string                  `json:"id"`
	Domain           string                  `json:"domain"`
	ObservedAt       *time.Time              `json:"observed_at,omitempty"`
	ComparisonPeriod string                  `json:"comparison_period,omitempty"`
	Comparison       string                  `json:"comparison,omitempty"`
	DataState        string                  `json:"data_state"`
	Confidence       string                  `json:"confidence,omitempty"`
	Value            *float64                `json:"value,omitempty"`
	Baseline         *float64                `json:"baseline,omitempty"`
	Delta            *float64                `json:"delta,omitempty"`
	Unit             string                  `json:"unit,omitempty"`
	Destination      DailyInsightDestination `json:"destination"`
}

type DailyInsightAction struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type DailyInsight struct {
	State       string              `json:"state"`
	AnswerKind  string              `json:"answer_kind"`
	ClaimID     string              `json:"claim_id,omitempty"`
	GapReason   string              `json:"gap_reason,omitempty"`
	Remediation string              `json:"remediation_id,omitempty"`
	Title       string              `json:"title"`
	Observation string              `json:"observation"`
	Meaning     string              `json:"meaning"`
	NextStep    *DailyInsightAction `json:"next_step,omitempty"`
	EvidenceIDs []string            `json:"evidence_ids"`
	Fallback    bool                `json:"fallback"`
	// Narrative is an optional, best-effort explanation of this exact
	// server-owned section. Observation and Meaning remain the factual
	// fallback and continue to render when an overlay is missing or rejected.
	Narrative *DailyInsightNarrativeOverlay `json:"narrative,omitempty"`
}

// DailyInsightNarrativeOverlay is additive client-facing presentation data.
// Claim and evidence links let clients render it without treating prose as a
// new source of truth.
type DailyInsightNarrativeOverlay struct {
	Text        string   `json:"text"`
	ClaimIDs    []string `json:"claim_ids"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// ApplyRecentSleepBelowReference adds the one Phase B0 claim to an otherwise
// complete factual snapshot. It never changes the primary decision, and it
// never turns an unknown canonical record into a health action.
func ApplyRecentSleepBelowReference(snapshot *DailyInsightSnapshot, claim RecentSleepBelowReference, locale string) *DailyInsightSnapshot {
	if snapshot == nil {
		return snapshot
	}
	copy := cloneDailyInsightSnapshot(snapshot)
	copy.PolicyDigest = claim.EvidenceDigest
	if claim.State == RecentSleepClaimFalse || claim.State == RecentSleepClaimUnknown {
		return copy
	}
	for index := range copy.Domains {
		domain := &copy.Domains[index]
		if domain.Key != "sleep" {
			continue
		}
		switch claim.State {
		case RecentSleepClaimProvisional:
			domain.DataState, domain.Confidence = "partial", "provisional"
			domain.Insight.State = "insight"
			domain.Insight.AnswerKind = DailyInsightAnswerProvisional
			domain.Insight.ClaimID = ""
			domain.Insight.GapReason = "sleep_current_sync"
			domain.Insight.Remediation = ""
			domain.Insight.Observation, domain.Insight.Meaning = localizedCurrentSyncSleepContext(locale)
			domain.Insight.NextStep = nil
		case RecentSleepClaimTrue:
			domain.DataState, domain.Confidence = "fresh", "final"
			domain.Insight.State = "insight"
			domain.Insight.AnswerKind = DailyInsightAnswerConfirmedPersonal
			domain.Insight.ClaimID = "recent_sleep_below_reference"
			domain.Insight.GapReason, domain.Insight.Remediation = "", ""
			domain.Insight.Observation, domain.Insight.Meaning = localizedRecentSleepBelowReference(locale)
			domain.Insight.EvidenceIDs = []string{"sleep_recent_reference", "sleep_recent_short_nights"}
			copy.Evidence = append(copy.Evidence, recentSleepClaimEvidence(claim, domain.Destination, copy.UpdatedAt)...)
			if claim.ActionEvent {
				id, text := localizedWindDownAction(locale)
				domain.Insight.NextStep = &DailyInsightAction{ID: id, Text: text}
			} else {
				domain.Insight.NextStep = nil
			}
		}
		return copy
	}
	return copy
}

// recentSleepClaimEvidence replaces display-aggregate references for the B0
// sleep claim. A future narrative overlay can therefore acknowledge only the
// same canonical history and current window that decided the claim.
func recentSleepClaimEvidence(claim RecentSleepBelowReference, destination DailyInsightDestination, observedAt *time.Time) []DailyInsightEvidence {
	reference := claim.ReferenceHours
	shortNights := float64(claim.CurrentShortDays)
	threshold := 3.0
	return []DailyInsightEvidence{
		{
			ID: "sleep_recent_reference", Domain: "sleep", ObservedAt: observedAt,
			ComparisonPeriod: "D-93..D-4; at least 60 final nights", Comparison: "personal canonical reference",
			DataState: "fresh", Confidence: "final", Value: &reference, Unit: "h", Destination: destination,
		},
		{
			ID: "sleep_recent_short_nights", Domain: "sleep", ObservedAt: observedAt,
			ComparisonPeriod: "D-3..D", Comparison: "nights at least 0.5 h below the personal reference",
			DataState: "fresh", Confidence: "final", Value: &shortNights, Baseline: &threshold, Unit: "nights", Destination: destination,
		},
	}
}

type DailyInsightDomain struct {
	Key         string                  `json:"key"`
	Band        string                  `json:"band"`
	DataState   string                  `json:"data_state"`
	Confidence  string                  `json:"confidence,omitempty"`
	AsOf        *time.Time              `json:"as_of,omitempty"`
	Summary     string                  `json:"summary"`
	Insight     DailyInsight            `json:"insight"`
	Destination DailyInsightDestination `json:"destination"`
	// NarrativeSubject is a closed server-only variant for a permitted B1
	// proposition. It must participate in the material hash, but does not
	// belong to the client snapshot or become display copy.
	NarrativeSubject string `json:"-"`
}

type DailyInsightChange struct {
	ID          string                  `json:"id"`
	Domain      string                  `json:"domain"`
	Severity    string                  `json:"severity"`
	Title       string                  `json:"title"`
	Detail      string                  `json:"detail"`
	EvidenceIDs []string                `json:"evidence_ids"`
	Destination DailyInsightDestination `json:"destination"`
}

type DailyInsightSnapshot struct {
	Date       string                 `json:"date"`
	DecisionID string                 `json:"decision_id"`
	Version    string                 `json:"snapshot_version"`
	UpdatedAt  *time.Time             `json:"updated_at,omitempty"`
	Primary    DailyInsight           `json:"primary"`
	Domains    []DailyInsightDomain   `json:"domains"`
	Evidence   []DailyInsightEvidence `json:"evidence"`
	Changes    []DailyInsightChange   `json:"changes"`
	HasMore    bool                   `json:"has_more"`
	// PolicyDigest is server-internal cache material. It binds a future
	// narrative overlay to the exact canonical sleep records without exposing
	// an implementation hash as a user-facing fact.
	PolicyDigest string `json:"-"`
}

// DailyInsightNarrativeInput is the provider-facing claim packet. It is
// deliberately distinct from DailyInsightSnapshot: display copy, actions,
// primary policy and unrelated domain fields never become model input.
type DailyInsightNarrativeInput struct {
	Version string                             `json:"version"`
	Locale  string                             `json:"locale"`
	Domains []DailyInsightNarrativeDomainInput `json:"domains"`
}

type DailyInsightNarrativeDomainInput struct {
	Key    string                       `json:"key"`
	Claims []DailyInsightNarrativeClaim `json:"claims"`
}

type DailyInsightNarrativeClaim struct {
	ID                   string   `json:"id"`
	Domain               string   `json:"domain"`
	Kind                 string   `json:"kind"`
	Proposition          string   `json:"proposition"`
	EvidenceIDs          []string `json:"evidence_ids"`
	ComparisonPeriod     string   `json:"comparison_period,omitempty"`
	Confidence           string   `json:"confidence,omitempty"`
	RequiredQualifierIDs []string `json:"required_qualifier_ids,omitempty"`
}

// DailyInsightNarrative is the narrow provider result. Primary and actions
// intentionally have no model-owned text. A nil section is a valid request to
// keep the deterministic fallback for that domain.
type DailyInsightNarrative struct {
	Version string                        `json:"version"`
	Locale  string                        `json:"locale"`
	Domains []DailyInsightNarrativeDomain `json:"domains"`
}

type DailyInsightNarrativeSection struct {
	Sentences []DailyInsightNarrativeSentence `json:"sentences"`
}

type DailyInsightNarrativeSentence struct {
	Text         string   `json:"text"`
	ClaimIDs     []string `json:"claim_ids"`
	QualifierIDs []string `json:"qualifier_ids"`
}

type DailyInsightNarrativeDomain struct {
	Key     string                        `json:"key"`
	Section *DailyInsightNarrativeSection `json:"section"`
}

// BuildDailyInsightNarrativeInput converts a factual snapshot into the closed
// proposition set that an optional provider may explain. It never sends the
// primary, action copy, or display strings to the model.
func BuildDailyInsightNarrativeInput(snapshot *DailyInsightSnapshot, locale string) DailyInsightNarrativeInput {
	input := DailyInsightNarrativeInput{
		Version: DailyInsightNarrativeInputVersion,
		Locale:  normalizeDailyInsightLocale(locale),
		Domains: make([]DailyInsightNarrativeDomainInput, 0, 3),
	}
	if snapshot == nil {
		return input
	}
	for _, domain := range snapshot.Domains {
		packet := DailyInsightNarrativeDomainInput{Key: domain.Key, Claims: []DailyInsightNarrativeClaim{}}
		if domainNarrativeEligible(domain) {
			packet.Claims = append(packet.Claims, buildDailyInsightNarrativeClaim(snapshot, domain, input.Locale))
		}
		input.Domains = append(input.Domains, packet)
	}
	return input
}

func normalizeDailyInsightLocale(locale string) string {
	switch locale {
	case "ru", "sr":
		return locale
	default:
		return "en"
	}
}

func domainNarrativeEligible(domain DailyInsightDomain) bool {
	// A generic "current context is available" sentence adds no interpretation
	// beyond the card the person is already reading. Do not spend a provider
	// request to paraphrase it. B1 is an optional explanation of a distinct,
	// server-selected claim; the deterministic factual card remains the useful
	// answer on ordinary days.
	if domain.Insight.State != "insight" || domain.Insight.Remediation != "" || domain.Insight.ClaimID == "" {
		return false
	}
	switch domain.Insight.AnswerKind {
	case DailyInsightAnswerConfirmedPersonal, DailyInsightAnswerFactual:
		return domain.DataState == "fresh" && len(domain.Insight.EvidenceIDs) > 0
	default:
		return false
	}
}

// HasEligibleDailyInsightNarrativeClaims reports whether a snapshot contains
// at least one non-generic, server-owned claim that B1 may explain. It is a
// cost and quality boundary: an all-factual snapshot must not make a provider
// call merely to restate the visible cards.
func HasEligibleDailyInsightNarrativeClaims(snapshot *DailyInsightSnapshot, locale string) bool {
	for _, domain := range BuildDailyInsightNarrativeInput(snapshot, locale).Domains {
		if len(domain.Claims) > 0 {
			return true
		}
	}
	return false
}

func buildDailyInsightNarrativeClaim(snapshot *DailyInsightSnapshot, domain DailyInsightDomain, locale string) DailyInsightNarrativeClaim {
	claim := DailyInsightNarrativeClaim{
		ID:                   domain.Key + "_current_context",
		Domain:               domain.Key,
		Kind:                 "observation",
		EvidenceIDs:          append([]string(nil), domain.Insight.EvidenceIDs...),
		Confidence:           domain.Confidence,
		RequiredQualifierIDs: []string{"current_context"},
	}
	if domain.Insight.ClaimID != "" {
		claim.ID = domain.Insight.ClaimID
	}
	// The B0 rule is a four-night pattern, not a statement about whichever
	// individual sleep row happens to be latest. Keep that policy-selected
	// meaning intact instead of deriving a tempting but potentially false
	// one-night comparison from the display evidence.
	if claim.ID == "recent_sleep_below_reference" {
		claim.Kind = "comparison"
		claim.RequiredQualifierIDs = []string{"personal_pattern", "current_context"}
		claim.Proposition = localizedRecentSleepNarrativeProposition(locale)
		return claim
	}
	if claim.ID == "recovery_readiness_context" {
		claim.Proposition = localizedRecoveryNarrativeProposition(locale, domain.Band)
		return claim
	}
	if claim.ID == "energy_current_verdict_context" {
		claim.Proposition = localizedEnergyNarrativeProposition(locale, domain.NarrativeSubject)
		return claim
	}

	for _, evidence := range snapshot.Evidence {
		if evidence.Domain != domain.Key || !containsDailyInsightID(domain.Insight.EvidenceIDs, evidence.ID) {
			continue
		}
		claim.ComparisonPeriod = evidence.ComparisonPeriod
		if evidence.Confidence != "" {
			claim.Confidence = evidence.Confidence
		}
		if domain.Key == "sleep" && evidence.Delta != nil {
			claim.Proposition = localizedSleepNarrativeProposition(locale, *evidence.Delta)
			return claim
		}
		break
	}
	claim.Proposition = localizedDomainNarrativeProposition(locale, domain)
	return claim
}

func localizedRecentSleepNarrativeProposition(locale string) string {
	switch locale {
	case "ru":
		return "Несколько последних ночей были короче личного исторического ориентира сна."
	case "sr":
		return "Nekoliko poslednjih noći bilo je kraće od ličnog istorijskog obrasca sna."
	default:
		return "Several recent nights were shorter than the personal historical sleep reference."
	}
}

func localizedSleepNarrativeProposition(locale string, delta float64) string {
	if delta > 0.05 {
		switch locale {
		case "ru":
			return "Последняя ночь была длиннее недавнего личного среднего сна."
		case "sr":
			return "Poslednja noć je bila duža od nedavnog ličnog proseka sna."
		default:
			return "Last night was longer than the recent personal sleep average."
		}
	}
	if delta < -0.05 {
		switch locale {
		case "ru":
			return "Последняя ночь была короче недавнего личного среднего сна."
		case "sr":
			return "Poslednja noć je bila kraća od nedavnog ličnog proseka sna."
		default:
			return "Last night was shorter than the recent personal sleep average."
		}
	}
	switch locale {
	case "ru":
		return "Последняя ночь близка к недавнему личному среднему сна."
	case "sr":
		return "Poslednja noć je blizu nedavnog ličnog proseka sna."
	default:
		return "Last night is close to the recent personal sleep average."
	}
}

func localizedRecoveryNarrativeProposition(locale, band string) string {
	switch locale {
	case "ru":
		if band == "optimal" {
			return "Сигналы восстановления сегодня находятся в верхнем диапазоне готовности."
		}
		return "Сигналы восстановления сегодня находятся в нижнем диапазоне готовности."
	case "sr":
		if band == "optimal" {
			return "Signali oporavka su danas u višem opsegu spremnosti."
		}
		return "Signali oporavka su danas u nižem opsegu spremnosti."
	default:
		if band == "optimal" {
			return "Today’s recovery signals sit in the higher readiness band."
		}
		return "Today’s recovery signals sit in the lower readiness band."
	}
}

func localizedEnergyNarrativeProposition(locale, verdict string) string {
	switch locale {
	case "ru":
		switch verdict {
		case "push_hard":
			return "Текущий энергетический контекст находится в диапазоне более высокого ресурса."
		case "rest":
			return "Текущий энергетический контекст находится в диапазоне низкого ресурса."
		default:
			return "Текущий энергетический контекст находится в диапазоне восстановления."
		}
	case "sr":
		switch verdict {
		case "push_hard":
			return "Trenutni energetski kontekst je u opsegu većeg energetskog kapaciteta."
		case "rest":
			return "Trenutni energetski kontekst je u opsegu nižeg energetskog kapaciteta."
		default:
			return "Trenutni energetski kontekst je u opsegu oporavka."
		}
	default:
		switch verdict {
		case "push_hard":
			return "The current energy context is in a higher-capacity range."
		case "rest":
			return "The current energy context is in a lower-capacity range."
		default:
			return "The current energy context is in a recovery-oriented range."
		}
	}
}

func localizedDomainNarrativeProposition(locale string, domain DailyInsightDomain) string {
	switch locale {
	case "ru":
		switch domain.Key {
		case "recovery":
			return "Текущие сигналы восстановления доступны для сегодняшнего контекста."
		case "energy":
			return "Текущий запас энергии доступен как контекст для сегодняшнего темпа."
		default:
			return "Текущий контекст сна доступен для сегодняшнего наблюдения."
		}
	case "sr":
		switch domain.Key {
		case "recovery":
			return "Trenutni signali oporavka dostupni su za današnji kontekst."
		case "energy":
			return "Trenutna rezerva energije dostupna je kao kontekst za današnji tempo."
		default:
			return "Trenutni kontekst sna dostupan je za današnje praćenje."
		}
	default:
		switch domain.Key {
		case "recovery":
			return "Current recovery signals are available for today’s context."
		case "energy":
			return "The current energy reserve is available as context for today’s pace."
		default:
			return "Current sleep context is available for today’s observation."
		}
	}
}

func containsDailyInsightID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// ApplyDailyInsightNarrative attaches only a validated domain overlay. A nil
// section deliberately preserves its deterministic fallback; valid sibling
// domains do not depend on it.
func ApplyDailyInsightNarrative(snapshot *DailyInsightSnapshot, narrative DailyInsightNarrative) (*DailyInsightSnapshot, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("daily insight snapshot is nil")
	}
	validated, _, err := ValidateDailyInsightNarrative(snapshot, narrative.Locale, narrative)
	if err != nil {
		return nil, err
	}
	out := cloneDailyInsightSnapshot(snapshot)
	byKey := make(map[string]int, len(out.Domains))
	for index, domain := range out.Domains {
		byKey[domain.Key] = index
	}
	for _, candidate := range validated.Domains {
		index, known := byKey[candidate.Key]
		if !known {
			return nil, fmt.Errorf("unknown narrative domain %q", candidate.Key)
		}
		if candidate.Section == nil {
			continue
		}
		text, claimIDs, evidenceIDs := flattenDailyInsightNarrativeSection(*candidate.Section, BuildDailyInsightNarrativeInput(snapshot, narrative.Locale), candidate.Key)
		out.Domains[index].Insight.Narrative = &DailyInsightNarrativeOverlay{Text: text, ClaimIDs: claimIDs, EvidenceIDs: evidenceIDs}
	}
	return out, nil
}

func cloneDailyInsightSnapshot(snapshot *DailyInsightSnapshot) *DailyInsightSnapshot {
	out := *snapshot
	out.Primary.EvidenceIDs = append([]string(nil), snapshot.Primary.EvidenceIDs...)
	out.Domains = append([]DailyInsightDomain(nil), snapshot.Domains...)
	for index := range out.Domains {
		out.Domains[index].Insight.EvidenceIDs = append([]string(nil), snapshot.Domains[index].Insight.EvidenceIDs...)
		if narrative := snapshot.Domains[index].Insight.Narrative; narrative != nil {
			out.Domains[index].Insight.Narrative = &DailyInsightNarrativeOverlay{Text: narrative.Text, ClaimIDs: append([]string(nil), narrative.ClaimIDs...), EvidenceIDs: append([]string(nil), narrative.EvidenceIDs...)}
		}
	}
	out.Evidence = append([]DailyInsightEvidence(nil), snapshot.Evidence...)
	out.Changes = append([]DailyInsightChange(nil), snapshot.Changes...)
	return &out
}

func flattenDailyInsightNarrativeSection(section DailyInsightNarrativeSection, input DailyInsightNarrativeInput, key string) (string, []string, []string) {
	parts, claimIDs, evidenceIDs := make([]string, 0, len(section.Sentences)), []string{}, []string{}
	for _, sentence := range section.Sentences {
		parts = append(parts, strings.TrimSpace(sentence.Text))
		for _, claimID := range sentence.ClaimIDs {
			if !containsDailyInsightID(claimIDs, claimID) {
				claimIDs = append(claimIDs, claimID)
			}
			for _, domain := range input.Domains {
				if domain.Key == key {
					for _, claim := range domain.Claims {
						if claim.ID == claimID {
							for _, evidenceID := range claim.EvidenceIDs {
								if !containsDailyInsightID(evidenceIDs, evidenceID) {
									evidenceIDs = append(evidenceIDs, evidenceID)
								}
							}
						}
					}
				}
			}
		}
	}
	return strings.Join(parts, " "), claimIDs, evidenceIDs
}

// ValidateDailyInsightNarrative is deliberately a server-side boundary, not a
// parser convenience. Structured claim/qualifier links, bounded prose and the
// prohibited-content screen make an overlay fail closed per domain. This does
// not claim to solve natural-language semantics; the deterministic fallback is
// still authoritative whenever the overlay is not clearly within its packet.
func ValidateDailyInsightNarrative(snapshot *DailyInsightSnapshot, locale string, candidate DailyInsightNarrative) (DailyInsightNarrative, map[string]string, error) {
	input := BuildDailyInsightNarrativeInput(snapshot, locale)
	invalid := make(map[string]string)
	if candidate.Version != DailyInsightNarrativeVersion {
		return DailyInsightNarrative{}, invalid, fmt.Errorf("unexpected narrative version %q", candidate.Version)
	}
	if candidate.Locale != input.Locale {
		return DailyInsightNarrative{}, invalid, fmt.Errorf("unexpected narrative locale %q", candidate.Locale)
	}
	provided := make(map[string]DailyInsightNarrativeDomain, len(candidate.Domains))
	for _, domain := range candidate.Domains {
		if _, exists := provided[domain.Key]; exists {
			return DailyInsightNarrative{}, invalid, fmt.Errorf("duplicate narrative domain %q", domain.Key)
		}
		provided[domain.Key] = domain
	}
	for _, domain := range candidate.Domains {
		if _, known := narrativeInputDomain(input, domain.Key); !known {
			return DailyInsightNarrative{}, invalid, fmt.Errorf("unknown narrative domain %q", domain.Key)
		}
	}

	out := DailyInsightNarrative{Version: DailyInsightNarrativeVersion, Locale: input.Locale, Domains: make([]DailyInsightNarrativeDomain, 0, len(input.Domains))}
	for _, expected := range input.Domains {
		candidateDomain, found := provided[expected.Key]
		if !found {
			invalid[expected.Key] = "missing from provider response"
			out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key})
			continue
		}
		if len(expected.Claims) == 0 {
			if candidateDomain.Section != nil {
				invalid[expected.Key] = "domain has no eligible claims"
			}
			out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key})
			continue
		}
		if candidateDomain.Section == nil {
			out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key})
			continue
		}
		if err := validateDailyInsightNarrativeSection(*candidateDomain.Section, expected); err != nil {
			invalid[expected.Key] = err.Error()
			out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key})
			continue
		}
		out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key, Section: cloneDailyInsightNarrativeSection(*candidateDomain.Section)})
	}
	return out, invalid, nil
}

func narrativeInputDomain(input DailyInsightNarrativeInput, key string) (DailyInsightNarrativeDomainInput, bool) {
	for _, domain := range input.Domains {
		if domain.Key == key {
			return domain, true
		}
	}
	return DailyInsightNarrativeDomainInput{}, false
}

func cloneDailyInsightNarrativeSection(section DailyInsightNarrativeSection) *DailyInsightNarrativeSection {
	out := DailyInsightNarrativeSection{Sentences: make([]DailyInsightNarrativeSentence, 0, len(section.Sentences))}
	for _, sentence := range section.Sentences {
		out.Sentences = append(out.Sentences, DailyInsightNarrativeSentence{
			Text: strings.TrimSpace(sentence.Text), ClaimIDs: append([]string(nil), sentence.ClaimIDs...), QualifierIDs: append([]string(nil), sentence.QualifierIDs...),
		})
	}
	return &out
}

func validateDailyInsightNarrativeSection(section DailyInsightNarrativeSection, input DailyInsightNarrativeDomainInput) error {
	if len(section.Sentences) == 0 || len(section.Sentences) > 2 {
		return fmt.Errorf("expected one or two sentences")
	}
	claims := make(map[string]DailyInsightNarrativeClaim, len(input.Claims))
	requiredClaims, requiredQualifiers := make(map[string]struct{}, len(input.Claims)), map[string]struct{}{}
	for _, claim := range input.Claims {
		claims[claim.ID] = claim
		requiredClaims[claim.ID] = struct{}{}
		for _, qualifierID := range claim.RequiredQualifierIDs {
			requiredQualifiers[qualifierID] = struct{}{}
		}
	}
	usedClaims, usedQualifiers := map[string]struct{}{}, map[string]struct{}{}
	wordCount := 0
	for _, sentence := range section.Sentences {
		text := strings.TrimSpace(sentence.Text)
		if text == "" {
			return fmt.Errorf("empty sentence")
		}
		wordCount += len(strings.Fields(text))
		if containsNarrativeDigit(text) {
			return fmt.Errorf("new numeric text is not allowed")
		}
		if forbidden := forbiddenNarrativeFragment(text); forbidden != "" {
			return fmt.Errorf("forbidden narrative content %q", forbidden)
		}
		if len(sentence.ClaimIDs) == 0 {
			return fmt.Errorf("sentence has no claim IDs")
		}
		for _, claimID := range sentence.ClaimIDs {
			if _, known := claims[claimID]; !known {
				return fmt.Errorf("unapproved claim ID %q", claimID)
			}
			usedClaims[claimID] = struct{}{}
		}
		for _, qualifierID := range sentence.QualifierIDs {
			if _, required := requiredQualifiers[qualifierID]; !required {
				return fmt.Errorf("unapproved qualifier ID %q", qualifierID)
			}
			usedQualifiers[qualifierID] = struct{}{}
		}
	}
	if wordCount > 45 {
		return fmt.Errorf("domain narrative exceeds 45 words")
	}
	for claimID := range requiredClaims {
		if _, used := usedClaims[claimID]; !used {
			return fmt.Errorf("missing required claim ID %q", claimID)
		}
	}
	for qualifierID := range requiredQualifiers {
		if _, used := usedQualifiers[qualifierID]; !used {
			return fmt.Errorf("missing required qualifier ID %q", qualifierID)
		}
	}
	return nil
}

func containsNarrativeDigit(text string) bool {
	for _, r := range text {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func forbiddenNarrativeFragment(text string) string {
	lower := strings.ToLower(text)
	for _, fragment := range []string{
		"diagnos", "treatment", "prescrib", "prognos", "medical", "medic", "disease",
		"you should", "you must", "need to", "avoid ", "take a ",
		"диагноз", "лечени", "прогноз", "болезн", "медицин", "лекар",
		"тебе нужно", "вам нужно", "следует ", "избегай", "избегайте", "сделай ", "сделайте ",
		"dijagnoz", "lečen", "prognoz", "bolest", "medicin", "lek ",
		"treba da", "izbeg", "uradi ", "uradite ",
	} {
		if strings.Contains(lower, fragment) {
			return fragment
		}
	}
	return ""
}

// BuildDailyInsightSnapshot converts the final briefing into a stable,
// explainable Today representation. It never promotes the technical strain
// field into a user-facing domain.
func BuildDailyInsightSnapshot(resp *BriefingResponse, lang string) *DailyInsightSnapshot {
	if resp == nil || resp.Date == "" {
		return nil
	}
	decision := resp.DailyDecision
	if decision == nil {
		decision = BuildDailyDecision(resp)
	}
	decisionID := ""
	if decision != nil {
		decisionID = decision.ID
	}
	updatedAt := (*time.Time)(nil)
	if resp.TodayGuidance != nil {
		updatedAt = resp.TodayGuidance.UpdatedAt
	}

	snapshot := &DailyInsightSnapshot{
		Date: resp.Date, DecisionID: decisionID, Version: DailyInsightSnapshotVersion, UpdatedAt: updatedAt,
		Domains: []DailyInsightDomain{}, Evidence: []DailyInsightEvidence{}, Changes: []DailyInsightChange{},
	}
	copy := dailyInsightCopy(lang)
	snapshot.Domains = []DailyInsightDomain{
		buildSleepInsightDomain(resp, updatedAt, copy),
		buildRecoveryInsightDomain(resp, updatedAt, copy),
		buildEnergyInsightDomain(resp, updatedAt, copy),
	}
	for _, domain := range snapshot.Domains {
		for _, id := range domain.Insight.EvidenceIDs {
			evidence := DailyInsightEvidence{ID: id, Domain: domain.Key, ObservedAt: updatedAt, DataState: domain.DataState, Confidence: domain.Confidence, Destination: domain.Destination}
			switch domain.Key {
			case "sleep":
				if resp.Sleep != nil && resp.Sleep.LatestTotal != nil {
					value := *resp.Sleep.LatestTotal
					evidence.Value = &value
					evidence.Unit = "hours"
					if resp.Sleep.TotalAvg > 0 {
						baseline := resp.Sleep.TotalAvg
						evidence.Baseline = &baseline
						delta := value - baseline
						evidence.Delta = &delta
					}
				}
			case "recovery":
				value := float64(resp.ReadinessToday)
				evidence.Value = &value
				evidence.Unit = "score"
			case "energy":
				if resp.EnergyBank != nil {
					value := float64(resp.EnergyBank.Current)
					evidence.Value = &value
					evidence.Unit = "percent"
				}
			}
			snapshot.Evidence = append(snapshot.Evidence, evidence)
		}
	}
	snapshot.Primary = choosePrimaryInsight(resp, decision, snapshot.Domains, copy)
	if resp.Headline != nil && resp.Headline.Key != "" && len(snapshot.Primary.EvidenceIDs) > 0 {
		headlineEvidenceIDs := make([]string, 0, len(resp.Headline.Metrics))
		for _, metric := range resp.Headline.Metrics {
			id := headlineEvidenceID(resp.Date, metric.Metric)
			headlineEvidenceIDs = append(headlineEvidenceIDs, id)
			evidence := DailyInsightEvidence{
				ID: id, Domain: "recovery", ObservedAt: updatedAt,
				ComparisonPeriod: "baseline", DataState: "fresh", Confidence: "final",
				Value: floatPtr(metric.Value), Baseline: floatPtr(metric.Baseline),
				Delta: floatPtr(metric.DeltaAbs), Unit: metric.Unit,
				Destination: DailyInsightDestination{Kind: "section", ID: "recovery"},
			}
			snapshot.Evidence = append(snapshot.Evidence, evidence)
		}
		if len(headlineEvidenceIDs) == 0 {
			id := evidenceID("headline", resp.Date)
			headlineEvidenceIDs = []string{id}
			snapshot.Evidence = append(snapshot.Evidence, DailyInsightEvidence{
				ID: id, Domain: "recovery", ObservedAt: updatedAt,
				DataState: "fresh", Confidence: "final",
				Destination: DailyInsightDestination{Kind: "section", ID: "recovery"},
			})
		}
		primaryID := snapshot.Primary.EvidenceIDs[0]
		for _, domain := range snapshot.Domains {
			if domain.Key == "recovery" && primaryID != evidenceID("recovery", resp.Date) {
				snapshot.Changes = append(snapshot.Changes, DailyInsightChange{ID: evidenceID("headline", resp.Date), Domain: "recovery", Severity: resp.Headline.Severity, Title: resp.Headline.Title, Detail: resp.Headline.Detail, EvidenceIDs: headlineEvidenceIDs, Destination: domain.Destination})
			}
		}
	}
	return snapshot
}

// DailyInsightMaterialHash excludes display timestamps and prose. It changes
// only when policy-selected evidence, states, or the bounded action changes.
func DailyInsightMaterialHash(snapshot *DailyInsightSnapshot) string {
	if snapshot == nil {
		return ""
	}
	parts := []string{
		DailyInsightPolicyVersion,
		DailyInsightActionCatalogVersion,
		snapshot.Version,
		snapshot.Date,
		snapshot.DecisionID,
		snapshot.PolicyDigest,
		snapshot.Primary.State,
		snapshot.Primary.AnswerKind,
		snapshot.Primary.ClaimID,
		snapshot.Primary.GapReason,
		snapshot.Primary.Remediation,
		snapshot.Primary.NextStepID(),
		snapshot.Primary.NextStepText(),
	}
	parts = append(parts, snapshot.Primary.EvidenceIDs...)
	for _, evidence := range snapshot.Evidence {
		parts = append(parts, evidence.ID, evidence.Domain, evidence.DataState, evidence.Confidence, evidence.ComparisonPeriod, evidence.Unit)
		if evidence.Value != nil {
			parts = append(parts, fmt.Sprintf("value=%.9g", *evidence.Value))
		}
		if evidence.Baseline != nil {
			parts = append(parts, fmt.Sprintf("baseline=%.9g", *evidence.Baseline))
		}
		if evidence.Delta != nil {
			parts = append(parts, fmt.Sprintf("delta=%.9g", *evidence.Delta))
		}
	}
	for _, domain := range snapshot.Domains {
		parts = append(parts,
			domain.Key,
			domain.Band,
			domain.NarrativeSubject,
			domain.DataState,
			domain.Confidence,
			domain.Insight.State,
			domain.Insight.AnswerKind,
			domain.Insight.ClaimID,
			domain.Insight.GapReason,
			domain.Insight.Remediation,
		)
		parts = append(parts, domain.Insight.EvidenceIDs...)
	}
	for _, change := range snapshot.Changes {
		parts = append(parts, change.ID, change.Domain, change.Severity)
		parts = append(parts, change.EvidenceIDs...)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func (i DailyInsight) NextStepID() string {
	if i.NextStep == nil {
		return ""
	}
	return i.NextStep.ID
}

func (i DailyInsight) NextStepText() string {
	if i.NextStep == nil {
		return ""
	}
	return i.NextStep.Text
}

func buildSleepInsightDomain(resp *BriefingResponse, asOf *time.Time, copy insightCopy) DailyInsightDomain {
	id := evidenceID("sleep", resp.Date)
	domain := DailyInsightDomain{Key: "sleep", Band: "unknown", DataState: "missing", AsOf: asOf, Destination: DailyInsightDestination{Kind: "sleep", ID: "sleep"}}
	if resp.Sleep == nil {
		domain.Summary = copy.sleepMissing
		observation, meaning, remediation := localizedInsightDataGuidance(copy.locale, "sleep", "missing")
		domain.Insight = DailyInsight{State: "insufficient_data", AnswerKind: DailyInsightAnswerDataGuidance, GapReason: "sleep_missing", Remediation: remediation, Title: copy.sleepTitle, Observation: observation, Meaning: meaning, EvidenceIDs: []string{id}, Fallback: true}
		return domain
	}
	if resp.SleepQuality != nil && resp.SleepQuality.ScorePct != nil {
		domain.Band = sleepQualityInsightBand(*resp.SleepQuality.ScorePct)
	}
	domain.DataState, domain.Confidence = "fresh", "final"
	if resp.Sleep.LatestDate != "" && resp.Sleep.LatestDate != resp.Date {
		domain.DataState, domain.Confidence = "stale", "low"
	} else if resp.SleepQuality != nil {
		switch resp.SleepQuality.Confidence {
		case SleepQualityConfidencePartial:
			domain.DataState, domain.Confidence = "partial", "partial"
		case SleepQualityConfidenceLow, SleepQualityConfidenceMissing:
			domain.DataState, domain.Confidence = "partial", "low"
		}
	}
	sleepSummary := copy.sleepAvailable
	if resp.Sleep.LatestTotal != nil {
		sleepSummary = localizedSleepDuration(copy, *resp.Sleep.LatestTotal)
	}
	domain.Summary = sleepSummary
	insightState := "insight"
	answerKind := DailyInsightAnswerFactual
	if domain.DataState == "stale" || domain.DataState == "partial" {
		insightState = "insufficient_data"
		answerKind = DailyInsightAnswerDataGuidance
	}
	observation := sleepInsightInterpretation(resp, copy, domain.DataState)
	meaning := copy.sleepMeaning
	gapReason, remediation := "", ""
	if answerKind == DailyInsightAnswerDataGuidance {
		gapReason = "sleep_" + domain.DataState
		observation, meaning, remediation = localizedInsightDataGuidance(copy.locale, "sleep", domain.DataState)
	} else if observation == "" {
		answerKind = DailyInsightAnswerProvisional
		observation = localizedInsightFactualContext(copy.locale, "sleep")
	}
	domain.Insight = DailyInsight{State: insightState, AnswerKind: answerKind, GapReason: gapReason, Remediation: remediation, Title: copy.sleepTitle, Observation: observation, Meaning: meaning, EvidenceIDs: []string{id}, Fallback: true}
	return domain
}

func buildRecoveryInsightDomain(resp *BriefingResponse, asOf *time.Time, copy insightCopy) DailyInsightDomain {
	id := evidenceID("recovery", resp.Date)
	state, confidence := "fresh", resp.ReadinessConfidence
	if resp.ReadinessServing != nil {
		state, confidence = resp.ReadinessServing.Status, resp.ReadinessServing.Confidence
	}
	domain := DailyInsightDomain{Key: "recovery", Band: firstNonEmptyInsight(resp.ReadinessTodayBand, resp.ReadinessBand, "unknown"), DataState: state, Confidence: confidence, AsOf: asOf, Destination: DailyInsightDestination{Kind: "section", ID: "recovery"}}
	domain.Summary = localizedReadinessFact(copy, resp.ReadinessToday, resp.ReadinessTodayLabel)
	if domain.Summary == "" {
		domain.Summary = firstInsightText(resp.ReadinessLabel, copy.recoveryMeaning)
	}
	insightState, answerKind, observation := "insight", DailyInsightAnswerFactual, firstInsightText(resp.ReadinessTip)
	meaning, gapReason, remediation := copy.recoveryMeaning, "", ""
	if !recoveryInsightHasUsableEvidence(state) {
		insightState, answerKind = "insufficient_data", DailyInsightAnswerDataGuidance
		gapReason = "recovery_" + firstNonEmptyInsight(state, "missing")
		observation, meaning, remediation = localizedInsightDataGuidance(copy.locale, "recovery", state)
	} else if observation == "" {
		answerKind = DailyInsightAnswerProvisional
		observation = localizedInsightFactualContext(copy.locale, "recovery")
	}
	domain.Insight = DailyInsight{State: insightState, AnswerKind: answerKind, GapReason: gapReason, Remediation: remediation, Title: copy.recoveryTitle, Observation: observation, Meaning: meaning, EvidenceIDs: []string{id}, Fallback: true}
	// A routine fair reading repeats the card and is deliberately not model
	// eligible. Low and optimal are distinct, final server classifications that
	// can be explained without granting the model a recommendation or a new
	// physiological claim.
	if insightState == "insight" && answerKind == DailyInsightAnswerFactual && state == ReadinessServingFresh && confidence == ReadinessConfidenceFinal && (domain.Band == "low" || domain.Band == "optimal") {
		domain.Insight.ClaimID = "recovery_readiness_context"
	}
	return domain
}

func buildEnergyInsightDomain(resp *BriefingResponse, asOf *time.Time, copy insightCopy) DailyInsightDomain {
	id := evidenceID("energy", resp.Date)
	// The current clients have no standalone Energy route. Activity is the
	// truthful existing destination because it owns the EnergyBank history;
	// this is a presentation destination, not a claim that Energy means load.
	domain := DailyInsightDomain{Key: "energy", Band: "unknown", DataState: "missing", AsOf: asOf, Destination: DailyInsightDestination{Kind: "section", ID: "activity"}}
	if resp.EnergyBank == nil {
		domain.Summary = copy.energyMissing
		observation, meaning, remediation := localizedInsightDataGuidance(copy.locale, "energy", "missing")
		domain.Insight = DailyInsight{State: "insufficient_data", AnswerKind: DailyInsightAnswerDataGuidance, GapReason: "energy_missing", Remediation: remediation, Title: copy.energyTitle, Observation: observation, Meaning: meaning, EvidenceIDs: []string{id}, Fallback: true}
		return domain
	}
	domain.Band, domain.DataState, domain.Confidence = resp.EnergyBank.Level(), "fresh", "final"
	if len(resp.EnergyBank.Flags) > 0 {
		domain.DataState, domain.Confidence = "partial", "provisional"
	}
	if resp.EnergyBank.ActionVerdict == "" || resp.EnergyBank.VerdictReason == "" {
		domain.DataState, domain.Confidence = "missing", "low"
	}
	domain.Summary = localizedEnergyFact(copy, resp.EnergyBank.Current, resp.EnergyBank.Capacity)
	if domain.Summary == "" {
		domain.Summary = firstInsightText(resp.EnergyBank.VerdictLabel, copy.energyMeaning)
	}
	answerKind, observation, meaning := DailyInsightAnswerFactual, firstInsightText(resp.EnergyBank.VerdictReason), copy.energyMeaning
	domain.Insight = DailyInsight{State: "insight", AnswerKind: answerKind, Title: copy.energyTitle, Observation: observation, Meaning: meaning, EvidenceIDs: []string{id}, Fallback: true}
	if domain.DataState == "missing" {
		domain.Insight.State = "insufficient_data"
		domain.Insight.AnswerKind = DailyInsightAnswerDataGuidance
		domain.Insight.GapReason = "energy_missing"
		domain.Insight.Observation, domain.Insight.Meaning, domain.Insight.Remediation = localizedInsightDataGuidance(copy.locale, "energy", "missing")
	} else if domain.DataState == "partial" {
		domain.Insight.AnswerKind = DailyInsightAnswerProvisional
		if domain.Insight.Observation == "" {
			domain.Insight.Observation = localizedInsightFactualContext(copy.locale, "energy")
		}
	} else if domain.Insight.Observation == "" {
		domain.Insight.AnswerKind = DailyInsightAnswerProvisional
		domain.Insight.Observation = localizedInsightFactualContext(copy.locale, "energy")
	}
	// Moderate is the ordinary-day verdict and does not justify a provider
	// paraphrase. A final non-moderate verdict is an explicit server policy
	// signal that B1 may explain, while the action itself remains server-owned.
	if domain.Insight.State == "insight" && domain.Insight.AnswerKind == DailyInsightAnswerFactual && domain.DataState == "fresh" && domain.Confidence == "final" && resp.EnergyBank.ActionVerdict != "moderate" {
		domain.Insight.ClaimID = "energy_current_verdict_context"
		domain.NarrativeSubject = resp.EnergyBank.ActionVerdict
	}
	return domain
}

func choosePrimaryInsight(resp *BriefingResponse, decision *DailyDecision, domains []DailyInsightDomain, copy insightCopy) DailyInsight {
	primary := domains[1].Insight
	primary.Title = copy.primaryTitle
	if decision == nil {
		return primary
	}
	primary.Observation = firstInsightText(decision.Reason, primary.Observation)
	primary.Meaning = copy.primaryMeaning
	primary.NextStep = &DailyInsightAction{ID: "daily-decision-" + decision.Mode, Text: decision.Label}
	for _, evidenceDomain := range decision.EvidenceDomains {
		for _, domain := range domains {
			if domain.Key == evidenceDomain && len(domain.Insight.EvidenceIDs) > 0 {
				primary.EvidenceIDs = append([]string(nil), domain.Insight.EvidenceIDs...)
				return primary
			}
		}
	}
	return primary
}

func headlineEvidenceID(date, metric string) string {
	sum := sha256.Sum256([]byte("headline\x1f" + date + "\x1f" + metric + "\x1f" + DailyInsightSnapshotVersion))
	return "headline-" + hex.EncodeToString(sum[:6])
}

func floatPtr(value float64) *float64 {
	return &value
}

func evidenceID(domain, date string) string {
	sum := sha256.Sum256([]byte(domain + "\x1f" + date + "\x1f" + DailyInsightSnapshotVersion))
	return domain + "-" + hex.EncodeToString(sum[:6])
}

func firstInsightText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// localizedInsightDataGuidance keeps an unavailable data point useful without
// turning a personal health observer into a clinical warning surface. The
// remediation is advisory and never gates the rest of the Today response.
func localizedInsightDataGuidance(locale, domain, state string) (observation, meaning, remediation string) {
	switch locale {
	case "ru":
		switch domain {
		case "sleep":
			if state == "stale" {
				return "Последняя запись сна относится не к сегодняшней ночи.", "Персональное сравнение пока не показываем; проверь синхронизацию устройства, когда будет удобно.", "sync_sleep"
			}
			if state == "partial" {
				return "Ночная запись ещё неполная, поэтому персональное сравнение пока не показываем.", "Заверши синхронизацию устройства, когда будет удобно.", "sync_sleep"
			}
			return "За сегодняшнюю ночь пока нет пригодной записи сна.", "Остальные инсайты остаются доступны; проверь синхронизацию устройства, когда будет удобно.", "sync_sleep"
		case "recovery":
			if state == ReadinessServingDataAccruing {
				return "Сигналы восстановления за сегодня ещё собираются.", "Покажем более точный контекст после следующего обновления данных.", ""
			}
			return "Сигналы восстановления за сегодня пока неполные.", "Остальные инсайты остаются доступны; проверь синхронизацию устройства, когда будет удобно.", "sync_recovery"
		default:
			return "Данные об энергии за сегодня пока неполные.", "Остальные инсайты остаются доступны; проверь синхронизацию устройства, когда будет удобно.", "sync_energy"
		}
	case "sr":
		switch domain {
		case "sleep":
			if state == "stale" {
				return "Poslednji zapis sna ne odnosi se na prethodnu noć.", "Lično poređenje zasad ne prikazujemo; proverite sinhronizaciju uređaja kada vam odgovara.", "sync_sleep"
			}
			if state == "partial" {
				return "Noćni zapis još nije potpun, pa lično poređenje zasad ne prikazujemo.", "Završite sinhronizaciju uređaja kada vam odgovara.", "sync_sleep"
			}
			return "Za prethodnu noć još nema upotrebljivog zapisa sna.", "Ostali uvidi su i dalje dostupni; proverite sinhronizaciju uređaja kada vam odgovara.", "sync_sleep"
		case "recovery":
			if state == ReadinessServingDataAccruing {
				return "Signali oporavka za danas se još prikupljaju.", "Precizniji kontekst će se pojaviti nakon sledećeg ažuriranja podataka.", ""
			}
			return "Signali oporavka za danas još nisu potpuni.", "Ostali uvidi su i dalje dostupni; proverite sinhronizaciju uređaja kada vam odgovara.", "sync_recovery"
		default:
			return "Podaci o energiji za danas još nisu potpuni.", "Ostali uvidi su i dalje dostupni; proverite sinhronizaciju uređaja kada vam odgovara.", "sync_energy"
		}
	default:
		switch domain {
		case "sleep":
			if state == "stale" {
				return "The latest sleep record is not from last night.", "A personal comparison is not shown yet; check device sync when convenient.", "sync_sleep"
			}
			if state == "partial" {
				return "The overnight record is still incomplete, so a personal comparison is not shown yet.", "Finish device sync when convenient.", "sync_sleep"
			}
			return "There is no usable sleep record for last night yet.", "The rest of Today remains available; check device sync when convenient.", "sync_sleep"
		case "recovery":
			if state == ReadinessServingDataAccruing {
				return "Today’s recovery signals are still accumulating.", "A more precise context will appear after the next data update.", ""
			}
			return "Today’s recovery signals are incomplete.", "The rest of Today remains available; check device sync when convenient.", "sync_recovery"
		default:
			return "Today’s energy data is incomplete.", "The rest of Today remains available; check device sync when convenient.", "sync_energy"
		}
	}
}

func localizedInsightFactualContext(locale, domain string) string {
	switch locale {
	case "ru":
		switch domain {
		case "sleep":
			return "Ночь учтена в сегодняшнем контексте; личное сравнение появится, когда накопится история."
		case "recovery":
			return "Доступные сигналы восстановления помогают задать спокойный темп дня."
		default:
			return "Текущий запас можно учитывать при выборе темпа на оставшуюся часть дня."
		}
	case "sr":
		switch domain {
		case "sleep":
			return "Noć je uračunata u današnji kontekst; lično poređenje će se pojaviti kada se prikupi više istorije."
		case "recovery":
			return "Dostupni signali oporavka pomažu da se odredi mirniji tempo dana."
		default:
			return "Trenutnu rezervu možete uzeti u obzir pri izboru tempa za ostatak dana."
		}
	default:
		switch domain {
		case "sleep":
			return "Last night is part of today’s context; a personal comparison will appear as more history accumulates."
		case "recovery":
			return "Available recovery signals help set a measured pace for the day."
		default:
			return "The current reserve can help choose a pace for the rest of the day."
		}
	}
}

func localizedCurrentSyncSleepContext(locale string) (observation, meaning string) {
	switch locale {
	case "ru":
		return "Ночь учтена по текущей синхронизации.", "Окончательное сравнение появится после вечерней проверки данных."
	case "sr":
		return "Noć je evidentirana prema trenutnoj sinhronizaciji.", "Konačno poređenje će se pojaviti nakon večernje provere podataka."
	default:
		return "Last night is included from the current sync.", "The final comparison will appear after this evening’s data check."
	}
}

func localizedRecentSleepBelowReference(locale string) (observation, meaning string) {
	switch locale {
	case "ru":
		return "Несколько последних ночей были короче твоего обычного ритма.", "Сегодня вечером оставь место для спокойного завершения дня."
	case "sr":
		return "Nekoliko poslednjih noći bilo je kraće od vašeg uobičajenog ritma.", "Ostavite večeras prostora za mirniji završetak dana."
	default:
		return "Several recent nights were shorter than your usual rhythm.", "Leave room for a quieter end to the day tonight."
	}
}

func localizedWindDownAction(locale string) (id, text string) {
	switch locale {
	case "ru":
		return "wind_down", "Сделать вечер тише"
	case "sr":
		return "wind_down", "Utišati veče"
	default:
		return "wind_down", "Wind down this evening"
	}
}

func localizedSleepDuration(copy insightCopy, hours float64) string {
	// The title identifies the active locale; every locale uses a separately
	// authored sentence rather than leaking English into report_lang=ru/sr.
	switch copy.sleepTitle {
	case "Сон":
		return fmt.Sprintf("Продолжительность сна — %.1f ч.", hours)
	case "San":
		return fmt.Sprintf("Trajanje sna je %.1f sati.", hours)
	default:
		return fmt.Sprintf("Sleep duration was %.1f hours.", hours)
	}
}

// sleepInsightInterpretation emits comparative context only for fresh sleep data.
func sleepInsightInterpretation(resp *BriefingResponse, copy insightCopy, dataState string) string {
	if dataState != "fresh" {
		return copy.sleepIncomplete
	}
	if resp.Sleep != nil && resp.Sleep.LatestTotal != nil && resp.Sleep.TotalAvg > 0 {
		return localizedSleepComparison(copy, *resp.Sleep.LatestTotal, resp.Sleep.TotalAvg)
	}
	if resp.SleepQuality != nil && resp.SleepQuality.ScorePct != nil && resp.SleepQuality.Confidence == SleepQualityConfidenceFinal {
		return localizedSleepQuality(copy, *resp.SleepQuality.ScorePct)
	}
	return ""
}

// localizedSleepComparison describes the latest duration against the personal baseline.
func localizedSleepComparison(copy insightCopy, latest, baseline float64) string {
	delta := latest - baseline
	if math.Abs(delta) < 0.1 {
		switch copy.locale {
		case "ru":
			return "Продолжительность сна близка к вашему среднему."
		case "sr":
			return "Trajanje sna je blizu vašeg prosjeka."
		default:
			return "Sleep duration is close to your usual average."
		}
	}
	if delta > 0 {
		switch copy.locale {
		case "ru":
			return fmt.Sprintf("На %.1f ч дольше вашего среднего.", delta)
		case "sr":
			return fmt.Sprintf("%.1f h duže od vašeg prosjeka.", delta)
		default:
			return fmt.Sprintf("%.1f hours longer than your usual average.", delta)
		}
	}
	switch copy.locale {
	case "ru":
		return fmt.Sprintf("На %.1f ч меньше вашего среднего.", -delta)
	case "sr":
		return fmt.Sprintf("%.1f h kraće od vašeg prosjeka.", -delta)
	default:
		return fmt.Sprintf("%.1f hours shorter than your usual average.", -delta)
	}
}

// localizedSleepQuality describes a final, validated sleep-quality score.
func localizedSleepQuality(copy insightCopy, score int) string {
	switch copy.locale {
	case "ru":
		return fmt.Sprintf("Качество сна %d%% — учитываем его в восстановлении.", score)
	case "sr":
		return fmt.Sprintf("Kvalitet sna je %d%% i uračunat je u oporavak.", score)
	default:
		return fmt.Sprintf("Sleep quality is %d%% and is factored into recovery.", score)
	}
}

// localizedReadinessFact formats all valid readiness scores, including zero.
func localizedReadinessFact(copy insightCopy, score int, label string) string {
	if score < 0 {
		return ""
	}
	if label == "" {
		switch copy.locale {
		case "ru":
			return fmt.Sprintf("Готовность — %d%%.", score)
		case "sr":
			return fmt.Sprintf("Spremnost je %d%%.", score)
		default:
			return fmt.Sprintf("Readiness is %d%%.", score)
		}
	}
	switch copy.locale {
	case "ru":
		return fmt.Sprintf("Готовность — %d%%: %s.", score, label)
	case "sr":
		return fmt.Sprintf("Spremnost je %d%%: %s.", score, label)
	default:
		return fmt.Sprintf("Readiness is %d%%: %s.", score, label)
	}
}

// localizedEnergyFact formats a reported energy reserve, including a depleted zero snapshot.
func localizedEnergyFact(copy insightCopy, current, capacity int) string {
	if capacity < 0 {
		return ""
	}
	switch copy.locale {
	case "ru":
		return fmt.Sprintf("Запас энергии — %d из %d.", current, capacity)
	case "sr":
		return fmt.Sprintf("Energetska rezerva je %d od %d.", current, capacity)
	default:
		return fmt.Sprintf("Energy reserve is %d of %d.", current, capacity)
	}
}

// recoveryInsightHasUsableEvidence distinguishes withheld estimates from conservative capped scores.
func recoveryInsightHasUsableEvidence(state string) bool {
	switch state {
	case ReadinessServingMissing, ReadinessServingStale, ReadinessServingDataAccruing, ReadinessServingLowCoverage:
		return false
	default:
		return true
	}
}

func sleepQualityInsightBand(score int) string {
	switch {
	case score >= 80:
		return "restorative"
	case score >= 60:
		return "good"
	case score >= 40:
		return "mixed"
	default:
		return "poor"
	}
}

func firstNonEmptyInsight(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}
