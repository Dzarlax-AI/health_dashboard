package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

// DailyInsightSnapshot is the deterministic, client-safe basis for Today.
// It deliberately contains no model output: policy chooses the domain,
// evidence, and allowed action before any narrative provider is involved.
const DailyInsightSnapshotVersion = "daily-insight-v3"

// These versions are part of the material contract. Changing policy or the
// action catalogue must invalidate a previously generated narrative even if
// the visible health values happen to be unchanged.
const (
	DailyInsightPolicyVersion        = "daily-insight-policy-v3"
	DailyInsightActionCatalogVersion = "daily-insight-actions-v3"
	DailyInsightPromptRevision       = "daily-insight-prompt-v7"
	// Bump when the provider-visible packet or its rendering contract changes.
	// The v23 packet carries server-formatted facts so the model can write one
	// useful paragraph instead of a disconnected abstract add-on.
	DailyInsightNarrativeInputVersion = "today-insight-synthesis-input-v23"
	DailyInsightNarrativeVersion      = "today-insight-synthesis-v4"
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
	// NarrativeSubject is a closed server-only variant used solely to phrase
	// the overall explanation. It never becomes client display copy.
	NarrativeSubject string `json:"-"`
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
// never turns an unknown canonical record into a health action. A confirmed
// current-evening claim exposes the same gentle server-selected next step on
// every such day; the narrower ActionEvent cadence remains separate.
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
			domain.Insight.Observation, domain.Insight.Meaning = localizedRecentSleepBelowReference(locale, claim.CurrentShortNightCount)
			domain.Insight.EvidenceIDs = []string{"sleep_recent_reference", "sleep_recent_short_nights"}
			copy.Evidence = append(copy.Evidence, recentSleepClaimEvidence(claim, domain.Destination, copy.UpdatedAt)...)
			copy.NarrativeFacts = appendOrReplaceDailyInsightNarrativeFact(copy.NarrativeFacts, DailyInsightNarrativeFact{
				ID: "sleep_recent_short_nights", Domain: "sleep", Meaning: "recent server-derived short-night pattern", Window: "last four nights", Authority: "server_derived", Fresh: true,
				Statement: localizedNarrativeRecentShortNights(locale, claim.CurrentShortNightCount), DisplayValues: []string{fmt.Sprintf("%d", claim.CurrentShortNightCount), "4"}, EvidenceIDs: []string{"sleep_recent_reference", "sleep_recent_short_nights"},
			})
			if claim.EveningActionAvailable {
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

func appendOrReplaceDailyInsightNarrativeFact(facts []DailyInsightNarrativeFact, replacement DailyInsightNarrativeFact) []DailyInsightNarrativeFact {
	for index := range facts {
		if facts[index].ID == replacement.ID {
			facts[index] = replacement
			return facts
		}
	}
	return append(facts, replacement)
}

func localizedNarrativeRecentShortNights(locale string, count int) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Коротких ночей за последние четыре: %d.", count)
	case "sr":
		return fmt.Sprintf("Kraćih noći u poslednje četiri: %d.", count)
	default:
		return fmt.Sprintf("Shorter nights in the last four: %d.", count)
	}
}

// recentSleepClaimEvidence replaces display-aggregate references for the B0
// sleep claim. A future narrative overlay can therefore acknowledge only the
// same canonical history and current window that decided the claim.
func recentSleepClaimEvidence(claim RecentSleepBelowReference, destination DailyInsightDestination, observedAt *time.Time) []DailyInsightEvidence {
	reference := claim.ReferenceHours
	shortNights := float64(claim.CurrentShortNightCount)
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
	AIInsight   *DailyInsightAIInsight  `json:"ai_insight,omitempty"`
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
	Date                    string                 `json:"date"`
	DecisionID              string                 `json:"decision_id"`
	Version                 string                 `json:"snapshot_version"`
	UpdatedAt               *time.Time             `json:"updated_at,omitempty"`
	Primary                 DailyInsight           `json:"primary"`
	AIInsight               *DailyInsightAIInsight `json:"ai_insight,omitempty"`
	Domains                 []DailyInsightDomain   `json:"domains"`
	Evidence                []DailyInsightEvidence `json:"evidence"`
	Changes                 []DailyInsightChange   `json:"changes"`
	HasMore                 bool                   `json:"has_more"`
	DecisionEvidenceDomains []string               `json:"-"`
	// PolicyDigest is server-internal cache material. It binds a future
	// narrative overlay to the exact canonical sleep records without exposing
	// an implementation hash as a user-facing fact.
	PolicyDigest string `json:"-"`
	// NarrativeFacts are privacy-minimized, server-derived aggregates prepared
	// from BriefingResponse. They deliberately never carry raw HealthKit samples,
	// sources/devices, identifiers, or EnergyBank component provenance.
	NarrativeFacts []DailyInsightNarrativeFact `json:"-"`
}

// DailyInsightNarrativeInput is retained for historical corpus readers. The
// provider-facing runtime packet is DailyInsightNarrativeSlotInput below.
type DailyInsightNarrativeInput struct {
	Version string                             `json:"version"`
	Locale  string                             `json:"locale"`
	Domains []DailyInsightNarrativeDomainInput `json:"domains"`
}

// DailyInsightNarrativeSlotInput is the exact overall-only provider payload.
// It contains privacy-minimized server-derived facts, visible B0 copy, and a
// small closed action allow-list; no raw events or domain-only prose enter it.
type DailyInsightNarrativeSlotInput struct {
	Version string                           `json:"version"`
	Locale  string                           `json:"locale"`
	Slot    DailyInsightNarrativeDomainInput `json:"slot"`
}

type DailyInsightNarrativeDomainInput struct {
	Key      string                               `json:"key"`
	Claims   []DailyInsightNarrativeClaim         `json:"-"`
	Facts    []DailyInsightNarrativeFact          `json:"facts"`
	Story    *DailyInsightNarrativeStory          `json:"-"`
	Action   *DailyInsightNarrativeAction         `json:"-"`
	Position *DailyInsightNarrativeServerPosition `json:"-"`
	// Baseline is exact already-visible B0 copy. It is anti-duplication context,
	// not evidence: it has no IDs and cannot support the generated text.
	Baseline *DailyInsightNarrativeBaseline `json:"visible_b0_baseline,omitempty"`
	// ActionOptions is a small server-owned allow-list. The provider may pick at
	// most one ID from it; absence means no action was selected.
	ActionOptions []DailyInsightNarrativeAction `json:"action_options,omitempty"`
}

// DailyInsightNarrativeFact is an already-localized, server-owned fact that
// may be paraphrased in the reader-facing paragraph. DisplayValues are the
// only numeric forms the provider may reproduce; their source values stay in
// the snapshot/evidence layer rather than becoming model-owned calculations.
type DailyInsightNarrativeFact struct {
	ID            string   `json:"id"`
	Domain        string   `json:"domain,omitempty"`
	Meaning       string   `json:"meaning,omitempty"`
	Window        string   `json:"window,omitempty"`
	Authority     string   `json:"authority,omitempty"`
	Fresh         bool     `json:"fresh,omitempty"`
	Statement     string   `json:"statement"`
	DisplayValues []string `json:"display_values,omitempty"`
	EvidenceIDs   []string `json:"evidence_ids"`
}

type DailyInsightNarrativeBaseline struct {
	Primary string                       `json:"primary"`
	Domains []DailyInsightBaselineDomain `json:"domains"`
}

type DailyInsightBaselineDomain struct {
	Domain      string `json:"domain"`
	Summary     string `json:"summary"`
	Observation string `json:"observation"`
	Meaning     string `json:"meaning"`
	NextStep    string `json:"next_step,omitempty"`
}

// DailyInsightNarrativeStory is the single relation selected by the server.
// It lets the model arrange the supplied facts into a human note without
// inventing causality, a diagnosis, or a new recommendation.
type DailyInsightNarrativeStory struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Statement  string   `json:"statement"`
	FactIDs    []string `json:"fact_ids"`
	DecisionID string   `json:"decision_id,omitempty"`
}

// DailyInsightNarrativeAction is an optional action already selected by the
// server. It is context, not a licence for the provider to add advice.
type DailyInsightNarrativeAction struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	FactIDs []string `json:"fact_ids"`
}

// DailyInsightNarrativeServerPosition is a server-owned frame for an
// independently generated domain explanation. It is present only when this
// domain was one of the explicit inputs to the final DailyDecision. The model
// may use it to choose a coherent human framing, but may not state the basis
// signals as additional claims or create a new relation between them.
type DailyInsightNarrativeServerPosition struct {
	ID                string                                      `json:"id"`
	Statement         string                                      `json:"statement"`
	SlotRole          string                                      `json:"slot_role"`
	SupportingSignals []DailyInsightNarrativeServerPositionSignal `json:"supporting_signals"`
}

// DailyInsightNarrativeServerPositionSignal gives the model compact, factual
// context for the server position. It is not separately citable prose
// material.
type DailyInsightNarrativeServerPositionSignal struct {
	Domain      string   `json:"domain"`
	Proposition string   `json:"proposition"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type DailyInsightNarrativeClaim struct {
	ID          string `json:"id"`
	Domain      string `json:"domain"`
	Kind        string `json:"kind"`
	Proposition string `json:"proposition"`
	// AnchorVariants are complete, localized factual sentences authored and
	// rendered by the server. They intentionally never enter provider JSON:
	// literal-copy requirements made otherwise useful prose fail closed when a
	// model naturally paraphrased the fact. A contract revision accompanies any
	// wording change because the values are part of the rendered experience.
	AnchorVariants       []DailyInsightNarrativeAnchorVariant `json:"-"`
	EvidenceIDs          []string                             `json:"evidence_ids"`
	ComparisonPeriod     string                               `json:"comparison_period,omitempty"`
	Confidence           string                               `json:"confidence,omitempty"`
	RequiredQualifierIDs []string                             `json:"required_qualifier_ids,omitempty"`
	MeaningLinks         []DailyInsightNarrativeMeaningLink   `json:"meaning_links,omitempty"`
}

type DailyInsightNarrativeAnchorVariant struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// DailyInsightNarrativeMeaningLink is a server-approved interpretive move for
// one closed claim. It describes a permitted interpretive angle rather than
// supplying ready-made user-facing prose. The provider selects one angle per
// section and may phrase it naturally, but cannot create a new explanation,
// causal story, or action outside this small catalogue.
type DailyInsightNarrativeMeaningLink struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	ActionID  string `json:"action_id,omitempty"`
}

// DailyInsightNarrative is the narrow provider result. Primary and actions
// intentionally have no model-owned text. A nil section is a valid request to
// keep the deterministic fallback for that domain.
type DailyInsightNarrative struct {
	Version string                        `json:"version"`
	Locale  string                        `json:"locale"`
	Overall *DailyInsightNarrativeSection `json:"overall,omitempty"`
	Domains []DailyInsightNarrativeDomain `json:"domains"`
}

type DailyInsightNarrativeSection struct {
	AnchorVariantID string                          `json:"anchor_variant_id"`
	Sentences       []DailyInsightNarrativeSentence `json:"sentences"`
	// Text/FactIDs/ActionID are the v14 overall-synthesis response. Sentences
	// remains only for decoding historical frozen-corpus artifacts.
	Text     string   `json:"text,omitempty"`
	FactIDs  []string `json:"fact_ids,omitempty"`
	ActionID string   `json:"action_id,omitempty"`
}

type DailyInsightNarrativeSentence struct {
	Text         string   `json:"text"`
	ClaimIDs     []string `json:"claim_ids"`
	QualifierIDs []string `json:"qualifier_ids"`
	MeaningIDs   []string `json:"meaning_ids"`
	PositionIDs  []string `json:"position_ids"`
}

type DailyInsightNarrativeDomain struct {
	Key     string                        `json:"key"`
	Section *DailyInsightNarrativeSection `json:"section"`
}

// DailyInsightNarrativeSlot is the provider response for exactly one
// independently generated explanation. It intentionally cannot carry any
// sibling prose.
type DailyInsightNarrativeSlot struct {
	Version string                      `json:"version"`
	Locale  string                      `json:"locale"`
	Slot    DailyInsightNarrativeDomain `json:"slot"`
}

const DailyInsightNarrativeOverallSlot = "overall"

var dailyInsightNarrativeDomainKeys = []string{"sleep", "recovery", "energy"}

var dailyInsightNarrativeSlotKeys = []string{DailyInsightNarrativeOverallSlot, "sleep", "recovery", "energy"}

// BuildDailyInsightNarrativeInput retains the pre-v14 claim shape for frozen
// corpus readers; it is not the runtime provider contract.
func BuildDailyInsightNarrativeInput(snapshot *DailyInsightSnapshot, locale string) DailyInsightNarrativeInput {
	input := DailyInsightNarrativeInput{
		Version: DailyInsightNarrativeInputVersion,
		Locale:  normalizeDailyInsightLocale(locale),
		Domains: make([]DailyInsightNarrativeDomainInput, 0, 3),
	}
	if snapshot == nil {
		return input
	}
	for _, key := range dailyInsightNarrativeDomainKeys {
		packet := DailyInsightNarrativeDomainInput{Key: key, Claims: []DailyInsightNarrativeClaim{}, Facts: []DailyInsightNarrativeFact{}}
		for _, domain := range snapshot.Domains {
			if domain.Key != key {
				continue
			}
			if domainNarrativeEligible(domain) {
				packet.Claims = append(packet.Claims, buildDailyInsightNarrativeClaim(snapshot, domain, input.Locale))
				packet.Facts = buildDailyInsightNarrativeDomainFacts(snapshot, domain, input.Locale)
				packet.Story = buildDailyInsightNarrativeStory(packet.Claims, packet.Facts, domain.Insight.NextStep, input.Locale, domain.Key)
				packet.Action = buildDailyInsightNarrativeAction(domain.Insight.NextStep, packet.Facts, input.Locale)
			}
			break
		}
		input.Domains = append(input.Domains, packet)
	}
	return input
}

// BuildDailyInsightNarrativeSlotInput derives the closed overall synthesis
// packet. The bool is false only for an unknown slot.
func BuildDailyInsightNarrativeSlotInput(snapshot *DailyInsightSnapshot, locale, slot string) (DailyInsightNarrativeSlotInput, bool) {
	input := DailyInsightNarrativeSlotInput{
		Version: DailyInsightNarrativeInputVersion,
		Locale:  normalizeDailyInsightLocale(locale),
		Slot:    DailyInsightNarrativeDomainInput{Key: slot, Claims: []DailyInsightNarrativeClaim{}, Facts: []DailyInsightNarrativeFact{}},
	}
	if !isDailyInsightNarrativeSlot(slot) {
		return DailyInsightNarrativeSlotInput{}, false
	}
	if snapshot == nil {
		return input, true
	}
	if slot == DailyInsightNarrativeOverallSlot {
		input.Slot.Facts = append(input.Slot.Facts, dailyInsightFreshNarrativeFacts(snapshot)...)
		input.Slot.Baseline = dailyInsightVisibleB0Baseline(snapshot)
		input.Slot.ActionOptions = dailyInsightNarrativeActionOptions(snapshot)
		// Retained only for historical frozen-corpus readers; it is excluded
		// from provider JSON and never establishes runtime eligibility.
		if legacyOverallNarrativeEligible(snapshot) {
			input.Slot.Claims = append(input.Slot.Claims, buildOverallDailyInsightNarrativeClaim(snapshot, input.Locale))
			if len(input.Slot.Facts) == 0 {
				// Historical corpus snapshots predate NarrativeFacts. This fallback
				// is never reached for runtime snapshots and remains excluded from
				// the v14 provider contract by their retired field identities.
				input.Slot.Facts = buildOverallDailyInsightNarrativeFacts(snapshot, input.Locale)
			}
		}
		return input, true
	}
	for _, domain := range BuildDailyInsightNarrativeInput(snapshot, input.Locale).Domains {
		if domain.Key == slot {
			input.Slot = domain
			break
		}
	}
	if len(input.Slot.Claims) > 0 && dailyInsightDecisionUsesDomain(snapshot, slot) {
		input.Slot.Position = buildDailyInsightNarrativeServerPosition(snapshot, input.Locale)
	}
	return input, true
}

func dailyInsightDecisionUsesDomain(snapshot *DailyInsightSnapshot, domain string) bool {
	if snapshot == nil {
		return false
	}
	return containsDailyInsightID(snapshot.DecisionEvidenceDomains, domain)
}

func buildDailyInsightNarrativeServerPosition(snapshot *DailyInsightSnapshot, locale string) *DailyInsightNarrativeServerPosition {
	if snapshot == nil || snapshot.Primary.NarrativeSubject == "" {
		return nil
	}
	position := &DailyInsightNarrativeServerPosition{
		ID:                "daily_decision_position",
		Statement:         localizedDailyInsightServerPosition(locale, snapshot.Primary.NarrativeSubject),
		SlotRole:          localizedDailyInsightServerPositionRole(locale),
		SupportingSignals: []DailyInsightNarrativeServerPositionSignal{},
	}
	for _, domainKey := range snapshot.DecisionEvidenceDomains {
		for _, domain := range snapshot.Domains {
			if domain.Key != domainKey || !domainNarrativeDecisionEligible(domain) {
				continue
			}
			claim := buildDailyInsightNarrativeClaim(snapshot, domain, locale)
			position.SupportingSignals = append(position.SupportingSignals, DailyInsightNarrativeServerPositionSignal{
				Domain: domain.Key, Proposition: claim.Proposition, EvidenceIDs: append([]string(nil), claim.EvidenceIDs...),
			})
			break
		}
	}
	return position
}

func localizedDailyInsightServerPosition(locale, mode string) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		switch mode {
		case "rest":
			return "На сегодня сервер выбрал режим с приоритетом восстановления."
		case "active_recovery":
			return "На сегодня сервер выбрал более бережный режим с учётом восстановления."
		case "push_hard":
			return "На сегодня сервер видит пространство для более насыщенного дня."
		default:
			return "На сегодня сервер оставил сбалансированный режим."
		}
	case "sr":
		switch mode {
		case "rest":
			return "Server je za danas izabrao režim u kome oporavak ima prednost."
		case "active_recovery":
			return "Server je za danas izabrao pažljiviji režim uz uvažavanje oporavka."
		case "push_hard":
			return "Server za danas vidi prostor za zahtevniji dan."
		default:
			return "Server je za danas zadržao uravnotežen režim."
		}
	default:
		switch mode {
		case "rest":
			return "The server has set a recovery-first position for today."
		case "active_recovery":
			return "The server has set a lighter, recovery-aware position for today."
		case "push_hard":
			return "The server sees room for a more demanding day."
		default:
			return "The server has kept a balanced position for today."
		}
	}
}

func localizedDailyInsightServerPositionRole(locale string) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return "Этот сигнал входит в основание этой позиции."
	case "sr":
		return "Ovaj signal je deo osnove za tu poziciju."
	default:
		return "This signal is part of the basis for that position."
	}
}

func isDailyInsightNarrativeSlot(slot string) bool {
	for _, key := range dailyInsightNarrativeSlotKeys {
		if key == slot {
			return true
		}
	}
	return false
}

func overallNarrativeEligible(snapshot *DailyInsightSnapshot) bool {
	return len(dailyInsightFreshNarrativeDomains(snapshot)) >= 2
}

func legacyOverallNarrativeEligible(snapshot *DailyInsightSnapshot) bool {
	if snapshot == nil || snapshot.DecisionID == "" || snapshot.Primary.State != "insight" || snapshot.Primary.Remediation != "" || len(snapshot.Primary.EvidenceIDs) == 0 {
		return false
	}
	for _, evidence := range snapshot.Evidence {
		if !containsDailyInsightID(snapshot.Primary.EvidenceIDs, evidence.ID) || evidence.DataState != "fresh" {
			continue
		}
		for _, domain := range snapshot.Domains {
			if domain.Key == evidence.Domain && domain.DataState == "fresh" {
				return true
			}
		}
	}
	return false
}

func dailyInsightFreshNarrativeFacts(snapshot *DailyInsightSnapshot) []DailyInsightNarrativeFact {
	if snapshot == nil {
		return []DailyInsightNarrativeFact{}
	}
	facts := make([]DailyInsightNarrativeFact, 0, len(snapshot.NarrativeFacts))
	for _, fact := range snapshot.NarrativeFacts {
		if fact.Fresh && fact.Domain != "" && fact.ID != "" {
			facts = append(facts, fact)
		}
	}
	return facts
}

func dailyInsightFreshNarrativeDomains(snapshot *DailyInsightSnapshot) []string {
	domains := []string{}
	for _, fact := range dailyInsightFreshNarrativeFacts(snapshot) {
		if !containsDailyInsightID(domains, fact.Domain) {
			domains = append(domains, fact.Domain)
		}
	}
	return domains
}

func dailyInsightVisibleB0Baseline(snapshot *DailyInsightSnapshot) *DailyInsightNarrativeBaseline {
	if snapshot == nil {
		return nil
	}
	baseline := &DailyInsightNarrativeBaseline{Primary: joinVisibleB0Copy(snapshot.Primary), Domains: make([]DailyInsightBaselineDomain, 0, len(snapshot.Domains))}
	for _, domain := range snapshot.Domains {
		baseline.Domains = append(baseline.Domains, DailyInsightBaselineDomain{
			Domain: domain.Key, Summary: domain.Summary, Observation: domain.Insight.Observation,
			Meaning: domain.Insight.Meaning, NextStep: domain.Insight.NextStepText(),
		})
	}
	return baseline
}

func joinVisibleB0Copy(insight DailyInsight) string {
	return strings.TrimSpace(strings.Join([]string{insight.Title, insight.Observation, insight.Meaning, insight.NextStepText()}, "\n"))
}

func dailyInsightNarrativeActionOptions(snapshot *DailyInsightSnapshot) []DailyInsightNarrativeAction {
	if snapshot == nil {
		return []DailyInsightNarrativeAction{}
	}
	options := make([]DailyInsightNarrativeAction, 0, 2)
	add := func(action *DailyInsightAction) {
		if action == nil || action.ID == "" || action.Text == "" || len(options) >= 2 {
			return
		}
		if !dailyInsightNarrativeLowRiskAction(action.ID) {
			return
		}
		for _, option := range options {
			if option.ID == action.ID {
				return
			}
		}
		options = append(options, DailyInsightNarrativeAction{ID: action.ID, Text: action.Text})
	}
	add(snapshot.Primary.NextStep)
	for _, domain := range snapshot.Domains {
		add(domain.Insight.NextStep)
	}
	return options
}

func dailyInsightNarrativeLowRiskAction(id string) bool {
	switch id {
	case "wind_down", "daily-decision-rest", "daily-decision-active_recovery", "daily-decision-moderate":
		return true
	default:
		return false
	}
}

// dailyInsightNarrativeDecisionDomains returns only the fresh domain contexts
// that the server actually used for today's primary decision. A combined
// explanation is meaningful only when at least two such contexts exist.
func dailyInsightNarrativeDecisionDomains(snapshot *DailyInsightSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	domains := make([]string, 0, len(snapshot.DecisionEvidenceDomains))
	for _, key := range dailyInsightNarrativeDomainKeys {
		if !containsDailyInsightID(snapshot.DecisionEvidenceDomains, key) {
			continue
		}
		for _, domain := range snapshot.Domains {
			if domain.Key == key && domainNarrativeDecisionEligible(domain) {
				domains = append(domains, key)
				break
			}
		}
	}
	return domains
}

func buildOverallDailyInsightNarrativeClaim(snapshot *DailyInsightSnapshot, locale string) DailyInsightNarrativeClaim {
	return DailyInsightNarrativeClaim{
		ID:                   "overall_daily_decision_context",
		Domain:               DailyInsightNarrativeOverallSlot,
		Kind:                 "daily_decision",
		Proposition:          localizedOverallNarrativeProposition(locale, snapshot.Primary.NarrativeSubject),
		AnchorVariants:       localizedOverallNarrativeAnchors(locale, snapshot.Primary.NarrativeSubject),
		EvidenceIDs:          append([]string(nil), snapshot.Primary.EvidenceIDs...),
		ComparisonPeriod:     "current day",
		Confidence:           snapshot.Primary.AnswerKind,
		RequiredQualifierIDs: []string{"current_context"},
		MeaningLinks:         overallNarrativeMeaningLinks(locale, dailyInsightNarrativeDecisionDomains(snapshot)),
	}
}

func localizedOverallNarrativeProposition(locale, mode string) string {
	switch locale {
	case "ru":
		switch mode {
		case "rest":
			return "На сегодня выбран режим отдыха."
		case "active_recovery":
			return "На сегодня выбран режим активного восстановления."
		case "push_hard":
			return "На сегодня выбран режим более высокой нагрузки."
		default:
			return "На сегодня выбран умеренный режим."
		}
	case "sr":
		switch mode {
		case "rest":
			return "Za danas je izabran režim odmora."
		case "active_recovery":
			return "Za danas je izabran režim aktivnog oporavka."
		case "push_hard":
			return "Za danas je izabran režim većeg opterećenja."
		default:
			return "Za danas je izabran umeren režim."
		}
	default:
		switch mode {
		case "rest":
			return "Today is set to a rest-oriented pace."
		case "active_recovery":
			return "Today is set to an active-recovery pace."
		case "push_hard":
			return "Today is set to a higher-load pace."
		default:
			return "Today is set to a moderate pace."
		}
	}
}

func localizedOverallNarrativeAnchors(locale, mode string) []DailyInsightNarrativeAnchorVariant {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		switch mode {
		case "rest":
			return []DailyInsightNarrativeAnchorVariant{{ID: "rest", Text: "На сегодня выбран режим отдыха."}}
		case "active_recovery":
			return []DailyInsightNarrativeAnchorVariant{{ID: "active-recovery", Text: "На сегодня выбран режим активного восстановления."}}
		case "push_hard":
			return []DailyInsightNarrativeAnchorVariant{{ID: "push-hard", Text: "На сегодня выбран режим более высокой нагрузки."}}
		default:
			return []DailyInsightNarrativeAnchorVariant{{ID: "moderate", Text: "На сегодня выбран умеренный режим."}}
		}
	case "sr":
		switch mode {
		case "rest":
			return []DailyInsightNarrativeAnchorVariant{{ID: "rest", Text: "Za danas je izabran režim odmora."}}
		case "active_recovery":
			return []DailyInsightNarrativeAnchorVariant{{ID: "active-recovery", Text: "Za danas je izabran režim aktivnog oporavka."}}
		case "push_hard":
			return []DailyInsightNarrativeAnchorVariant{{ID: "push-hard", Text: "Za danas je izabran režim većeg opterećenja."}}
		default:
			return []DailyInsightNarrativeAnchorVariant{{ID: "moderate", Text: "Za danas je izabran umeren režim."}}
		}
	default:
		switch mode {
		case "rest":
			return []DailyInsightNarrativeAnchorVariant{{ID: "rest", Text: "Today is set to a rest-oriented pace."}}
		case "active_recovery":
			return []DailyInsightNarrativeAnchorVariant{{ID: "active-recovery", Text: "Today is set to an active-recovery pace."}}
		case "push_hard":
			return []DailyInsightNarrativeAnchorVariant{{ID: "push-hard", Text: "Today is set to a higher-load pace."}}
		default:
			return []DailyInsightNarrativeAnchorVariant{{ID: "moderate", Text: "Today is set to a moderate pace."}}
		}
	}
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
	if !domainNarrativeBaseEligible(domain) {
		return false
	}
	switch domain.Insight.ClaimID {
	case "recent_sleep_below_reference":
	default:
		return false
	}
	return true
}

// domainNarrativeDecisionEligible is intentionally broader than the
// independent-domain gate. A recovery classification or Energy verdict can
// support a server-owned combined recommendation, but neither alone
// establishes a new relationship for a model to explain. Keeping that
// distinction avoids spending a provider call merely to restate a visible
// score or card.
func domainNarrativeDecisionEligible(domain DailyInsightDomain) bool {
	if !domainNarrativeBaseEligible(domain) {
		return false
	}
	switch domain.Insight.ClaimID {
	case "recent_sleep_below_reference", "recovery_readiness_context", "energy_current_verdict_context":
		return true
	default:
		return false
	}
}

func domainNarrativeBaseEligible(domain DailyInsightDomain) bool {
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

// HasEligibleDailyInsightNarrativeClaims reports whether the overall-only B1
// packet contains fresh server-derived facts from at least two domains.
func HasEligibleDailyInsightNarrativeClaims(snapshot *DailyInsightSnapshot, locale string) bool {
	for _, slot := range dailyInsightNarrativeSlotKeys {
		if HasEligibleDailyInsightNarrativeSlot(snapshot, locale, slot) {
			return true
		}
	}
	return false
}

// HasEligibleDailyInsightNarrativeSlot is overall-only at runtime. Standalone
// sleep, recovery, and energy prose remains disabled.
func HasEligibleDailyInsightNarrativeSlot(snapshot *DailyInsightSnapshot, locale, slot string) bool {
	// The rich-sleep experiment showed that its only currently server-approved
	// relation restates the deterministic B0 pattern and the separately rendered
	// wind-down action. Preserve the packet shape for audit and validation of
	// historical artifacts, but do not spend a provider call until a future
	// sleep-specific relation adds distinct server-owned meaning. B1 serving is
	// therefore currently limited to an overall, genuinely combined explanation.
	if slot != DailyInsightNarrativeOverallSlot {
		return false
	}
	// Legacy claims remain decodable for frozen historical corpus artifacts,
	// but cannot schedule or serve a current B1 provider request.
	return overallNarrativeEligible(snapshot)
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
		claim.AnchorVariants = localizedSleepNarrativeAnchors(locale)
		claim.MeaningLinks = sleepNarrativeMeaningLinks(locale)
		return claim
	}
	if claim.ID == "recovery_readiness_context" {
		claim.Proposition = localizedRecoveryNarrativeProposition(locale, domain.Band)
		claim.AnchorVariants = localizedRecoveryNarrativeAnchors(locale, domain.Band)
		claim.MeaningLinks = recoveryNarrativeMeaningLinks(locale, domain.Band)
		return claim
	}
	if claim.ID == "energy_current_verdict_context" {
		claim.Proposition = localizedEnergyNarrativeProposition(locale, domain.NarrativeSubject)
		claim.AnchorVariants = localizedEnergyNarrativeAnchors(locale, domain.NarrativeSubject)
		claim.MeaningLinks = energyNarrativeMeaningLinks(locale, domain.NarrativeSubject)
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

func buildDailyInsightNarrativeDomainFacts(snapshot *DailyInsightSnapshot, domain DailyInsightDomain, locale string) []DailyInsightNarrativeFact {
	facts := []DailyInsightNarrativeFact{}
	add := func(id, statement string, evidenceIDs []string) {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			return
		}
		facts = append(facts, DailyInsightNarrativeFact{
			ID: id, Statement: statement, DisplayValues: narrativeDisplayValues(statement), EvidenceIDs: append([]string(nil), evidenceIDs...),
		})
	}
	add(domain.Key+"_summary", domain.Summary, domain.Insight.EvidenceIDs)
	add(domain.Key+"_assessment", domain.Insight.Observation, domain.Insight.EvidenceIDs)
	for _, evidenceID := range domain.Insight.EvidenceIDs {
		for _, evidence := range snapshot.Evidence {
			if evidence.ID != evidenceID {
				continue
			}
			statement := localizedNarrativeEvidenceStatement(locale, evidence)
			if statement == "" {
				continue
			}
			values := narrativeDisplayValues(statement)
			facts = append(facts, DailyInsightNarrativeFact{ID: evidence.ID, Statement: statement, DisplayValues: values, EvidenceIDs: []string{evidence.ID}})
		}
	}
	return facts
}

func buildOverallDailyInsightNarrativeFacts(snapshot *DailyInsightSnapshot, locale string) []DailyInsightNarrativeFact {
	facts := []DailyInsightNarrativeFact{}
	for _, key := range dailyInsightNarrativeDecisionDomains(snapshot) {
		for _, domain := range snapshot.Domains {
			if domain.Key != key {
				continue
			}
			facts = append(facts, buildDailyInsightNarrativeDomainFacts(snapshot, domain, locale)...)
			break
		}
	}
	if action := snapshot.Primary.NextStep; action != nil && strings.TrimSpace(action.Text) != "" {
		facts = append(facts, DailyInsightNarrativeFact{
			ID: "daily_decision", Statement: action.Text, DisplayValues: narrativeDisplayValues(action.Text), EvidenceIDs: append([]string(nil), snapshot.Primary.EvidenceIDs...),
		})
	}
	return facts
}

func buildDailyInsightNarrativeStory(claims []DailyInsightNarrativeClaim, facts []DailyInsightNarrativeFact, action *DailyInsightAction, locale, slot string) *DailyInsightNarrativeStory {
	if len(claims) != 1 || len(claims[0].MeaningLinks) != 1 || len(facts) == 0 {
		return nil
	}
	meaning := claims[0].MeaningLinks[0]
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		factIDs = append(factIDs, fact.ID)
	}
	story := &DailyInsightNarrativeStory{ID: meaning.ID, Kind: slot + "_context", Statement: meaning.Statement, FactIDs: factIDs}
	if action != nil {
		story.DecisionID = action.ID
	}
	return story
}

func buildDailyInsightNarrativeAction(action *DailyInsightAction, facts []DailyInsightNarrativeFact, locale string) *DailyInsightNarrativeAction {
	if action == nil || strings.TrimSpace(action.ID) == "" {
		return nil
	}
	text := localizedDailyInsightNarrativeAction(locale, action.ID)
	if text == "" {
		text = action.Text
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		factIDs = append(factIDs, fact.ID)
	}
	return &DailyInsightNarrativeAction{ID: action.ID, Text: text, FactIDs: factIDs}
}

// localizedDailyInsightNarrativeAction is server-owned copy for a model
// packet, separate from the compact UI label in DailyInsightAction.Text. It
// is intentionally defined only for closed action IDs; the provider may make
// it sound natural, but cannot add another suggestion or promise an outcome.
func localizedDailyInsightNarrativeAction(locale, actionID string) string {
	if actionID != "wind_down" {
		return ""
	}
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return "Сегодня вечером оставь себе спокойный час без задач и начни сворачиваться раньше."
	case "sr":
		return "Večeras ostavi sebi miran sat bez obaveza i počni da se smiruješ ranije."
	default:
		return "Leave yourself a calm hour without extra tasks tonight and start winding down earlier."
	}
}

func localizedNarrativeEvidenceStatement(locale string, evidence DailyInsightEvidence) string {
	if evidence.Value == nil {
		return ""
	}
	value := *evidence.Value
	if evidence.ID == "sleep_recent_reference" && (evidence.Unit == "h" || evidence.Unit == "hours") {
		hours := int(value)
		minutes := int(math.Round((value - float64(hours)) * 60))
		if minutes == 60 {
			hours, minutes = hours+1, 0
		}
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Твой обычный сон: %d ч %d мин.", hours, minutes)
		case "sr":
			return fmt.Sprintf("Tvoj uobičajeni san: %d h %d min.", hours, minutes)
		default:
			return fmt.Sprintf("Your usual sleep: %d h %d min.", hours, minutes)
		}
	}
	if evidence.ID == "sleep_recent_short_nights" && evidence.Unit == "nights" {
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Коротких ночей за последние четыре: %.0f.", value)
		case "sr":
			return fmt.Sprintf("Kraćih noći u poslednje četiri: %.0f.", value)
		default:
			return fmt.Sprintf("Shorter nights in the last four: %.0f.", value)
		}
	}
	switch evidence.Unit {
	case "h", "hours":
		hours := int(value)
		minutes := int(math.Round((value - float64(hours)) * 60))
		if minutes == 60 {
			hours, minutes = hours+1, 0
		}
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Длительность сна: %d ч %d мин.", hours, minutes)
		case "sr":
			return fmt.Sprintf("Trajanje sna: %d h %d min.", hours, minutes)
		default:
			return fmt.Sprintf("Sleep duration: %d h %d min.", hours, minutes)
		}
	case "percent", "score":
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Текущее значение: %.0f%%.", value)
		case "sr":
			return fmt.Sprintf("Trenutna vrednost: %.0f%%.", value)
		default:
			return fmt.Sprintf("Current value: %.0f%%.", value)
		}
	case "nights":
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Более коротких последних ночей: %.0f.", value)
		case "sr":
			return fmt.Sprintf("Skorijih kraćih noći: %.0f.", value)
		default:
			return fmt.Sprintf("Recent shorter nights: %.0f.", value)
		}
	default:
		return fmt.Sprintf("%.2f", value)
	}
}

// narrativeDisplayValues returns the numeric forms a provider may echo. The
// packet keeps the surrounding localized statement, while this compact list
// lets the validator reject an invented literal without banning useful values.
func narrativeDisplayValues(text string) []string {
	values := []string{}
	for _, token := range narrativeNumericTokens(text) {
		if !containsDailyInsightID(values, token) {
			values = append(values, token)
		}
	}
	return values
}

func narrativeNumericTokens(text string) []string {
	values := []string{}
	runes := []rune(text)
	for index := 0; index < len(runes); {
		if !unicode.IsDigit(runes[index]) {
			index++
			continue
		}
		var current strings.Builder
		current.WriteRune(runes[index])
		index++
		for index < len(runes) {
			r := runes[index]
			if unicode.IsDigit(r) {
				current.WriteRune(r)
				index++
				continue
			}
			if (r == '.' || r == ',') && index+1 < len(runes) && unicode.IsDigit(runes[index+1]) {
				current.WriteRune(r)
				index++
				continue
			}
			if isNarrativeGroupingSpace(r) && index+1 < len(runes) && unicode.IsDigit(runes[index+1]) {
				current.WriteRune(r)
				index++
				continue
			}
			break
		}
		values = append(values, current.String())
	}
	return values
}

func overallNarrativeMeaningLinks(locale string, domains []string) []DailyInsightNarrativeMeaningLink {
	if len(domains) < 2 {
		return nil
	}
	return []DailyInsightNarrativeMeaningLink{{
		ID:        "overall_combined_context",
		Statement: localizedOverallNarrativeMeaning(locale, domains),
	}}
}

func sleepNarrativeMeaningLinks(locale string) []DailyInsightNarrativeMeaningLink {
	return []DailyInsightNarrativeMeaningLink{{
		ID: "sleep_personal_reference", Statement: localizedNarrativeMeaning(locale, "sleep_personal_reference"),
	}}
}

func recoveryNarrativeMeaningLinks(locale, band string) []DailyInsightNarrativeMeaningLink {
	return []DailyInsightNarrativeMeaningLink{{
		ID: "recovery_day_to_day_effect", Statement: localizedRecoveryNarrativeMeaning(locale, band),
	}}
}

func energyNarrativeMeaningLinks(locale, verdict string) []DailyInsightNarrativeMeaningLink {
	if verdict == "active_recovery" {
		return nil
	}
	return []DailyInsightNarrativeMeaningLink{{
		ID: "energy_day_to_day_effect", Statement: localizedEnergyNarrativeMeaning(locale, verdict),
	}}
}

func localizedNarrativeMeaning(locale, id string) string {
	translations := map[string]map[string]string{
		"en": {
			"sleep_personal_reference": "Several shorter nights in the latest four make a calmer end to today more fitting.",
		},
		"ru": {
			"sleep_personal_reference": "Несколько коротких ночей за последние четыре — повод сделать сегодняшний вечер спокойнее.",
		},
		"sr": {
			"sleep_personal_reference": "Nekoliko kraćih noći u poslednje četiri čini mirniji kraj dana boljim izborom.",
		},
	}
	if byID, found := translations[normalizeDailyInsightLocale(locale)]; found {
		return byID[id]
	}
	return translations["en"][id]
}

func localizedOverallNarrativeMeaning(locale string, domains []string) string {
	labels := make([]string, 0, len(domains))
	for _, domain := range domains {
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			switch domain {
			case "sleep":
				labels = append(labels, "сна")
			case "recovery":
				labels = append(labels, "восстановления")
			case "energy":
				labels = append(labels, "энергии")
			}
		case "sr":
			switch domain {
			case "sleep":
				labels = append(labels, "san")
			case "recovery":
				labels = append(labels, "oporavak")
			case "energy":
				labels = append(labels, "energiju")
			}
		default:
			labels = append(labels, domain)
		}
	}
	joined := strings.Join(labels, ", ")
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return "Сегодняшняя картина складывается из " + joined + "."
	case "sr":
		return "Današnja slika obuhvata " + joined + "."
	default:
		return "Today's picture brings together " + joined + "."
	}
}

func localizedRecoveryNarrativeMeaning(locale, band string) string {
	higher := band == "optimal"
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		if higher {
			return "Восстановление сегодня даёт устойчивую опору для общей картины дня."
		}
		return "Восстановление сегодня — заметная часть общей картины дня."
	case "sr":
		if higher {
			return "Oporavak danas daje stabilniji oslonac celoj slici dana."
		}
		return "Oporavak je danas primetan deo cele slike dana."
	default:
		if higher {
			return "Recovery gives today’s overall picture a steadier foundation."
		}
		return "Recovery is a noticeable part of today’s overall picture."
	}
}

func localizedEnergyNarrativeMeaning(locale, verdict string) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		switch verdict {
		case "push_hard":
			return "Энергия сегодня даёт больше свободы в выборе темпа дня."
		case "rest":
			return "Энергия сегодня — ресурс, который стоит распределить по дню."
		default:
			return "Энергия сегодня задаёт более спокойный ритм дня."
		}
	case "sr":
		switch verdict {
		case "push_hard":
			return "Energija danas daje više slobode pri izboru ritma dana."
		case "rest":
			return "Energija je danas resurs koji vredi rasporediti kroz dan."
		default:
			return "Energija danas postavlja mirniji ritam dana."
		}
	default:
		switch verdict {
		case "push_hard":
			return "Energy gives you more room to choose the day’s pace."
		case "rest":
			return "Energy is a resource to spread across the day."
		default:
			return "Energy sets a calmer rhythm for the day."
		}
	}
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

func localizedSleepNarrativeAnchors(locale string) []DailyInsightNarrativeAnchorVariant {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return []DailyInsightNarrativeAnchorVariant{{ID: "recent-shorter-than-usual", Text: "Несколько последних ночей ты спал меньше обычного для тебя."}}
	case "sr":
		return []DailyInsightNarrativeAnchorVariant{{ID: "recent-shorter-than-usual", Text: "Nekoliko poslednjih noći spavao si manje nego što je za tebe uobičajeno."}}
	default:
		return []DailyInsightNarrativeAnchorVariant{{ID: "recent-shorter-than-usual", Text: "Several recent nights were shorter than usual for you."}}
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
			return "Сегодня восстановление на хорошем уровне."
		}
		return "Сегодня восстановление не на пике."
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

func localizedRecoveryNarrativeAnchors(locale, band string) []DailyInsightNarrativeAnchorVariant {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		if band == "optimal" {
			return []DailyInsightNarrativeAnchorVariant{{ID: "recovery-strong", Text: "Сегодня восстановление на хорошем уровне."}}
		}
		return []DailyInsightNarrativeAnchorVariant{{ID: "recovery-lower", Text: "Сегодня восстановление не на пике."}}
	case "sr":
		if band == "optimal" {
			return []DailyInsightNarrativeAnchorVariant{{ID: "recovery-strong", Text: "Danas su signali oporavka u višem opsegu spremnosti."}}
		}
		return []DailyInsightNarrativeAnchorVariant{{ID: "recovery-lower", Text: "Danas su signali oporavka u nižem opsegu spremnosti."}}
	default:
		if band == "optimal" {
			return []DailyInsightNarrativeAnchorVariant{{ID: "recovery-strong", Text: "Today's recovery signals sit in the higher readiness band."}}
		}
		return []DailyInsightNarrativeAnchorVariant{{ID: "recovery-lower", Text: "Today's recovery signals sit in the lower readiness band."}}
	}
}

func localizedEnergyNarrativeProposition(locale, verdict string) string {
	switch locale {
	case "ru":
		switch verdict {
		case "push_hard":
			return "Сил сегодня достаточно."
		case "rest":
			return "Сегодня сил немного."
		default:
			return "Сегодня лучше не добавлять интенсивности."
		}
	case "sr":
		switch verdict {
		case "push_hard":
			return "Trenutna rezerva energije je u opsegu većeg kapaciteta."
		case "rest":
			return "Trenutna rezerva energije je u opsegu nižeg kapaciteta."
		default:
			return "Danas je bolje ne dodavati intenzitet."
		}
	default:
		switch verdict {
		case "push_hard":
			return "The current energy context is in a higher-capacity range."
		case "rest":
			return "The current energy context is in a lower-capacity range."
		default:
			return "Today is better suited to avoiding extra intensity."
		}
	}
}

func localizedEnergyNarrativeAnchors(locale, verdict string) []DailyInsightNarrativeAnchorVariant {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		switch verdict {
		case "push_hard":
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-available", Text: "Сил сегодня достаточно."}}
		case "rest":
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-lower", Text: "Сегодня сил немного."}}
		default:
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-no-extra-intensity", Text: "Сегодня лучше не добавлять интенсивности."}}
		}
	case "sr":
		switch verdict {
		case "push_hard":
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-available", Text: "Današnja rezerva energije je u opsegu većeg kapaciteta."}}
		case "rest":
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-lower", Text: "Današnja rezerva energije je u opsegu nižeg kapaciteta."}}
		default:
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-no-extra-intensity", Text: "Danas je bolje ne dodavati intenzitet."}}
		}
	default:
		switch verdict {
		case "push_hard":
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-available", Text: "The current energy context is in a higher-capacity range."}}
		case "rest":
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-lower", Text: "The current energy context is in a lower-capacity range."}}
		default:
			return []DailyInsightNarrativeAnchorVariant{{ID: "energy-no-extra-intensity", Text: "Today is better suited to avoiding extra intensity."}}
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

// ApplyDailyInsightNarrative attaches only validated overlays. A nil section
// deliberately preserves its deterministic fallback; valid sibling slots do
// not depend on it.
func ApplyDailyInsightNarrative(snapshot *DailyInsightSnapshot, narrative DailyInsightNarrative) (*DailyInsightSnapshot, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("daily insight snapshot is nil")
	}
	validated, _, err := ValidateDailyInsightNarrative(snapshot, narrative.Locale, narrative)
	if err != nil {
		return nil, err
	}
	out := cloneDailyInsightSnapshot(snapshot)
	if validated.Overall != nil {
		input, _ := BuildDailyInsightNarrativeSlotInput(snapshot, narrative.Locale, DailyInsightNarrativeOverallSlot)
		text, claimIDs, evidenceIDs := flattenDailyInsightNarrativeSection(*validated.Overall, input.Slot)
		out.Primary.Narrative = &DailyInsightNarrativeOverlay{Text: text, ClaimIDs: claimIDs, EvidenceIDs: evidenceIDs}
	}
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
		input, _ := BuildDailyInsightNarrativeSlotInput(snapshot, narrative.Locale, candidate.Key)
		text, claimIDs, evidenceIDs := flattenDailyInsightNarrativeSection(*candidate.Section, input.Slot)
		out.Domains[index].Insight.Narrative = &DailyInsightNarrativeOverlay{Text: text, ClaimIDs: claimIDs, EvidenceIDs: evidenceIDs}
	}
	return out, nil
}

func cloneDailyInsightSnapshot(snapshot *DailyInsightSnapshot) *DailyInsightSnapshot {
	out := *snapshot
	if insight := snapshot.AIInsight; insight != nil {
		copy := *insight
		copy.FactIDs = append([]string(nil), insight.FactIDs...)
		copy.EvidenceIDs = append([]string(nil), insight.EvidenceIDs...)
		out.AIInsight = &copy
	}
	out.DecisionEvidenceDomains = append([]string(nil), snapshot.DecisionEvidenceDomains...)
	out.Primary.EvidenceIDs = append([]string(nil), snapshot.Primary.EvidenceIDs...)
	if narrative := snapshot.Primary.Narrative; narrative != nil {
		out.Primary.Narrative = &DailyInsightNarrativeOverlay{Text: narrative.Text, ClaimIDs: append([]string(nil), narrative.ClaimIDs...), EvidenceIDs: append([]string(nil), narrative.EvidenceIDs...)}
	}
	out.Domains = append([]DailyInsightDomain(nil), snapshot.Domains...)
	for index := range out.Domains {
		if insight := snapshot.Domains[index].AIInsight; insight != nil {
			copy := *insight
			copy.FactIDs = append([]string(nil), insight.FactIDs...)
			copy.EvidenceIDs = append([]string(nil), insight.EvidenceIDs...)
			out.Domains[index].AIInsight = &copy
		}
		out.Domains[index].Insight.EvidenceIDs = append([]string(nil), snapshot.Domains[index].Insight.EvidenceIDs...)
		if narrative := snapshot.Domains[index].Insight.Narrative; narrative != nil {
			out.Domains[index].Insight.Narrative = &DailyInsightNarrativeOverlay{Text: narrative.Text, ClaimIDs: append([]string(nil), narrative.ClaimIDs...), EvidenceIDs: append([]string(nil), narrative.EvidenceIDs...)}
		}
	}
	out.Evidence = append([]DailyInsightEvidence(nil), snapshot.Evidence...)
	out.Changes = append([]DailyInsightChange(nil), snapshot.Changes...)
	out.NarrativeFacts = append([]DailyInsightNarrativeFact(nil), snapshot.NarrativeFacts...)
	for index := range out.NarrativeFacts {
		out.NarrativeFacts[index].DisplayValues = append([]string(nil), snapshot.NarrativeFacts[index].DisplayValues...)
		out.NarrativeFacts[index].EvidenceIDs = append([]string(nil), snapshot.NarrativeFacts[index].EvidenceIDs...)
	}
	return &out
}

func flattenDailyInsightNarrativeSection(section DailyInsightNarrativeSection, input DailyInsightNarrativeDomainInput) (string, []string, []string) {
	if text := strings.TrimSpace(section.Text); text != "" {
		factIDs := append([]string(nil), section.FactIDs...)
		evidenceIDs := make([]string, 0, len(factIDs))
		for _, factID := range factIDs {
			for _, fact := range input.Facts {
				if fact.ID != factID {
					continue
				}
				for _, evidenceID := range fact.EvidenceIDs {
					if !containsDailyInsightID(evidenceIDs, evidenceID) {
						evidenceIDs = append(evidenceIDs, evidenceID)
					}
				}
			}
		}
		return text, factIDs, evidenceIDs
	}
	parts, claimIDs, evidenceIDs := make([]string, 0, len(section.Sentences)), []string{}, []string{}
	for _, sentence := range section.Sentences {
		parts = append(parts, strings.TrimSpace(sentence.Text))
		for _, claimID := range sentence.ClaimIDs {
			if !containsDailyInsightID(claimIDs, claimID) {
				claimIDs = append(claimIDs, claimID)
			}
			for _, claim := range input.Claims {
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
	if candidate.Locale != normalizeDailyInsightLocale(locale) {
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
	overallInput, _ := BuildDailyInsightNarrativeSlotInput(snapshot, input.Locale, DailyInsightNarrativeOverallSlot)
	if candidate.Overall != nil {
		var (
			section *DailyInsightNarrativeSection
			err     error
		)
		switch {
		case overallNarrativeEligible(snapshot):
			section, err = validateDailyInsightHumanSynthesis(candidate.Overall, overallInput.Slot, input.Locale)
		case legacyOverallNarrativeEligible(snapshot):
			// Frozen corpus artifacts may still use the pre-fact claim wrapper.
			section, err = normalizeDailyInsightNarrativeSection(*candidate.Overall, overallInput.Slot, input.Locale)
		default:
			err = fmt.Errorf("overall synthesis has fewer than two fresh domains")
		}
		if err != nil {
			invalid[DailyInsightNarrativeOverallSlot] = err.Error()
		} else {
			out.Overall = section
		}
	}
	for _, expected := range input.Domains {
		// A combined narrative can still contain independently generated slot
		// sections. Rebuild the slot packet here rather than validating against
		// the legacy combined input: only the slot packet carries the closed
		// server_position that a section must cite. Dropping it makes an already
		// accepted independent response fail a later quality-gate revalidation.
		slotInput, _ := BuildDailyInsightNarrativeSlotInput(snapshot, input.Locale, expected.Key)
		expected = slotInput.Slot
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
		section, err := normalizeDailyInsightNarrativeSection(*candidateDomain.Section, expected, input.Locale)
		if err != nil {
			invalid[expected.Key] = err.Error()
			out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key})
			continue
		}
		out.Domains = append(out.Domains, DailyInsightNarrativeDomain{Key: expected.Key, Section: section})
	}
	return out, invalid, nil
}

// ValidateDailyInsightNarrativeSlot validates a provider response against one
// independently cached claim packet. It intentionally rejects sibling text so
// one slot cannot overwrite or delay another slot’s explanation.
func ValidateDailyInsightNarrativeSlot(snapshot *DailyInsightSnapshot, locale, slot string, candidate DailyInsightNarrative) (DailyInsightNarrative, error) {
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, locale, slot)
	if !known {
		return DailyInsightNarrative{}, fmt.Errorf("unknown narrative slot %q", slot)
	}
	if candidate.Version != DailyInsightNarrativeVersion {
		return DailyInsightNarrative{}, fmt.Errorf("unexpected narrative version %q", candidate.Version)
	}
	if candidate.Locale != input.Locale {
		return DailyInsightNarrative{}, fmt.Errorf("unexpected narrative locale %q", candidate.Locale)
	}
	var section *DailyInsightNarrativeSection
	if slot == DailyInsightNarrativeOverallSlot {
		if len(candidate.Domains) != 0 {
			return DailyInsightNarrative{}, fmt.Errorf("overall response contains domain text")
		}
		section = candidate.Overall
	} else {
		if candidate.Overall != nil || len(candidate.Domains) != 1 || candidate.Domains[0].Key != slot {
			return DailyInsightNarrative{}, fmt.Errorf("response does not contain exactly slot %q", slot)
		}
		section = candidate.Domains[0].Section
	}
	out := DailyInsightNarrative{Version: DailyInsightNarrativeVersion, Locale: input.Locale}
	if slot == DailyInsightNarrativeOverallSlot {
		if !overallNarrativeEligible(snapshot) && !legacyOverallNarrativeEligible(snapshot) {
			if section != nil {
				return DailyInsightNarrative{}, fmt.Errorf("overall synthesis has fewer than two fresh domains")
			}
			return out, nil
		}
		if section != nil {
			var err error
			if overallNarrativeEligible(snapshot) {
				section, err = validateDailyInsightHumanSynthesis(section, input.Slot, locale)
			} else {
				section, err = normalizeDailyInsightNarrativeSection(*section, input.Slot, locale)
			}
			if err != nil {
				return DailyInsightNarrative{}, err
			}
		}
		out.Overall = section
		return out, nil
	}
	if len(input.Slot.Claims) == 0 {
		if section != nil {
			return DailyInsightNarrative{}, fmt.Errorf("slot %q has no eligible claims", slot)
		}
		if slot != DailyInsightNarrativeOverallSlot {
			out.Domains = []DailyInsightNarrativeDomain{{Key: slot}}
		}
		return out, nil
	}
	if section != nil {
		var err error
		section, err = normalizeDailyInsightNarrativeSection(*section, input.Slot, locale)
		if err != nil {
			return DailyInsightNarrative{}, err
		}
	}
	out.Domains = []DailyInsightNarrativeDomain{{Key: slot, Section: section}}
	return out, nil
}

// ValidateDailyInsightNarrativeSlotResponse validates the compact response
// shape used by the independent slot generator. The return value is nil when
// the model correctly elects not to add meaning beyond the server fallback.
func ValidateDailyInsightNarrativeSlotResponse(snapshot *DailyInsightSnapshot, locale, slot string, candidate DailyInsightNarrativeSlot) (*DailyInsightNarrativeSection, error) {
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, locale, slot)
	if !known {
		return nil, fmt.Errorf("unknown narrative slot %q", slot)
	}
	return ValidateDailyInsightNarrativeSlotResponseWithInput(snapshot, locale, slot, input.Slot, candidate)
}

// ValidateDailyInsightNarrativeSlotResponseWithInput validates against an
// already-frozen provider packet. It is used by the offline evaluator so the
// semantic boundary is checked against the exact B0 baseline, facts and
// action allow-list that were sent to the provider rather than rebuilding a
// potentially different packet from a sanitized snapshot.
func ValidateDailyInsightNarrativeSlotResponseWithInput(snapshot *DailyInsightSnapshot, locale, slot string, input DailyInsightNarrativeDomainInput, candidate DailyInsightNarrativeSlot) (*DailyInsightNarrativeSection, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("daily insight snapshot is nil")
	}
	if input.Key != slot {
		return nil, fmt.Errorf("frozen narrative packet key %q, want %q", input.Key, slot)
	}
	if candidate.Version != DailyInsightNarrativeVersion {
		return nil, fmt.Errorf("unexpected narrative version %q", candidate.Version)
	}
	if candidate.Locale != normalizeDailyInsightLocale(locale) {
		return nil, fmt.Errorf("unexpected narrative locale %q", candidate.Locale)
	}
	if candidate.Slot.Key != slot {
		return nil, fmt.Errorf("response slot %q, want %q", candidate.Slot.Key, slot)
	}
	if slot == DailyInsightNarrativeOverallSlot {
		if candidate.Slot.Section == nil {
			// A provider null is a technical fail-safe; later evaluation treats it
			// as product failure, but B0 must still remain available at runtime.
			return nil, nil
		}
		return validateDailyInsightHumanSynthesis(candidate.Slot.Section, input, locale)
	}
	if len(input.Claims) == 0 {
		if candidate.Slot.Section != nil {
			return nil, fmt.Errorf("slot %q has no eligible claims", slot)
		}
		return nil, nil
	}
	if candidate.Slot.Section == nil {
		return nil, nil
	}
	if candidate.Slot.Section.AnchorVariantID != "" {
		return nil, fmt.Errorf("provider response must not select a server anchor variant")
	}
	return normalizeDailyInsightNarrativeSection(*candidate.Slot.Section, input, locale)
}

// ApplyDailyInsightNarrativeSlot attaches an already validated independent
// section. It never touches another domain’s text or the deterministic action.
func ApplyDailyInsightNarrativeSlot(snapshot *DailyInsightSnapshot, locale, slot string, section *DailyInsightNarrativeSection) (*DailyInsightSnapshot, error) {
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, locale, slot)
	if !known {
		return nil, fmt.Errorf("unknown narrative slot %q", slot)
	}
	if section == nil {
		return cloneDailyInsightSnapshot(snapshot), nil
	}
	var text string
	var claimIDs, evidenceIDs []string
	if slot == DailyInsightNarrativeOverallSlot {
		validated, err := validateDailyInsightHumanSynthesis(section, input.Slot, locale)
		if err != nil {
			return nil, err
		}
		text, claimIDs = validated.Text, append([]string(nil), validated.FactIDs...)
		for _, fact := range input.Slot.Facts {
			if containsDailyInsightID(claimIDs, fact.ID) {
				evidenceIDs = append(evidenceIDs, fact.EvidenceIDs...)
			}
		}
	} else {
		if err := validateDailyInsightNarrativeSection(*section, input.Slot, locale); err != nil {
			return nil, err
		}
		text, claimIDs, evidenceIDs = flattenDailyInsightNarrativeSection(*section, input.Slot)
	}
	out := cloneDailyInsightSnapshot(snapshot)
	overlay := &DailyInsightNarrativeOverlay{Text: text, ClaimIDs: claimIDs, EvidenceIDs: evidenceIDs}
	if slot == DailyInsightNarrativeOverallSlot {
		out.Primary.Narrative = overlay
		return out, nil
	}
	for index := range out.Domains {
		if out.Domains[index].Key == slot {
			out.Domains[index].Insight.Narrative = overlay
			return out, nil
		}
	}
	return nil, fmt.Errorf("snapshot lacks narrative slot %q", slot)
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
	out := DailyInsightNarrativeSection{AnchorVariantID: section.AnchorVariantID, Text: strings.TrimSpace(section.Text), FactIDs: append([]string(nil), section.FactIDs...), ActionID: section.ActionID, Sentences: make([]DailyInsightNarrativeSentence, 0, len(section.Sentences))}
	for _, sentence := range section.Sentences {
		out.Sentences = append(out.Sentences, DailyInsightNarrativeSentence{
			Text: strings.TrimSpace(sentence.Text), ClaimIDs: append([]string(nil), sentence.ClaimIDs...), QualifierIDs: append([]string(nil), sentence.QualifierIDs...), MeaningIDs: append([]string(nil), sentence.MeaningIDs...), PositionIDs: append([]string(nil), sentence.PositionIDs...),
		})
	}
	return &out
}

func validateDailyInsightHumanSynthesis(section *DailyInsightNarrativeSection, input DailyInsightNarrativeDomainInput, locale string) (*DailyInsightNarrativeSection, error) {
	if section.AnchorVariantID != "" || len(section.Sentences) != 0 {
		return nil, fmt.Errorf("human synthesis response must use text, fact_ids, and action_id only")
	}
	text := strings.TrimSpace(section.Text)
	if text == "" {
		return nil, fmt.Errorf("human synthesis text is empty")
	}
	if words := len(strings.Fields(text)); words > 75 {
		return nil, fmt.Errorf("human synthesis has %d words, want at most 75", words)
	}
	if sentences := narrativeSentenceCount(text); sentences < 1 || sentences > 3 {
		return nil, fmt.Errorf("human synthesis has %d sentences, want 1..3", sentences)
	}
	facts := make(map[string]DailyInsightNarrativeFact, len(input.Facts))
	for _, fact := range input.Facts {
		facts[fact.ID] = fact
	}
	if len(section.FactIDs) == 0 {
		return nil, fmt.Errorf("human synthesis must cite facts")
	}
	domains := map[string]struct{}{}
	seen := map[string]struct{}{}
	for _, id := range section.FactIDs {
		fact, ok := facts[id]
		if !ok {
			return nil, fmt.Errorf("unsupported fact ID %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("duplicate fact ID %q", id)
		}
		seen[id] = struct{}{}
		domains[fact.Domain] = struct{}{}
	}
	if len(domains) < 2 {
		return nil, fmt.Errorf("human synthesis must cite at least two domains")
	}
	if err := validateNarrativeNumbers(text, input.Facts); err != nil {
		return nil, err
	}
	if err := validateDailyInsightNarrativeLocale(text, locale); err != nil {
		return nil, err
	}
	if violation := forbiddenHumanSynthesisFragment(text); violation != "" {
		return nil, fmt.Errorf("unsafe human synthesis text contains %q", violation)
	}
	if violation := humanSynthesisServerActionConflict(text, input.ActionOptions); violation != "" {
		return nil, fmt.Errorf("unsafe human synthesis %s", violation)
	}
	if section.ActionID != "" {
		allowed := false
		for _, action := range input.ActionOptions {
			if action.ID == section.ActionID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("unsupported action ID %q", section.ActionID)
		}
	}
	return cloneDailyInsightNarrativeSection(*section), nil
}

func narrativeSentenceCount(text string) int {
	runes := []rune(text)
	count := 0
	for index, r := range runes {
		if (r == '.' || r == ',') && index > 0 && index+1 < len(runes) && unicode.IsDigit(runes[index-1]) && unicode.IsDigit(runes[index+1]) {
			continue
		}
		if r == '.' || r == '!' || r == '?' {
			count++
		}
	}
	if count == 0 && strings.TrimSpace(text) != "" {
		return 1
	}
	return count
}

func validateDailyInsightNarrativeLocale(text, locale string) error {
	if normalizeDailyInsightLocale(locale) == "sr" && containsCyrillicNarrativeText(text) {
		return fmt.Errorf("Serbian narrative must use Latin script")
	}
	if normalizeDailyInsightLocale(locale) == "ru" && !containsCyrillicNarrativeText(text) {
		return fmt.Errorf("Russian narrative must use Cyrillic script")
	}
	if normalizeDailyInsightLocale(locale) == "en" && containsCyrillicNarrativeText(text) {
		return fmt.Errorf("English narrative must not use Cyrillic script")
	}
	return nil
}

// This is deliberately a narrow hard-safety classifier, not a style grammar.
// Naturalness and duplication belong to the evaluation corpus, not runtime.
func forbiddenHumanSynthesisFragment(text string) string {
	lower := strings.ToLower(text)
	if forecast := humanSynthesisForecastOutcome(lower); forecast != "" {
		return forecast
	}
	// Keep English causal verbs as whole words so ordinary "because of" is
	// not mistaken for the forbidden stem inside be-cause.
	for _, token := range strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch token {
		case "cause", "causes", "caused", "causing":
			return token
		}
	}
	for _, fragment := range []string{
		"diagnos", "disease", "medical", "treatment", "medication", "supplement", "prescrib", "prognos",
		"диагноз", "болезн", "медицин", "лечени", "лекар", "добавк", "прогноз",
		"dijagnoz", "bolest", "medicin", "lečen", "lek ", "suplement", "prognoz",
		// Causality, forecasts, and assertions about feelings or capacity are
		// hard health-safety boundaries, not prose-style preferences. Each stem
		// covers the corresponding grammatical forms in the supported locales.
		"leads to", "guarantee", "feel ", "can handle", "capacity", "able to",
		"вызыва", "привод", "гарант", "почувству", "устал", "сможешь", "способен",
		"uzroku", "dovodi", "garant", "oseća", "umor", "sposoban",
		// A suggestion may be low-risk, but it cannot promise an unsupported
		// benefit for energy or recovery.
		"help protect", "help support", "help preserve", "help improve", "protect your energy", "support your recovery", "preserve your energy", "improve your recovery",
		"поможет", "поддержит восстановление", "сохранит энергию", "улучшит восстановление",
		"pomoći", "podržaće oporavak", "sačuvaće energiju", "poboljšaće oporavak",
		// Do not let generated copy dismiss urgent or professional care.
		"no need to see", "do not seek care", "don't seek care", "ignore urgent", "не нужно обращаться к врачу", "не обращайся к врачу", "игнорируй сроч", "ne moraš kod lekara", "nemoj kod lekara", "ignoriši hitn",
		// Strong exercise or restriction commands are not reversible everyday
		// suggestions and remain outside B1's authority.
		"all-out", "maximal workout", "train hard", "fast all day", "skip meals", "starve", "тренируйся на максимум", "жестко огранич", "голодай", "не ешь", "treniraj maksimalno", "gladuj", "preskoči obroke", "strogo ogranič",
	} {
		if strings.Contains(lower, fragment) {
			return fragment
		}
	}
	return ""
}

// humanSynthesisForecastOutcome rejects promised health or functional
// outcomes, rather than treating a language's generic future auxiliary as a
// forecast. This lets ordinary soft future phrasing remain available in all
// supported locales.
func humanSynthesisForecastOutcome(lower string) string {
	for _, fragment := range []string{
		"will recover", "will improve", "will feel", "will be tired", "you'll recover", "you'll improve", "you'll feel", "recovery will", "energy will improve", "energy will fall", "readiness will improve", "readiness will fall", "feel better", "feel worse",
		"будешь восстан", "восстановишься", "будешь чувств", "будешь устав", "станет лучше", "станет хуже", "энергия улучшится", "энергия снизится", "готовность улучшится", "готовность снизится", "завтра восстанов",
		"ćeš se oporav", "oporavićeš se", "osećaćeš", "bićeš umor", "energija će se poboljš", "energija će pasti", "spremnost će se poboljš", "spremnost će pasti", "sutra ćeš se oporav",
	} {
		if strings.Contains(lower, fragment) {
			return fragment
		}
	}
	return ""
}

// humanSynthesisServerActionConflict preserves the server-owned safety
// boundary without attempting to classify every ordinary suggestion. B1 may
// offer a reversible everyday idea without action_id, but cannot contradict a
// currently supplied server action.
func humanSynthesisServerActionConflict(text string, options []DailyInsightNarrativeAction) string {
	lower := strings.ToLower(text)
	containsAny := func(fragments []string) bool {
		for _, fragment := range fragments {
			if strings.Contains(lower, fragment) {
				return true
			}
		}
		return false
	}
	for _, option := range options {
		switch option.ID {
		case "wind_down":
			if containsAny([]string{"stay up late", "skip sleep", "работай допоздна", "не ложись", "ostani budan", "preskoči san"}) {
				return "contains a command that conflicts with the server action"
			}
		case "daily-decision-rest", "daily-decision-active_recovery", "daily-decision-moderate":
			if containsAny([]string{"push through", "go hard", "work out hard", "работай на пределе", "тренируйся интенсивно", "idi do kraja", "treniraj jako"}) {
				return "contains a command that conflicts with the server action"
			}
		}
	}
	return ""
}

func validateDailyInsightNarrativeSection(section DailyInsightNarrativeSection, input DailyInsightNarrativeDomainInput, locale string) error {
	if section.AnchorVariantID != "" {
		// Previously persisted v12 sections remain readable for audit/review. Raw
		// provider responses still cannot choose an anchor (checked at the API
		// boundary), and v13 rendering deliberately does not prepend one.
		if _, err := dailyInsightNarrativeAnchor(input, section.AnchorVariantID); err != nil {
			return err
		}
	}
	return validateDailyInsightNarrativeSentences(section.Sentences, input, locale)
}

// normalizeDailyInsightNarrativeSection accepts a raw provider paragraph. The
// model now writes the complete reader-facing text from server-selected facts;
// an old anchor-prefixed section is intentionally stale after the v13 packet.
func normalizeDailyInsightNarrativeSection(section DailyInsightNarrativeSection, input DailyInsightNarrativeDomainInput, locale string) (*DailyInsightNarrativeSection, error) {
	if section.AnchorVariantID != "" {
		if _, err := dailyInsightNarrativeAnchor(input, section.AnchorVariantID); err != nil {
			return nil, err
		}
	}
	if err := validateDailyInsightNarrativeSentences(section.Sentences, input, locale); err != nil {
		return nil, err
	}
	return cloneDailyInsightNarrativeSection(section), nil
}

type dailyInsightNarrativeSelectedAnchor struct {
	ID          string
	ClaimID     string
	Text        string
	EvidenceIDs []string
}

func dailyInsightNarrativeAnchor(input DailyInsightNarrativeDomainInput, requestedID string) (dailyInsightNarrativeSelectedAnchor, error) {
	anchors := make([]dailyInsightNarrativeSelectedAnchor, 0)
	for _, claim := range input.Claims {
		for _, anchor := range claim.AnchorVariants {
			if anchor.ID == "" || strings.TrimSpace(anchor.Text) == "" {
				return dailyInsightNarrativeSelectedAnchor{}, fmt.Errorf("claim %q has an invalid anchor variant", claim.ID)
			}
			anchors = append(anchors, dailyInsightNarrativeSelectedAnchor{ID: anchor.ID, ClaimID: claim.ID, Text: anchor.Text, EvidenceIDs: append([]string(nil), claim.EvidenceIDs...)})
		}
	}
	if len(anchors) == 0 {
		return dailyInsightNarrativeSelectedAnchor{}, fmt.Errorf("narrative claim packet has no anchor variants")
	}
	if requestedID == "" {
		// Claim builders currently expose one fact anchor per eligible slot.
		// Fail closed if a later builder adds alternatives without a server
		// selection rule rather than handing that factual choice to the model.
		if len(anchors) != 1 {
			return dailyInsightNarrativeSelectedAnchor{}, fmt.Errorf("narrative claim packet needs a deterministic anchor selection")
		}
		return anchors[0], nil
	}
	for _, anchor := range anchors {
		if anchor.ID == requestedID {
			return anchor, nil
		}
	}
	return dailyInsightNarrativeSelectedAnchor{}, fmt.Errorf("unapproved anchor variant ID %q", requestedID)
}

func validateDailyInsightNarrativeSentences(sentences []DailyInsightNarrativeSentence, input DailyInsightNarrativeDomainInput, locale string) error {
	if len(sentences) == 0 || len(sentences) > 3 {
		return fmt.Errorf("expected one to three narrative sentences")
	}
	claims := make(map[string]DailyInsightNarrativeClaim, len(input.Claims))
	meanings := make(map[string]DailyInsightNarrativeMeaningLink)
	meaningClaimIDs := make(map[string]map[string]struct{})
	requiredClaims, requiredQualifiers := make(map[string]struct{}, len(input.Claims)), map[string]struct{}{}
	for _, claim := range input.Claims {
		claims[claim.ID] = claim
		requiredClaims[claim.ID] = struct{}{}
		for _, qualifierID := range claim.RequiredQualifierIDs {
			requiredQualifiers[qualifierID] = struct{}{}
		}
		for _, meaning := range claim.MeaningLinks {
			meanings[meaning.ID] = meaning
			if meaningClaimIDs[meaning.ID] == nil {
				meaningClaimIDs[meaning.ID] = make(map[string]struct{})
			}
			meaningClaimIDs[meaning.ID][claim.ID] = struct{}{}
		}
	}
	usedClaims, usedQualifiers, usedMeanings := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	wordCount := 0
	for _, sentence := range sentences {
		text := strings.TrimSpace(sentence.Text)
		if text == "" {
			return fmt.Errorf("empty sentence")
		}
		wordCount += len(strings.Fields(text))
		if err := validateNarrativeNumbers(text, input.Facts); err != nil {
			return err
		}
		if forbidden := forbiddenNarrativeFragment(text); forbidden != "" {
			return fmt.Errorf("forbidden narrative content %q", forbidden)
		}
		if err := validateDailyInsightNarrativeClaimScope(text, input); err != nil {
			return err
		}
		if normalizeDailyInsightLocale(locale) == "sr" && containsCyrillicNarrativeText(text) {
			return fmt.Errorf("Serbian narrative must use Latin script")
		}
		if len(sentence.ClaimIDs) == 0 {
			return fmt.Errorf("sentence has no claim IDs")
		}
		if len(meanings) != 0 && len(sentence.MeaningIDs) != 1 {
			return fmt.Errorf("section must cite exactly one meaning ID")
		}
		if input.Position != nil {
			if len(sentence.PositionIDs) != 1 || sentence.PositionIDs[0] != input.Position.ID {
				return fmt.Errorf("sentence must cite server position %q", input.Position.ID)
			}
		} else if len(sentence.PositionIDs) != 0 {
			return fmt.Errorf("sentence cites a server position outside this slot")
		}
		for _, meaningID := range sentence.MeaningIDs {
			if _, known := meanings[meaningID]; !known {
				return fmt.Errorf("unapproved meaning ID %q", meaningID)
			}
			usedMeanings[meaningID] = struct{}{}
		}
		for _, claimID := range sentence.ClaimIDs {
			if _, known := claims[claimID]; !known {
				return fmt.Errorf("unapproved claim ID %q", claimID)
			}
			usedClaims[claimID] = struct{}{}
		}
		for _, meaningID := range sentence.MeaningIDs {
			allowedClaims := meaningClaimIDs[meaningID]
			linked := false
			for _, claimID := range sentence.ClaimIDs {
				if _, allowed := allowedClaims[claimID]; allowed {
					linked = true
					break
				}
			}
			if !linked {
				return fmt.Errorf("meaning ID %q is not linked to a cited claim", meaningID)
			}
		}
		for _, qualifierID := range sentence.QualifierIDs {
			if _, required := requiredQualifiers[qualifierID]; !required {
				return fmt.Errorf("unapproved qualifier ID %q", qualifierID)
			}
			usedQualifiers[qualifierID] = struct{}{}
		}
	}
	if wordCount > 65 {
		return fmt.Errorf("narrative exceeds 65 words")
	}
	if len(meanings) != 0 && len(usedMeanings) != 1 {
		return fmt.Errorf("section must cite exactly one meaning ID")
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

// validateDailyInsightNarrativeClaimScope protects a closed, asymmetric fact
// in the B0 sleep claim. The evaluator counts shorter nights in D-3..D; it
// does not establish that those nights were consecutive. This is intentionally
// a narrow contract check rather than a general prose classifier.
func validateDailyInsightNarrativeClaimScope(text string, input DailyInsightNarrativeDomainInput) error {
	if !dailyInsightNarrativeInputHasClaim(input, "recent_sleep_below_reference") {
		return nil
	}
	lower := strings.ToLower(text)
	for _, fragment := range []string{
		"in a row", "consecutive", "last three nights",
		"подряд", "последние три ночи",
		"zaredom", "poslednje tri noći",
	} {
		if strings.Contains(lower, fragment) {
			return fmt.Errorf("sleep narrative overstates the four-night count as %q", fragment)
		}
	}
	return nil
}

func dailyInsightNarrativeInputHasClaim(input DailyInsightNarrativeDomainInput, want string) bool {
	for _, claim := range input.Claims {
		if claim.ID == want {
			return true
		}
	}
	return false
}

func validateNarrativeNumbers(text string, facts []DailyInsightNarrativeFact) error {
	allowed := []string{}
	for _, fact := range facts {
		for _, value := range fact.DisplayValues {
			for _, token := range narrativeNumericTokens(value) {
				allowed = append(allowed, token)
			}
		}
	}
	for _, token := range narrativeNumericTokens(text) {
		if !narrativeNumberIsAllowed(token, allowed) {
			return fmt.Errorf("numeric text %q is not present in the server display catalog", token)
		}
	}
	return nil
}

// Russian and Serbian prose commonly renders decimal fractions with a comma.
// Treat punctuation variants as the same supplied number without allowing a
// different value or performing any model-owned calculation.
func canonicalNarrativeNumericToken(token string) string {
	return strings.ReplaceAll(token, ",", ".")
}

func narrativeNumberIsAllowed(token string, allowed []string) bool {
	for _, value := range allowed {
		if canonicalNarrativeNumericToken(token) == canonicalNarrativeNumericToken(value) {
			return true
		}
		if grouped, ok := narrativeGroupedIntegerDigits(token); ok && digitsOnlyNarrativeNumber(value) && grouped == value {
			return true
		}
	}
	return false
}

func narrativeGroupedIntegerDigits(token string) (string, bool) {
	var groups []string
	var current strings.Builder
	separator := rune(0)
	for _, r := range token {
		if unicode.IsDigit(r) {
			current.WriteRune(r)
			continue
		}
		if r != '.' && r != ',' && !isNarrativeGroupingSpace(r) {
			return "", false
		}
		if current.Len() == 0 || (separator != 0 && separator != r) {
			return "", false
		}
		groups, current = append(groups, current.String()), strings.Builder{}
		separator = r
	}
	if separator == 0 || current.Len() == 0 {
		return "", false
	}
	groups = append(groups, current.String())
	if len(groups[0]) < 1 || len(groups[0]) > 3 {
		return "", false
	}
	for _, group := range groups[1:] {
		if len(group) != 3 {
			return "", false
		}
	}
	return strings.Join(groups, ""), true
}

func digitsOnlyNarrativeNumber(value string) bool {
	return value != "" && strings.IndexFunc(value, func(r rune) bool { return !unicode.IsDigit(r) }) == -1
}

func isNarrativeGroupingSpace(r rune) bool { return r == ' ' || r == '\u00a0' || r == '\u202f' }

// dailyInsightNarrativeAnchorRestatementFragments is deliberately a small,
// locale-specific denylist for the factual proposition already rendered by the
// server. It is not an attempt at general semantic inference: the provider is
// free to explain the allowed meaning naturally, but must not recast the same
// factual state in different words beside the server fact.
func dailyInsightNarrativeAnchorRestatementFragments(locale, anchorID string) []string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		switch anchorID {
		case "recent-shorter-than-usual":
			return []string{"спал меньше обычного", "последние ночи были короче обычного"}
		case "recovery-strong":
			return []string{"восстановление на хорошем уровне", "восстановление выглядит сильным", "хорошее восстановление"}
		case "recovery-lower":
			return []string{"восстановление не на пике", "слабое восстановление"}
		case "energy-available":
			return []string{"сил сегодня достаточно", "энергии сегодня достаточно"}
		case "energy-lower":
			return []string{"сил немного", "мало сил", "энергии мало"}
		case "energy-no-extra-intensity":
			return []string{"не добавлять интенсивности"}
		case "rest":
			return []string{"выбран режим отдыха", "режим отдыха"}
		case "active-recovery":
			return []string{"выбран режим активного восстановления", "режим активного восстановления"}
		case "push-hard":
			return []string{"выбран режим более высокой нагрузки", "режим более высокой нагрузки"}
		case "moderate":
			return []string{"выбран умеренный режим", "умеренный режим"}
		}
	case "sr":
		switch anchorID {
		case "recent-shorter-than-usual":
			return []string{"spavao si manje nego što je za tebe uobičajeno", "poslednjih noći bilo je kraće nego obično"}
		case "recovery-strong":
			return []string{"signali oporavka u višem opsegu spremnosti", "oporavak izgleda snažno"}
		case "recovery-lower":
			return []string{"signali oporavka u nižem opsegu spremnosti", "slabiji oporavak"}
		case "energy-available":
			return []string{"rezerva energije je u opsegu većeg kapaciteta", "dovoljno energije danas"}
		case "energy-lower":
			return []string{"rezerva energije je u opsegu nižeg kapaciteta", "malo energije danas"}
		case "energy-no-extra-intensity":
			return []string{"ne dodavati intenzitet"}
		case "rest":
			return []string{"izabran režim odmora", "režim odmora"}
		case "active-recovery":
			return []string{"izabran režim aktivnog oporavka", "režim aktivnog oporavka"}
		case "push-hard":
			return []string{"izabran režim većeg opterećenja", "režim većeg opterećenja"}
		case "moderate":
			return []string{"izabran umeren režim", "umeren režim"}
		}
	default:
		switch anchorID {
		case "recent-shorter-than-usual":
			return []string{"recent nights were shorter than usual", "you slept less than usual"}
		case "recovery-strong":
			return []string{"recovery signals sit in the higher readiness band", "your recovery looks strong"}
		case "recovery-lower":
			return []string{"recovery signals sit in the lower readiness band", "your recovery is low"}
		case "energy-available":
			return []string{"current energy context is in a higher capacity range", "you have enough energy today"}
		case "energy-lower":
			return []string{"current energy context is in a lower capacity range", "you have little energy today"}
		case "energy-no-extra-intensity":
			return []string{"better suited to avoiding extra intensity", "do not add intensity"}
		case "rest":
			return []string{"today is set to a rest-oriented pace", "rest-oriented pace"}
		case "active-recovery":
			return []string{"today is set to an active-recovery pace", "active-recovery pace"}
		case "push-hard":
			return []string{"today is set to a higher-load pace", "higher-load pace"}
		case "moderate":
			return []string{"today is set to a moderate pace", "moderate pace"}
		}
	}
	return nil
}

func normalizeDailyInsightNarrativeComparisonText(text string) string {
	var out strings.Builder
	space := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
			space = false
			continue
		}
		if !space {
			out.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(out.String())
}

func containsCyrillicNarrativeText(text string) bool {
	for _, r := range text {
		if (r >= 'А' && r <= 'я') || r == 'Ё' || r == 'ё' {
			return true
		}
	}
	return false
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
		"limitation", "limited data", "insufficient data", "data quality", "uncertain", "scope", "calibrat",
		"диагноз", "лечени", "прогноз", "болезн", "медицин", "лекар",
		"тебе нужно", "вам нужно", "следует ", "избегай", "избегайте", "сделай ", "сделайте ",
		"ограничен", "недостаточ", "не хватает данных", "качество данных", "неопредел", "калибров",
		"dijagnoz", "lečen", "prognoz", "bolest", "medicin", "lek ",
		"treba da", "izbeg", "uradi ", "uradite ",
		"ograničen", "nedovoljno podataka", "kvalitet podataka", "neizves", "kalibr",
		"random", "reliab", "случайн", "надежн", "надёжн", "slučajn", "pouzdan",
		"sleep pattern in today’s picture", "sleep pattern in today's picture", "energy is a resource to spread across the day", "more room to choose the day’s pace", "more room to choose the day's pace",
		"заметный рисунок сна", "энергию стоит распределить", "распределить энергию", "свободы в выборе темпа",
		"primetan obrazac sna", "energija je danas resurs", "vredi rasporediti kroz dan", "više slobode pri izboru ritma dana",
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
	decisionEvidenceDomains := []string{}
	if decision != nil {
		decisionID = decision.ID
		for _, domain := range decision.EvidenceDomains {
			if containsDailyInsightID(dailyInsightNarrativeDomainKeys, domain) && !containsDailyInsightID(decisionEvidenceDomains, domain) {
				decisionEvidenceDomains = append(decisionEvidenceDomains, domain)
			}
		}
	}
	updatedAt := (*time.Time)(nil)
	if resp.TodayGuidance != nil {
		updatedAt = resp.TodayGuidance.UpdatedAt
	}

	snapshot := &DailyInsightSnapshot{
		Date: resp.Date, DecisionID: decisionID, Version: DailyInsightSnapshotVersion, UpdatedAt: updatedAt,
		Domains: []DailyInsightDomain{}, Evidence: []DailyInsightEvidence{}, Changes: []DailyInsightChange{}, DecisionEvidenceDomains: decisionEvidenceDomains,
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
	snapshot.NarrativeFacts = buildDailyInsightNarrativeFacts(resp, snapshot.Domains, lang)
	return snapshot
}

// buildDailyInsightNarrativeFacts selects only typed, derived briefing values.
// In particular it intentionally excludes Sleep.Sources, raw samples,
// EnergyBank.Components and IllnessSuspicion prose: those fields are either
// identifying/provenance detail or have incompatible semantics during the
// EnergyBank v1/v2 cutover.
func buildDailyInsightNarrativeFacts(resp *BriefingResponse, domains []DailyInsightDomain, locale string) []DailyInsightNarrativeFact {
	if resp == nil {
		return []DailyInsightNarrativeFact{}
	}
	facts := make([]DailyInsightNarrativeFact, 0, 8)
	add := func(fact DailyInsightNarrativeFact) {
		fact.Authority = "server_derived"
		facts = append(facts, fact)
	}
	partialSleep := narrativeDomainDataState(domains, "sleep") == "partial"
	if sleepNarrativeFactFresh(resp) && partialSleep {
		sleep := resp.Sleep
		value := fmt.Sprintf("%.1f", *sleep.LatestTotal)
		add(DailyInsightNarrativeFact{ID: "sleep_current_recorded_duration", Domain: "sleep", Meaning: partialSleepCurrentDurationMeaning, Window: partialSleepCurrentDurationWindow, Fresh: true, Statement: localizedNarrativePartialSleepFact(locale, *sleep.LatestTotal), DisplayValues: []string{value}, EvidenceIDs: []string{"sleep_current_recorded_duration"}})
	} else if sleepNarrativeFactFresh(resp) {
		sleep := resp.Sleep
		values := []string{fmt.Sprintf("%.1f", *sleep.LatestTotal)}
		statement := localizedNarrativeSleepFact(locale, *sleep.LatestTotal, sleep.TotalAvg)
		if sleep.TotalAvg > 0 {
			values = append(values, fmt.Sprintf("%.1f", sleep.TotalAvg))
		}
		add(DailyInsightNarrativeFact{ID: "sleep_canonical_comparison", Domain: "sleep", Meaning: "canonical sleep duration compared with recent average", Window: "last night and recent average", Fresh: true, Statement: statement, DisplayValues: values, EvidenceIDs: []string{"sleep_canonical_comparison"}})
	}
	if quality := resp.SleepQuality; !partialSleep && sleepNarrativeFactFresh(resp) && quality != nil && quality.ScorePct != nil && quality.Confidence == SleepQualityConfidenceFinal {
		value := fmt.Sprintf("%d", *quality.ScorePct)
		add(DailyInsightNarrativeFact{ID: "sleep_quality", Domain: "sleep", Meaning: "server-derived sleep quality", Window: "last night", Fresh: true, Statement: localizedNarrativeSleepQualityFact(locale, *quality.ScorePct), DisplayValues: []string{value}, EvidenceIDs: []string{"sleep_quality"}})
	}
	if readinessNarrativeFresh(resp) {
		score := resp.ReadinessDisplayScore
		if score == 0 && resp.ReadinessToday != 0 {
			score = resp.ReadinessToday
		}
		add(DailyInsightNarrativeFact{ID: "readiness_current", Domain: "recovery", Meaning: "current readiness display, band, and serving state", Window: "today", Fresh: true, Statement: localizedNarrativeReadinessFact(locale, score, firstNonEmptyInsight(resp.ReadinessTodayLabel, resp.ReadinessLabel), resp.ReadinessServing), DisplayValues: []string{fmt.Sprintf("%d", score)}, EvidenceIDs: []string{"readiness_current"}})
		if raw := resp.RawMetrics; raw != nil && raw.ReadinessEvidence != nil {
			for _, component := range []ReadinessComponentEvidence{raw.ReadinessEvidence.HRV, raw.ReadinessEvidence.RHR} {
				if fact, ok := readinessComponentNarrativeFact(resp, domains, locale, component); ok {
					add(fact)
				}
			}
		}
	}
	if headline := resp.Headline; headline != nil && narrativeBriefingDateAligned(resp.Date) {
		added := 0
		for _, metric := range headline.Metrics {
			if added >= 2 { // context, not a metric dump
				break
			}
			domain, ok := narrativeHeadlineMetricDomain(metric.Metric)
			if !ok || metric.Value <= 0 || !headlineNarrativeFactFresh(resp, domains, domain, metric.Metric) {
				continue
			}
			values := []string{fmt.Sprintf("%.1f", metric.Value)}
			window := "current day"
			if metric.Baseline > 0 {
				window = "current day versus personal baseline"
				values = append(values, fmt.Sprintf("%.1f", metric.Baseline), fmt.Sprintf("%.1f", metric.DeltaAbs))
			}
			add(DailyInsightNarrativeFact{ID: fmt.Sprintf("headline_%s", metric.Metric), Domain: domain, Meaning: "server-derived " + domain + " headline value", Window: window, Fresh: true, Statement: localizedNarrativeHeadlineFact(locale, metric), DisplayValues: values, EvidenceIDs: []string{fmt.Sprintf("headline_%s", metric.Metric)}})
			added++
		}
	}
	if bank := resp.EnergyBank; bank != nil && energyNarrativeFresh(bank) {
		statement := localizedNarrativeEnergyFact(locale, bank)
		add(DailyInsightNarrativeFact{ID: "energy_authoritative_state", Domain: "energy", Meaning: "current EnergyBank reserve, drain, strain and stress measurements; server verdict is separate", Window: "today so far", Fresh: true, Statement: statement, DisplayValues: narrativeDisplayValues(statement), EvidenceIDs: []string{"energy_authoritative_state"}})
	}
	if raw := resp.RawMetrics; raw != nil && raw.LastDate == resp.Date && narrativeBriefingDateAligned(resp.Date) {
		if !partialSleep {
			if fact, ok := boundedSleepPatternFact(raw.Daily, raw.LastDate, locale); ok {
				add(fact)
			}
		}
		if fact, ok := boundedActivityTrendFact(raw.Daily, raw.LastDate, locale); ok {
			add(fact)
		}
	}
	return facts
}

func narrativeDomainDataState(domains []DailyInsightDomain, key string) string {
	for _, domain := range domains {
		if domain.Key == key {
			return domain.DataState
		}
	}
	return ""
}

// headlineNarrativeFactFresh keeps compact headline slices from relabelling a
// prior measurement as today's fact. A headline is B0 display context; it
// enters B1 only when the domain that owns the metric is fresh, final, and
// factual, and when that domain has exact date-aligned evidence for the
// specific metric. This deliberately withholds an otherwise useful headline
// rather than letting it create a second fresh domain for B1 eligibility.
func headlineNarrativeFactFresh(resp *BriefingResponse, domains []DailyInsightDomain, domain, metric string) bool {
	if resp == nil || !narrativeBriefingDateAligned(resp.Date) || !narrativeDomainFreshFinalFactual(domains, domain) {
		return false
	}
	if domain == "sleep" && metric == "sleep_total" {
		return sleepNarrativeFactFresh(resp)
	}
	return narrativeDailyMetricFresh(resp.RawMetrics, resp.Date, metric)
}

func narrativeDomainFreshFinalFactual(domains []DailyInsightDomain, key string) bool {
	for _, domain := range domains {
		if domain.Key != key {
			continue
		}
		return domain.DataState == "fresh" && domain.Confidence == "final" && domain.Insight.State == "insight" && domain.Insight.AnswerKind == DailyInsightAnswerFactual
	}
	return false
}

// narrativeDailyMetricFresh reads only the private, date-aligned carry. It
// never serializes that carry to the provider. Requiring exactly one current
// row makes ambiguous or compacted input fail closed.
func narrativeDailyMetricFresh(raw *RawMetrics, date, metric string) bool {
	if raw == nil || raw.LastDate != date || !narrativeBriefingDateAligned(date) {
		return false
	}
	found, present := false, false
	for _, daily := range raw.Daily {
		if daily.Date != date {
			continue
		}
		if found {
			return false
		}
		found = true
		switch metric {
		case "sleep_total":
			present = daily.Sleep != nil
		case "sleep_awake":
			present = daily.Awake != nil
		case "sleep_deep":
			present = daily.Deep != nil
		case "sleep_rem":
			present = daily.REM != nil
		case "sleep_core":
			present = daily.Core != nil
		case "sleep_unspecified":
			present = daily.Unspecified != nil
		case "heart_rate_variability":
			present = daily.HRV != nil
		case "resting_heart_rate":
			present = daily.RHR != nil
		case "step_count", "steps":
			present = daily.Steps != nil
		case "active_energy":
			present = daily.Calories != nil
		case "apple_exercise_time":
			present = daily.Exercise != nil
		}
	}
	return found && present
}

// sleepNarrativeFactFresh is intentionally stricter than the UI fallback:
// B1 can only expose a last-night value when the typed sleep record proves it
// belongs to this briefing date.
func sleepNarrativeFactFresh(resp *BriefingResponse) bool {
	return resp != nil && resp.Sleep != nil && resp.Sleep.LatestTotal != nil && narrativeBriefingDateAligned(resp.Date) && resp.Sleep.LatestDate == resp.Date
}

func narrativeBriefingDateAligned(date string) bool {
	_, ok := parseDailyNarrativeDate(date)
	return ok
}

// narrativeHeadlineMetricDomain maps the fixed typed headline catalogue to
// the domain that owns its meaning. Unknown metrics stay out of B1 rather than
// being relabelled as recovery evidence.
func narrativeHeadlineMetricDomain(metric string) (string, bool) {
	switch metric {
	case "sleep_total", "sleep_awake", "sleep_deep", "sleep_rem", "sleep_core", "sleep_unspecified":
		return "sleep", true
	case "heart_rate_variability", "resting_heart_rate":
		return "recovery", true
	case "step_count", "steps", "active_energy", "apple_exercise_time", "flights_climbed", "walking_running_distance":
		return "activity", true
	default:
		return "", false
	}
}

func readinessNarrativeFresh(resp *BriefingResponse) bool {
	if resp == nil || (resp.ReadinessServing != nil && resp.ReadinessServing.Status != ReadinessServingFresh) || resp.ReadinessConfidence == ReadinessConfidenceLow {
		return false
	}
	if resp.ReadinessDisplayScore != 0 || resp.ReadinessToday != 0 {
		return true
	}
	if resp.RawMetrics == nil || resp.RawMetrics.ReadinessEvidence == nil {
		return false
	}
	for _, component := range []ReadinessComponentEvidence{resp.RawMetrics.ReadinessEvidence.HRV, resp.RawMetrics.ReadinessEvidence.RHR, resp.RawMetrics.ReadinessEvidence.SleepDuration} {
		if component.Present && component.Value != nil {
			return true
		}
	}
	return false
}

// readinessComponentNarrativeFact exposes at most the two recovery inputs
// whose own serving evidence proves a current, final, adequately covered
// aggregate. ReadinessEvidence carries no baseline; when a matching fresh
// headline provides one, it may be stated as a comparison. Otherwise the
// packet deliberately reports only the confirmed current value and coverage.
func readinessComponentNarrativeFact(resp *BriefingResponse, domains []DailyInsightDomain, locale string, component ReadinessComponentEvidence) (DailyInsightNarrativeFact, bool) {
	if resp == nil || resp.RawMetrics == nil || resp.RawMetrics.ReadinessEvidence == nil || resp.RawMetrics.ReadinessEvidence.Date != resp.Date || !narrativeBriefingDateAligned(resp.Date) ||
		component.Value == nil || !component.Present || component.EvaluatedDate != resp.Date || component.SourceDate != resp.Date ||
		component.Freshness != ReadinessFreshnessOK || component.Confidence != ReadinessConfidenceFinal || component.SampleCount <= 0 {
		return DailyInsightNarrativeFact{}, false
	}
	metric, unit, id := "", "", ""
	switch component.Metric {
	case "heart_rate_variability":
		if component.SampleCount < MinSleepWindowHRVSamplesForFullConfidence {
			return DailyInsightNarrativeFact{}, false
		}
		metric, unit, id = "HRV", "ms", "readiness_hrv_current"
	case "resting_heart_rate":
		metric, unit, id = "resting heart rate", "bpm", "readiness_rhr_current"
	default:
		return DailyInsightNarrativeFact{}, false
	}
	baseline, hasBaseline := readinessComponentNarrativeBaseline(resp, domains, component.Metric, *component.Value)
	values := []string{fmt.Sprintf("%.1f", *component.Value)}
	samples := component.SampleCount
	if component.Metric == "resting_heart_rate" {
		// This counts retained RHR aggregate records, not independent pulse
		// measurements. Freshness/confidence above owns eligibility; exposing
		// the count as samples invites an unsupported reliability verdict.
		samples = 0
	} else {
		values = append(values, fmt.Sprintf("%d", samples))
	}
	window := "today, confirmed same-day coverage"
	meaning := "confirmed current recovery component without inferred deviation"
	if hasBaseline {
		values = append(values, fmt.Sprintf("%.1f", baseline))
		window = "today versus confirmed personal baseline"
		meaning = "confirmed current recovery component with personal baseline"
	}
	return DailyInsightNarrativeFact{ID: id, Domain: "recovery", Meaning: meaning, Window: window, Fresh: true,
		Statement: localizedNarrativeRecoveryComponentFact(locale, metric, unit, *component.Value, samples, baseline, hasBaseline), DisplayValues: values, EvidenceIDs: []string{id}}, true
}

func readinessComponentNarrativeBaseline(resp *BriefingResponse, domains []DailyInsightDomain, metric string, value float64) (float64, bool) {
	if resp == nil || resp.Headline == nil || !headlineNarrativeFactFresh(resp, domains, "recovery", metric) {
		return 0, false
	}
	for _, candidate := range resp.Headline.Metrics {
		if candidate.Metric == metric && candidate.Baseline > 0 && math.Abs(candidate.Value-value) < 0.0001 {
			return candidate.Baseline, true
		}
	}
	return 0, false
}

// boundedSleepPatternFact summarizes only a closed four-day calendar window.
// It emits no dates, records, source names, or individual-day values.
func boundedSleepPatternFact(daily []DailyHealthMetrics, lastDate, locale string) (DailyInsightNarrativeFact, bool) {
	last, ok := parseDailyNarrativeDate(lastDate)
	if !ok || len(daily) < 4 {
		return DailyInsightNarrativeFact{}, false
	}
	var newest, prior float64
	var previous time.Time
	for index := 0; index < 4; index++ {
		date, ok := parseDailyNarrativeDate(daily[index].Date)
		if !ok || daily[index].Sleep == nil || (index == 0 && !date.Equal(last)) || (index > 0 && previous.Sub(date) != 24*time.Hour) {
			return DailyInsightNarrativeFact{}, false
		}
		previous = date
		if index < 2 {
			newest += *daily[index].Sleep
		} else {
			prior += *daily[index].Sleep
		}
	}
	newest, prior = newest/2, prior/2
	return DailyInsightNarrativeFact{ID: "sleep_recent_four_day_pattern", Domain: "sleep", Meaning: "bounded recent sleep pattern without a causal claim", Window: "most recent four calendar days; newest two versus preceding two", Fresh: true, Statement: localizedNarrativeSleepPatternFact(locale, newest, prior), DisplayValues: []string{fmt.Sprintf("%.1f", newest), fmt.Sprintf("%.1f", prior)}, EvidenceIDs: []string{"sleep_recent_four_day_pattern"}}, true
}

// boundedActivityTrendFact compares two complete five-day aggregates. It is
// deliberately activity-only so it does not present overlapping sleep or
// readiness inputs as independent evidence.
func boundedActivityTrendFact(daily []DailyHealthMetrics, lastDate, locale string) (DailyInsightNarrativeFact, bool) {
	last, ok := parseDailyNarrativeDate(lastDate)
	if !ok || len(daily) < 11 {
		return DailyInsightNarrativeFact{}, false
	}
	// Daily is canonical newest-first. Require the current-day row explicitly,
	// then ignore it: activity totals for LastDate are intraday and must not
	// masquerade as a completed-day trend.
	current, ok := parseDailyNarrativeDate(daily[0].Date)
	if !ok || !current.Equal(last) {
		return DailyInsightNarrativeFact{}, false
	}
	var newest, prior float64
	for index := 1; index <= 10; index++ {
		date, ok := parseDailyNarrativeDate(daily[index].Date)
		expected := last.AddDate(0, 0, -index)
		if !ok || !date.Equal(expected) || daily[index].Steps == nil {
			return DailyInsightNarrativeFact{}, false
		}
		if index <= 5 {
			newest += *daily[index].Steps
		} else {
			prior += *daily[index].Steps
		}
	}
	newest, prior = newest/5, prior/5
	return DailyInsightNarrativeFact{ID: "activity_recent_steps_trend", Domain: "activity", Meaning: "bounded completed-day activity trend", Window: "ten completed calendar days before the current day; newest five versus preceding five", Fresh: true, Statement: localizedNarrativeActivityTrendFact(locale, newest, prior), DisplayValues: []string{fmt.Sprintf("%.0f", newest), fmt.Sprintf("%.0f", prior)}, EvidenceIDs: []string{"activity_recent_steps_trend"}}, true
}

func parseDailyNarrativeDate(value string) (time.Time, bool) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil || date.Format("2006-01-02") != value {
		return time.Time{}, false
	}
	return date, true
}

func localizedNarrativeSleepPatternFact(locale string, newest, prior float64) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("В закрытом окне из четырёх дней среднее за последние два: %.1f ч, перед ними: %.1f ч.", newest, prior)
	case "sr":
		return fmt.Sprintf("U zatvorenom prozoru od četiri dana prosek za poslednja dva iznosi %.1f h, a pre njih %.1f h.", newest, prior)
	default:
		return fmt.Sprintf("In a closed four-day window, the newest two-day average is %.1f h versus %.1f h before that.", newest, prior)
	}
}

func localizedNarrativeActivityTrendFact(locale string, newest, prior float64) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("В закрытом окне из десяти завершённых дней средняя активность за последние пять: %.0f шагов, перед ними: %.0f.", newest, prior)
	case "sr":
		return fmt.Sprintf("U zatvorenom prozoru od deset završenih dana prosečna aktivnost poslednjih pet je %.0f koraka, a pre njih %.0f.", newest, prior)
	default:
		return fmt.Sprintf("Across ten completed days before today, the newest five-day activity average is %.0f steps versus %.0f before that.", newest, prior)
	}
}

func energyNarrativeFresh(bank *EnergyBank) bool {
	if bank == nil {
		return false
	}
	for _, flag := range bank.Flags {
		if flag == "stale_stress" || flag == "data_accruing" {
			return false
		}
	}
	return bank.ActionVerdict != ""
}

func localizedNarrativeSleepFact(locale string, latest, average float64) string {
	if average <= 0 {
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Каноническая длительность сна прошлой ночью: %.1f ч.", latest)
		case "sr":
			return fmt.Sprintf("Kanonsko trajanje sna prošle noći: %.1f h.", latest)
		default:
			return fmt.Sprintf("Canonical sleep duration last night: %.1f h.", latest)
		}
	}
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Каноническая длительность сна: %.1f ч при недавнем среднем %.1f ч.", latest, average)
	case "sr":
		return fmt.Sprintf("Kanonsko trajanje sna: %.1f h uz skorašnji prosek %.1f h.", latest, average)
	default:
		return fmt.Sprintf("Canonical sleep duration: %.1f h against a recent average of %.1f h.", latest, average)
	}
}

func localizedNarrativePartialSleepFact(locale string, recorded float64) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Текущая записанная длительность сна: %.1f ч. Доступные данные не подтверждают полноту ночи или качество сна.", recorded)
	case "sr":
		return fmt.Sprintf("Trenutno zabeleženo trajanje sna je %.1f h. Dostupni podaci ne potvrđuju potpunost noći ni kvalitet sna.", recorded)
	default:
		return fmt.Sprintf("Currently recorded sleep duration is %.1f h. Available data do not confirm night completeness or sleep quality.", recorded)
	}
}

func localizedNarrativeSleepQualityFact(locale string, score int) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Серверная оценка качества сна: %d%%.", score)
	case "sr":
		return fmt.Sprintf("Serverska procena kvaliteta sna: %d%%.", score)
	default:
		return fmt.Sprintf("Server-derived sleep quality: %d%%.", score)
	}
}

func localizedNarrativeReadinessFact(locale string, score int, label string, serving *ReadinessServingState) string {
	state := "fresh"
	if serving != nil && serving.Status != "" {
		state = serving.Status
	}
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Текущая готовность: %d%%, %s; состояние данных: %s.", score, label, state)
	case "sr":
		return fmt.Sprintf("Trenutna spremnost: %d%%, %s; stanje podataka: %s.", score, label, state)
	default:
		return fmt.Sprintf("Current readiness: %d%%, %s; data state: %s.", score, label, state)
	}
}

func localizedNarrativeRecoveryComponentFact(locale, metric, unit string, value float64, samples int, baseline float64, hasBaseline bool) string {
	metric = localizedNarrativeRecoveryMetric(locale, metric)
	coverage := ""
	if samples > 0 {
		coverage = "; " + localizedNarrativeRecoverySampleCount(locale, samples)
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			coverage += " за этот день"
		case "sr":
			coverage += " za taj dan"
		}
	}
	if hasBaseline {
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Подтверждённое текущее значение %s: %.1f %s при личной базе %.1f %s%s.", metric, value, unit, baseline, unit, coverage)
		case "sr":
			return fmt.Sprintf("Potvrđena današnja vrednost %s: %.1f %s uz ličnu osnovu %.1f %s%s.", metric, value, unit, baseline, unit, coverage)
		default:
			return fmt.Sprintf("Confirmed current %s: %.1f %s against a personal baseline of %.1f %s%s.", metric, value, unit, baseline, unit, coverage)
		}
	}
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Подтверждённое текущее значение %s: %.1f %s%s.", metric, value, unit, coverage)
	case "sr":
		return fmt.Sprintf("Potvrđena današnja vrednost %s: %.1f %s%s.", metric, value, unit, coverage)
	default:
		return fmt.Sprintf("Confirmed current %s: %.1f %s%s.", metric, value, unit, coverage)
	}
}

func localizedNarrativeRecoveryMetric(locale, metric string) string {
	if metric != "resting heart rate" {
		return metric
	}
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return "пульса в покое"
	case "sr":
		return "pulsa u mirovanju"
	default:
		return metric
	}
}

func localizedNarrativeRecoverySampleCount(locale string, samples int) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		mod100 := samples % 100
		if mod100 >= 11 && mod100 <= 14 {
			return fmt.Sprintf("%d измерений", samples)
		}
		switch samples % 10 {
		case 1:
			return fmt.Sprintf("%d измерение", samples)
		case 2, 3, 4:
			return fmt.Sprintf("%d измерения", samples)
		default:
			return fmt.Sprintf("%d измерений", samples)
		}
	case "sr":
		mod100 := samples % 100
		if mod100 < 11 || mod100 > 14 {
			if samples%10 == 1 {
				return fmt.Sprintf("%d merenje", samples)
			}
		}
		return fmt.Sprintf("%d merenja", samples)
	default:
		if samples == 1 {
			return "1 same-day sample"
		}
		return fmt.Sprintf("%d same-day samples", samples)
	}
}

func localizedNarrativeHeadlineFact(locale string, metric HeadlineMetricDelta) string {
	if metric.Baseline <= 0 {
		switch normalizeDailyInsightLocale(locale) {
		case "ru":
			return fmt.Sprintf("Текущее серверное значение %s: %.1f.", metric.Metric, metric.Value)
		case "sr":
			return fmt.Sprintf("Trenutna serverska vrednost %s: %.1f.", metric.Metric, metric.Value)
		default:
			return fmt.Sprintf("Current server-derived %s value: %.1f.", metric.Metric, metric.Value)
		}
	}
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("Серверный сдвиг %s: %.1f при базе %.1f.", metric.Metric, metric.Value, metric.Baseline)
	case "sr":
		return fmt.Sprintf("Serversko odstupanje %s: %.1f uz osnovu %.1f.", metric.Metric, metric.Value, metric.Baseline)
	default:
		return fmt.Sprintf("Server-derived %s shift: %.1f against a %.1f baseline.", metric.Metric, metric.Value, metric.Baseline)
	}
}

func localizedNarrativeEnergyFact(locale string, bank *EnergyBank) string {
	switch normalizeDailyInsightLocale(locale) {
	case "ru":
		return fmt.Sprintf("EnergyBank: запас %d из %d, расход %d, нагрузка %d, стресс %d.", bank.Current, bank.Capacity, bank.DrainSoFar, bank.Strain, bank.Stress)
	case "sr":
		return fmt.Sprintf("EnergyBank: rezerva %d od %d, potrošnja %d, opterećenje %d, stres %d.", bank.Current, bank.Capacity, bank.DrainSoFar, bank.Strain, bank.Stress)
	default:
		return fmt.Sprintf("EnergyBank: reserve %d of %d, drain %d, strain %d, stress %d.", bank.Current, bank.Capacity, bank.DrainSoFar, bank.Strain, bank.Stress)
	}
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
	parts = append(parts, snapshot.DecisionEvidenceDomains...)
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

// DailyInsightNarrativeSlotMaterialHash binds one durable narrative to the
// exact closed claim packet it may explain. Unlike the snapshot hash, it does
// not change when another domain receives late data.
func DailyInsightNarrativeSlotMaterialHash(snapshot *DailyInsightSnapshot, locale, slot string) string {
	input, known := BuildDailyInsightNarrativeSlotInput(snapshot, locale, slot)
	if !known {
		return ""
	}
	payload, err := jsonMarshalDailyInsightNarrativeSlotInput(struct {
		Input                     DailyInsightNarrativeSlotInput `json:"input"`
		MeaningCatalogFingerprint string                         `json:"meaning_catalog_fingerprint"`
		PromptRevision            string                         `json:"prompt_revision"`
		NarrativeVersion          string                         `json:"narrative_version"`
		SnapshotVersion           string                         `json:"snapshot_version"`
		PolicyVersion             string                         `json:"policy_version"`
		ActionCatalogVersion      string                         `json:"action_catalog_version"`
	}{
		Input: input, MeaningCatalogFingerprint: DailyInsightNarrativeMeaningCatalogFingerprint(),
		PromptRevision: DailyInsightPromptRevision, NarrativeVersion: DailyInsightNarrativeVersion,
		SnapshotVersion: DailyInsightSnapshotVersion, PolicyVersion: DailyInsightPolicyVersion,
		ActionCatalogVersion: DailyInsightActionCatalogVersion,
	})
	if err != nil {
		// The input is made solely of static Go structs. Treat an impossible
		// marshal failure as no usable material rather than reusing old prose.
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// DailyInsightNarrativeAnchorCatalogFingerprint identifies the retired v24
// factual-anchor catalogue. It remains available solely to validate historical
// stored sections and corpus artifacts; rich-story generation does not depend
// on it.
func DailyInsightNarrativeAnchorCatalogFingerprint() string {
	type entry struct {
		Locale string   `json:"locale"`
		ID     string   `json:"id"`
		Text   string   `json:"text"`
		Parts  []string `json:"restatement_fragments"`
	}
	entries := make([]entry, 0, 42)
	add := func(locale string, anchors []DailyInsightNarrativeAnchorVariant) {
		for _, anchor := range anchors {
			entries = append(entries, entry{Locale: locale, ID: anchor.ID, Text: anchor.Text, Parts: dailyInsightNarrativeAnchorRestatementFragments(locale, anchor.ID)})
		}
	}
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, mode := range []string{"rest", "active_recovery", "push_hard", "moderate"} {
			add(locale, localizedOverallNarrativeAnchors(locale, mode))
		}
		add(locale, localizedSleepNarrativeAnchors(locale))
		for _, verdict := range []string{"push_hard", "rest", "active_recovery"} {
			add(locale, localizedEnergyNarrativeAnchors(locale, verdict))
		}
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		panic(fmt.Sprintf("marshal daily insight anchor catalog: %v", err))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// DailyInsightNarrativeMeaningCatalogFingerprint binds every server-approved
// interpretive angle to cached prose and B1 approval. A meaning-link edit can
// make a prior model output unsafe or redundant even when its factual anchor
// remains unchanged, so it must invalidate review independently of a manual
// prompt-revision bump.
func DailyInsightNarrativeMeaningCatalogFingerprint() string {
	type entry struct {
		Locale    string `json:"locale"`
		ClaimID   string `json:"claim_id"`
		ID        string `json:"id"`
		Statement string `json:"statement"`
		ActionID  string `json:"action_id,omitempty"`
	}
	entries := make([]entry, 0, 30)
	add := func(locale, claimID string, links []DailyInsightNarrativeMeaningLink) {
		for _, link := range links {
			entries = append(entries, entry{Locale: locale, ClaimID: claimID, ID: link.ID, Statement: link.Statement, ActionID: link.ActionID})
		}
	}
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, domains := range [][]string{{"sleep", "recovery"}, {"sleep", "energy"}, {"recovery", "energy"}, {"sleep", "recovery", "energy"}} {
			add(locale, "overall_daily_decision_context", overallNarrativeMeaningLinks(locale, domains))
		}
		add(locale, "recent_sleep_below_reference", sleepNarrativeMeaningLinks(locale))
		for _, verdict := range []string{"push_hard", "rest", "active_recovery"} {
			add(locale, "energy_current_verdict_context", energyNarrativeMeaningLinks(locale, verdict))
		}
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		panic(fmt.Sprintf("marshal daily insight meaning catalog: %v", err))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func jsonMarshalDailyInsightNarrativeSlotInput(input any) ([]byte, error) {
	// Keep the standard-library dependency narrow in this file’s public hash
	// path and retain deterministic struct-field ordering.
	return json.Marshal(input)
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
	primary.NarrativeSubject = decision.Mode
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
				return "Карточка сна обновится после следующей синхронизации.", "Сегодня доступны восстановление и энергия.", "sync_sleep"
			}
			if state == "partial" {
				return "Карточка сна собирается после пробуждения.", "К ней можно вернуться позже, когда будет удобнее.", "sync_sleep"
			}
			return "Карточка сна появится после следующей синхронизации.", "Сегодня доступны восстановление и энергия.", "sync_sleep"
		case "recovery":
			if state == ReadinessServingDataAccruing {
				return "Восстановление обновляется в течение утра.", "К этой карточке можно вернуться позже.", ""
			}
			return "Карточка восстановления обновится после следующей синхронизации.", "Сегодня доступны сон и энергия.", "sync_recovery"
		default:
			return "Энергия обновляется по ходу дня.", "Сегодня доступны сон и восстановление.", "sync_energy"
		}
	case "sr":
		switch domain {
		case "sleep":
			if state == "stale" {
				return "Kartica sna će se osvežiti nakon sledeće sinhronizacije.", "Danas su dostupni oporavak i energija.", "sync_sleep"
			}
			if state == "partial" {
				return "Kartica sna se popunjava nakon buđenja.", "Možete joj se vratiti kasnije, kada vam odgovara.", "sync_sleep"
			}
			return "Kartica sna će se pojaviti nakon sledeće sinhronizacije.", "Danas su dostupni oporavak i energija.", "sync_sleep"
		case "recovery":
			if state == ReadinessServingDataAccruing {
				return "Oporavak se osvežava tokom jutra.", "Možete se vratiti ovoj kartici kasnije.", ""
			}
			return "Kartica oporavka će se osvežiti nakon sledeće sinhronizacije.", "Danas su dostupni san i energija.", "sync_recovery"
		default:
			return "Energija se osvežava tokom dana.", "Danas su dostupni san i oporavak.", "sync_energy"
		}
	default:
		switch domain {
		case "sleep":
			if state == "stale" {
				return "Your sleep card will refresh after the next sync.", "Today’s recovery and energy are available now.", "sync_sleep"
			}
			if state == "partial" {
				return "Your sleep card is filling in after you wake up.", "You can come back to it later when it suits you.", "sync_sleep"
			}
			return "Your sleep card will appear after the next sync.", "Today’s recovery and energy are available now.", "sync_sleep"
		case "recovery":
			if state == ReadinessServingDataAccruing {
				return "Recovery updates through the morning.", "You can come back to this card later.", ""
			}
			return "Your recovery card will refresh after the next sync.", "Today’s sleep and energy are available now.", "sync_recovery"
		default:
			return "Energy updates through the day.", "Today’s sleep and recovery are available now.", "sync_energy"
		}
	}
}

func localizedInsightFactualContext(locale, domain string) string {
	switch locale {
	case "ru":
		switch domain {
		case "sleep":
			return "Прошедшая ночь уже стала частью сегодняшней картины."
		case "recovery":
			return "Доступные сигналы восстановления помогают задать спокойный темп дня."
		default:
			return "Текущий запас можно учитывать при выборе темпа на оставшуюся часть дня."
		}
	case "sr":
		switch domain {
		case "sleep":
			return "Prethodna noć je već deo današnje slike."
		case "recovery":
			return "Dostupni signali oporavka pomažu da se odredi mirniji tempo dana."
		default:
			return "Trenutnu rezervu možete uzeti u obzir pri izboru tempa za ostatak dana."
		}
	default:
		switch domain {
		case "sleep":
			return "Last night is already part of today’s picture."
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

func localizedRecentSleepBelowReference(locale string, shortNightCount int) (observation, meaning string) {
	if shortNightCount < 3 || shortNightCount > 4 {
		shortNightCount = 3
	}
	switch locale {
	case "ru":
		return fmt.Sprintf("%d из последних 4 ночей были короче твоей обычной продолжительности сна.", shortNightCount), "Сегодня вечером оставь место для спокойного завершения дня."
	case "sr":
		return fmt.Sprintf("%d od poslednje 4 noći bile su kraće od tvog uobičajenog sna.", shortNightCount), "Ostavi večeras prostora za mirniji završetak dana."
	default:
		return fmt.Sprintf("%d of your last 4 nights were shorter than your usual sleep.", shortNightCount), "Leave room for a quieter end to the day tonight."
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
			return "Trajanje sna je blizu ličnog proseka."
		default:
			return "Sleep duration is close to your usual average."
		}
	}
	if delta > 0 {
		switch copy.locale {
		case "ru":
			return fmt.Sprintf("На %.1f ч дольше вашего среднего.", delta)
		case "sr":
			return fmt.Sprintf("%.1f h duže od ličnog proseka.", delta)
		default:
			return fmt.Sprintf("%.1f hours longer than your usual average.", delta)
		}
	}
	switch copy.locale {
	case "ru":
		return fmt.Sprintf("На %.1f ч меньше вашего среднего.", -delta)
	case "sr":
		return fmt.Sprintf("%.1f h kraće od ličnog proseka.", -delta)
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
