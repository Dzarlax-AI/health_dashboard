package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"health-receiver/internal/health"
)

// DailyInsightNarrativeCorpus is the versioned, privacy-minimized input to the
// B1 product-quality gate. It removes identity, dates and raw records, while
// retaining the localized aggregate facts and display values that the rich
// story contract must be reviewed against.
//
// A corpus is deliberately external to the production database. Freezing its
// exact JSON and its checksum makes a model/prompt comparison reproducible
// without retaining raw records or treating a later live request as evidence.
type DailyInsightNarrativeCorpus struct {
	Version string `json:"version"`
	// PacketVersion and PacketShape bind a v2 corpus to the current
	// provider-packet builder. They are intentionally separate from the corpus
	// checksum: a perfectly intact old artifact must be re-frozen when the
	// serving packet changes.
	PacketVersion string                            `json:"packet_version,omitempty"`
	PacketShape   string                            `json:"packet_shape,omitempty"`
	Cases         []DailyInsightNarrativeCorpusCase `json:"cases"`
}

// DailyInsightNarrativeCandidateExport is an intermediate, non-evaluable
// review artifact. A human must still choose 20-30 candidates and attach the
// required state tags before it becomes a frozen corpus. Keeping that step
// explicit prevents a date scan from silently becoming a quality approval.
type DailyInsightNarrativeCandidateExport struct {
	Version    string                              `json:"version"`
	Candidates []DailyInsightNarrativeCandidate    `json:"candidates"`
	Failures   []DailyInsightNarrativeCandidateGap `json:"failures,omitempty"`
}

// DailyInsightNarrativeCandidate is a privacy-minimized review input plus structural
// hints that make manual corpus selection auditable. Hints are intentionally
// not corpus tags: facts that require independent provenance (a missing
// check-in, a late source update, or a recovery/energy conflict) must be
// confirmed by a reviewer rather than inferred from a snapshot.
type DailyInsightNarrativeCandidate struct {
	DailyInsightNarrativeCorpusCase
	ReviewHints []string `json:"review_hints,omitempty"`
}

// DailyInsightNarrativeCandidateGap contains no original date or health
// values. It records why a requested candidate could not be reconstructed.
type DailyInsightNarrativeCandidateGap struct {
	CandidateID string `json:"candidate_id"`
	Reason      string `json:"reason"`
}

type DailyInsightNarrativeCorpusCase struct {
	ID     string `json:"id"`
	Locale string `json:"locale"`
	// Origin lets a reviewer distinguish observed aggregate-state packets from
	// narrowly scoped synthetic edge fixtures. Synthetic fixtures are allowed
	// only to cover product states absent from retained history; they never
	// masquerade as evidence about a user.
	Origin            string                              `json:"origin,omitempty"` // observed_aggregate | synthetic_controlled
	Tags              []string                            `json:"tags"`
	Scenario          DailyInsightNarrativeCorpusScenario `json:"scenario,omitempty"`
	Snapshot          health.DailyInsightSnapshot         `json:"snapshot"`
	NarrativeSubjects map[string]string                   `json:"narrative_subjects,omitempty"`
	// PrimaryNarrativeSubject is the closed DailyDecision mode needed to
	// evaluate the independent overall slot without retaining a raw decision ID
	// or source-event measurements. Selected localized action copy is retained
	// as part of the rich-story packet.
	PrimaryNarrativeSubject string `json:"primary_narrative_subject,omitempty"`
	// DecisionEvidenceDomains preserves the closed provenance list used to
	// decide whether an independent domain slot receives the server position.
	// It contains only known domain keys, never a raw decision reason or value.
	DecisionEvidenceDomains []string `json:"decision_evidence_domains,omitempty"`
	PrimaryMeaningID        string   `json:"primary_meaning_id"`
	// These are the exact privacy-minimized inputs for the overall-only B1
	// request. They are ordinary corpus fields because health snapshots hide
	// live-only material from their JSON representation.
	NarrativeFacts    []health.DailyInsightNarrativeFact    `json:"narrative_facts,omitempty"`
	VisibleB0Baseline *health.DailyInsightNarrativeBaseline `json:"visible_b0_baseline,omitempty"`
	ActionOptions     []health.DailyInsightNarrativeAction  `json:"action_options,omitempty"`
}

// SnapshotForEvaluation restores the closed, non-display variants that are
// intentionally excluded from a live client snapshot. A frozen corpus must
// preserve these variants or it would review a different provider packet from
// the one that production serves. Today only energy has such a variant, and
// its value is a small server-owned enum rather than free text or a number.
func (item DailyInsightNarrativeCorpusCase) SnapshotForEvaluation() (health.DailyInsightSnapshot, error) {
	snapshot := item.Snapshot
	snapshot.Domains = append([]health.DailyInsightDomain(nil), item.Snapshot.Domains...)
	snapshot.NarrativeFacts = append([]health.DailyInsightNarrativeFact(nil), item.NarrativeFacts...)
	for index := range snapshot.NarrativeFacts {
		snapshot.NarrativeFacts[index].DisplayValues = append([]string(nil), item.NarrativeFacts[index].DisplayValues...)
		snapshot.NarrativeFacts[index].EvidenceIDs = append([]string(nil), item.NarrativeFacts[index].EvidenceIDs...)
	}
	// The existing packet builder reads actions from the closed snapshot. Keep
	// old hand-authored artifacts readable while the corpus-specific builder
	// below applies the exact frozen allow-list for new artifacts.
	for index, option := range item.ActionOptions {
		if index == 0 {
			snapshot.Primary.NextStep = &health.DailyInsightAction{ID: option.ID, Text: option.Text}
			continue
		}
		if index-1 < len(snapshot.Domains) && snapshot.Domains[index-1].Insight.NextStep == nil {
			snapshot.Domains[index-1].Insight.NextStep = &health.DailyInsightAction{ID: option.ID, Text: option.Text}
		}
	}
	if item.PrimaryNarrativeSubject != "" {
		if !validCorpusPrimaryNarrativeSubject(item.PrimaryNarrativeSubject) {
			return health.DailyInsightSnapshot{}, fmt.Errorf("case %q has unsupported primary narrative subject %q", item.ID, item.PrimaryNarrativeSubject)
		}
		if snapshot.DecisionID == "" || snapshot.Primary.NextStep == nil || len(snapshot.Primary.EvidenceIDs) == 0 {
			return health.DailyInsightSnapshot{}, fmt.Errorf("case %q has incomplete overall narrative context", item.ID)
		}
		snapshot.Primary.NarrativeSubject = item.PrimaryNarrativeSubject
	}
	for _, domain := range item.DecisionEvidenceDomains {
		if domain != "sleep" && domain != "recovery" && domain != "energy" {
			return health.DailyInsightSnapshot{}, fmt.Errorf("case %q has unsupported decision evidence domain %q", item.ID, domain)
		}
		if !containsCorpusString(snapshot.DecisionEvidenceDomains, domain) {
			snapshot.DecisionEvidenceDomains = append(snapshot.DecisionEvidenceDomains, domain)
		}
	}
	if len(item.NarrativeSubjects) == 0 {
		return snapshot, nil
	}
	for key, subject := range item.NarrativeSubjects {
		if key != "energy" || !validCorpusEnergyNarrativeSubject(subject) {
			return health.DailyInsightSnapshot{}, fmt.Errorf("case %q has unsupported narrative subject %q for domain %q", item.ID, subject, key)
		}
		found := false
		for index := range snapshot.Domains {
			if snapshot.Domains[index].Key == key {
				snapshot.Domains[index].NarrativeSubject = subject
				found = true
				break
			}
		}
		if !found {
			return health.DailyInsightSnapshot{}, fmt.Errorf("case %q has narrative subject for missing domain %q", item.ID, key)
		}
	}
	return snapshot, nil
}

// BuildDailyInsightNarrativeCorpusSlotInput restores the exact frozen B0 and
// action context after deriving the normal provider packet from the sanitized
// snapshot. Production generation never calls this helper.
func BuildDailyInsightNarrativeCorpusSlotInput(item DailyInsightNarrativeCorpusCase, locale, slot string) (health.DailyInsightNarrativeSlotInput, bool, error) {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return health.DailyInsightNarrativeSlotInput{}, false, err
	}
	input, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, slot)
	if !known || slot != health.DailyInsightNarrativeOverallSlot {
		return input, known, nil
	}
	if item.VisibleB0Baseline != nil {
		baseline := *item.VisibleB0Baseline
		baseline.Domains = append([]health.DailyInsightBaselineDomain(nil), item.VisibleB0Baseline.Domains...)
		input.Slot.Baseline = &baseline
	}
	if item.ActionOptions != nil {
		input.Slot.ActionOptions = append([]health.DailyInsightNarrativeAction(nil), item.ActionOptions...)
	}
	return input, true, nil
}

func validCorpusEnergyNarrativeSubject(value string) bool {
	switch value {
	case "rest", "active_recovery", "push_hard":
		return true
	default:
		return false
	}
}

func validCorpusPrimaryNarrativeSubject(value string) bool {
	switch value {
	case "rest", "active_recovery", "moderate", "push_hard":
		return true
	default:
		return false
	}
}

// SanitizeDailyInsightNarrativeCorpusCandidate removes identifiers, dates and
// raw records while retaining localized aggregate copy, display values and an
// already-selected action. The rich-story contract needs these semantics: a
// review that replaces them with opaque bands cannot tell whether the prose is
// useful. The result is still a candidate, not a frozen corpus; callers must
// supply review tags and non-serving scenario provenance separately.
func SanitizeDailyInsightNarrativeCorpusCandidate(snapshot health.DailyInsightSnapshot, locale, candidateID string) DailyInsightNarrativeCorpusCase {
	evidenceIDs := make(map[string]string, len(snapshot.Evidence))
	for index, evidence := range snapshot.Evidence {
		evidenceIDs[evidence.ID] = fmt.Sprintf("evidence-%02d", index+1)
	}
	mapEvidenceIDs := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if replacement, found := evidenceIDs[id]; found {
				out = append(out, replacement)
			}
		}
		return out
	}

	sanitized := health.DailyInsightSnapshot{
		Version:    snapshot.Version,
		Domains:    make([]health.DailyInsightDomain, 0, len(snapshot.Domains)),
		Evidence:   make([]health.DailyInsightEvidence, 0, len(snapshot.Evidence)),
		Changes:    []health.DailyInsightChange{},
		HasMore:    false,
		DecisionID: "",
		Primary:    health.DailyInsight{},
	}
	for _, domain := range snapshot.Domains {
		sanitized.Domains = append(sanitized.Domains, health.DailyInsightDomain{
			Key:        domain.Key,
			Band:       domain.Band,
			DataState:  domain.DataState,
			Confidence: domain.Confidence,
			Summary:    domain.Summary,
			Insight: health.DailyInsight{
				State:       domain.Insight.State,
				AnswerKind:  domain.Insight.AnswerKind,
				ClaimID:     domain.Insight.ClaimID,
				GapReason:   domain.Insight.GapReason,
				Remediation: domain.Insight.Remediation,
				Observation: domain.Insight.Observation,
				Meaning:     domain.Insight.Meaning,
				EvidenceIDs: mapEvidenceIDs(domain.Insight.EvidenceIDs),
				Fallback:    domain.Insight.Fallback,
				NextStep:    sanitizedNarrativeDomainAction(domain.Insight.NextStep),
			},
		})
	}
	narrativeFacts := make([]health.DailyInsightNarrativeFact, 0, len(snapshot.NarrativeFacts))
	for index, fact := range snapshot.NarrativeFacts {
		fact.EvidenceIDs = mapEvidenceIDs(fact.EvidenceIDs)
		if len(fact.EvidenceIDs) == 0 {
			fact.EvidenceIDs = []string{fmt.Sprintf("evidence-%02d", index+1)}
		}
		narrativeFacts = append(narrativeFacts, fact)
	}
	for index, evidence := range snapshot.Evidence {
		sanitized.Evidence = append(sanitized.Evidence, health.DailyInsightEvidence{
			ID:               fmt.Sprintf("evidence-%02d", index+1),
			Domain:           evidence.Domain,
			ComparisonPeriod: evidence.ComparisonPeriod,
			DataState:        evidence.DataState,
			Confidence:       evidence.Confidence,
			Value:            evidence.Value,
			Baseline:         evidence.Baseline,
			Delta:            evidence.Delta,
			Unit:             evidence.Unit,
		})
	}
	primarySubject := ""
	if snapshot.DecisionID != "" && snapshot.Primary.NextStep != nil && len(snapshot.Primary.EvidenceIDs) > 0 && validCorpusPrimaryNarrativeSubject(snapshot.Primary.NarrativeSubject) {
		primarySubject = snapshot.Primary.NarrativeSubject
		sanitized.DecisionID = "review-decision"
		sanitized.Primary = health.DailyInsight{
			State: snapshot.Primary.State, AnswerKind: snapshot.Primary.AnswerKind, ClaimID: snapshot.Primary.ClaimID,
			GapReason: snapshot.Primary.GapReason, Remediation: snapshot.Primary.Remediation,
			Observation: snapshot.Primary.Observation, Meaning: snapshot.Primary.Meaning,
			EvidenceIDs: mapEvidenceIDs(snapshot.Primary.EvidenceIDs), Fallback: snapshot.Primary.Fallback,
			NextStep: &health.DailyInsightAction{ID: "review-action", Text: snapshot.Primary.NextStep.Text},
		}
	}
	subjects := make(map[string]string)
	for _, domain := range snapshot.Domains {
		if domain.Key == "energy" && validCorpusEnergyNarrativeSubject(domain.NarrativeSubject) {
			subjects[domain.Key] = domain.NarrativeSubject
		}
	}
	if len(subjects) == 0 {
		subjects = nil
	}
	input, _ := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, health.DailyInsightNarrativeOverallSlot)
	return DailyInsightNarrativeCorpusCase{
		ID:                      candidateID,
		Locale:                  locale,
		Origin:                  DailyInsightNarrativeOriginObserved,
		Snapshot:                sanitized,
		NarrativeSubjects:       subjects,
		PrimaryNarrativeSubject: primarySubject,
		DecisionEvidenceDomains: append([]string(nil), snapshot.DecisionEvidenceDomains...),
		PrimaryMeaningID:        narrativeCorpusPrimaryMeaningID(snapshot),
		NarrativeFacts:          narrativeFacts,
		VisibleB0Baseline:       input.Slot.Baseline,
		ActionOptions:           append([]health.DailyInsightNarrativeAction(nil), input.Slot.ActionOptions...),
	}
}

func containsCorpusString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func sanitizedNarrativeDomainAction(action *health.DailyInsightAction) *health.DailyInsightAction {
	if action == nil || action.ID != "wind_down" {
		return nil
	}
	return &health.DailyInsightAction{ID: "wind_down", Text: action.Text}
}

// narrativeCorpusPrimaryMeaningID carries which closed domain meaning the
// already-rendered hero is based on. The decision hash is deliberately not
// retained; privacy-minimized aggregate copy and values are retained for the
// rich-story evaluation packet.
func narrativeCorpusPrimaryMeaningID(snapshot health.DailyInsightSnapshot) string {
	for _, primaryEvidenceID := range snapshot.Primary.EvidenceIDs {
		for _, domain := range snapshot.Domains {
			if !containsCorpusEvidenceID(domain.Insight.EvidenceIDs, primaryEvidenceID) {
				continue
			}
			return narrativeCorpusPrimaryMeaningForDomain(domain)
		}
	}
	return ""
}

func narrativeCorpusPrimaryMeaningForDomain(domain health.DailyInsightDomain) string {
	meaning := domain.Insight.ClaimID
	if meaning == "" {
		meaning = domain.Insight.AnswerKind
	}
	if meaning == "" {
		meaning = domain.Insight.State
	}
	return "primary:" + domain.Key + ":" + meaning
}

// DailyInsightNarrativeCandidateReviewHints derives only closed, structural
// selection aids from an already sanitized snapshot. These labels are never
// sent to a provider and are deliberately distinct from required corpus tags,
// whose stricter provenance is checked by ValidateDailyInsightNarrativeCorpus.
func DailyInsightNarrativeCandidateReviewHints(item DailyInsightNarrativeCorpusCase) []string {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return nil
	}
	hints := make([]string, 0, 4)
	if sleep, found := narrativeCorpusDomain(snapshot, "sleep"); found {
		switch {
		case sleep.DataState == "fresh" && sleep.Confidence == "final":
			hints = append(hints, "sleep_fresh_final")
		case sleep.DataState == "partial" || sleep.DataState == "missing" || sleep.DataState == "stale":
			hints = append(hints, "sleep_incomplete")
		case sleep.Insight.AnswerKind == health.DailyInsightAnswerProvisional && sleep.Insight.ClaimID == "":
			hints = append(hints, "sleep_limited_history")
		}
	}
	if sleep, found := narrativeCorpusDomain(snapshot, "sleep"); found && snapshot.Primary.State == "insight" && snapshot.Primary.AnswerKind == health.DailyInsightAnswerFactual && snapshot.Primary.NarrativeSubject == "moderate" && sleep.DataState == "fresh" && sleep.Confidence == "final" {
		hints = append(hints, "normal_context")
	}
	if recovery, found := narrativeCorpusDomain(snapshot, "recovery"); found && recovery.DataState == "fresh" && recovery.Confidence == "final" && recovery.Band == "optimal" {
		hints = append(hints, "positive_context")
	}
	for _, domain := range snapshot.Domains {
		if domain.Insight.AnswerKind == health.DailyInsightAnswerProvisional && domain.Insight.ClaimID == "" {
			hints = append(hints, "provisional_context")
			break
		}
	}
	if health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) {
		hints = append(hints, "narrative_eligible")
	}
	if narrativeCandidateHasBothFreshFinalDomains(snapshot, "recovery", "energy") {
		hints = append(hints, "recovery_energy_both_final")
	}
	if narrativeCorpusHasOnlyUnavailableDomains(snapshot) {
		hints = append(hints, "all_domains_unavailable")
	}
	return hints
}

func narrativeCandidateHasBothFreshFinalDomains(snapshot health.DailyInsightSnapshot, first, second string) bool {
	firstDomain, firstFound := narrativeCorpusDomain(snapshot, first)
	secondDomain, secondFound := narrativeCorpusDomain(snapshot, second)
	return firstFound && secondFound &&
		firstDomain.DataState == "fresh" && firstDomain.Confidence == "final" &&
		secondDomain.DataState == "fresh" && secondDomain.Confidence == "final"
}

// DailyInsightNarrativeCorpusScenario holds the deliberately non-serving
// context needed to audit difficult product cases. It is never sent to the
// provider. Snapshot fields remain the source of truth for claims; these
// annotations bind cases such as an absent check-in or a late source update
// to reviewable provenance that the Today snapshot intentionally does not
// expose.
type DailyInsightNarrativeCorpusScenario struct {
	CheckIn             string            `json:"checkin,omitempty"`               // absent | answered
	UpdateKind          string            `json:"update_kind,omitempty"`           // late_source_update
	ConflictEvidenceIDs map[string]string `json:"conflict_evidence_ids,omitempty"` // recovery/energy evidence IDs
}

// RequiredDailyInsightNarrativeCorpusTags capture the product states that
// must be represented before B1 can be considered for release. A case may
// carry several tags; the corpus remains bounded so review stays humane.
var RequiredDailyInsightNarrativeCorpusTags = []string{
	"mixed_sleep_baseline",
	"complete_sleep",
	"incomplete_sleep",
	"limited_history",
	"no_checkin",
	"energy_recovery_conflict",
	"late_source_update",
	"no_data",
	"normal_context",
	"positive_context",
	"provisional_context",
}

// RequiredDailyInsightNarrativeClaimIDs are the complete B1 claim catalogue.
// A frozen corpus must contain every supported claim in every shipped locale.
// This measures provider behavior across the actual localized claim contract.
// Standalone Recovery and Energy verdicts are deliberately deterministic-only:
// they remain server facts and can support a combined overall explanation, but
// are never represented as provider prose on their own.
var RequiredDailyInsightNarrativeClaimIDs = []string{
	"overall_daily_decision_context",
	"recent_sleep_below_reference",
}

// RequiredDailyInsightNarrativeMeaningIDs are server-owned interpretive
// variants which must have explicit frozen-corpus coverage before B1 can be
// approved. Each provider-served meaning has explicit review coverage, so the
// review cannot approve a lively overall narrative while leaving another
// enabled narrative path untested.
var RequiredDailyInsightNarrativeMeaningIDs = []string{
	"overall_combined_context",
	"sleep_personal_reference",
}

// RequiredDailyInsightNarrativeV2Tags cover the dimensions of the
// overall-only corpus. The v2 gate deliberately does not require every legacy
// claim or meaning ID: the model is evaluated on how well it interprets a
// bounded combination of server-derived facts.
var RequiredDailyInsightNarrativeV2Tags = []string{
	"facts_sleep_recovery",
	"facts_sleep_energy",
	"facts_recovery_energy",
	"action_options",
	"no_action",
	"fresh_data",
	"missing_data",
	"safety_control",
	"locale_en",
	"locale_ru",
	"locale_sr",
}

const (
	DailyInsightNarrativeCorpusVersionV1         = "daily-insight-narrative-corpus-v1"
	DailyInsightNarrativeCorpusVersionV2         = "daily-insight-narrative-corpus-v2"
	DailyInsightNarrativeCorpusMinCases          = 20
	DailyInsightNarrativeCorpusMaxCases          = 30
	DailyInsightNarrativeCorpusMaxSyntheticCases = 5

	DailyInsightNarrativeOriginObserved  = "observed_aggregate"
	DailyInsightNarrativeOriginSynthetic = "synthetic_controlled"
)

// DailyInsightNarrativeCorpusHash is the stable review identity of a corpus.
// It hashes canonical JSON rather than bytes read from a file, so harmless
// indentation changes cannot sever the review record from the same frozen
// point-in-time cases. Semantic changes, including a changed scenario field,
// produce a new hash.
func DailyInsightNarrativeCorpusHash(corpus DailyInsightNarrativeCorpus) (string, error) {
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(corpus)
	if err != nil {
		return "", fmt.Errorf("encode canonical corpus: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// DailyInsightNarrativeCorpusCurrentPacketShape returns a deterministic
// fingerprint of the current v2 packet contract and a fully-populated builder
// probe. It catches an unversioned JSON-shape or selection change before an
// old frozen corpus can certify the current serving builder.
func DailyInsightNarrativeCorpusCurrentPacketShape() string {
	factIDs := make([]string, 0, len(v2CorpusFactTaxonomy)+len(v2CorpusHeadlineMetricSlugs))
	for id := range v2CorpusFactTaxonomy {
		factIDs = append(factIDs, id)
	}
	for slug := range v2CorpusHeadlineMetricSlugs {
		factIDs = append(factIDs, "headline_"+slug)
	}
	sort.Strings(factIDs)
	facts := make([]health.DailyInsightNarrativeFact, 0, len(factIDs))
	for _, id := range factIDs {
		facts = append(facts, health.DailyInsightNarrativeFact{
			ID: id, Domain: v2CorpusFactDomain(id), Authority: "server_derived", Fresh: true,
			Statement: "packet-shape " + id, DisplayValues: []string{"1"}, EvidenceIDs: []string{id},
		})
	}
	probe := health.DailyInsightSnapshot{
		Version: health.DailyInsightSnapshotVersion,
		Domains: []health.DailyInsightDomain{
			{Key: "sleep", DataState: "fresh", Confidence: "final"},
			{Key: "recovery", DataState: "fresh", Confidence: "final"},
			{Key: "energy", DataState: "fresh", Confidence: "final"},
			{Key: "activity", DataState: "fresh", Confidence: "final"},
		},
		NarrativeFacts: facts,
		Primary:        health.DailyInsight{NextStep: &health.DailyInsightAction{ID: "wind_down", Text: "packet-shape action"}},
	}
	input, known := health.BuildDailyInsightNarrativeSlotInput(&probe, "en", health.DailyInsightNarrativeOverallSlot)
	if !known {
		panic("overall daily insight narrative slot is unavailable")
	}
	payload := struct {
		PacketVersion string                                `json:"packet_version"`
		InputShape    any                                   `json:"input_shape"`
		FactIDs       []string                              `json:"fact_ids"`
		ActionIDs     []string                              `json:"action_ids"`
		BuilderProbe  health.DailyInsightNarrativeSlotInput `json:"builder_probe"`
	}{
		PacketVersion: health.DailyInsightNarrativeInputVersion,
		InputShape:    narrativeCorpusJSONShape(reflect.TypeOf(health.DailyInsightNarrativeSlotInput{})),
		FactIDs:       factIDs,
		ActionIDs:     []string{"wind_down", "daily-decision-rest", "daily-decision-active_recovery", "daily-decision-moderate"},
		BuilderProbe:  input,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("marshal B1 corpus packet shape: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func narrativeCorpusJSONShape(t reflect.Type) any {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return t.String()
	}
	type field struct {
		Name  string `json:"name"`
		JSON  string `json:"json"`
		Shape any    `json:"shape"`
	}
	fields := make([]field, 0, t.NumField())
	for index := 0; index < t.NumField(); index++ {
		item := t.Field(index)
		if item.PkgPath != "" || strings.Split(item.Tag.Get("json"), ",")[0] == "-" {
			continue
		}
		fields = append(fields, field{Name: item.Name, JSON: item.Tag.Get("json"), Shape: narrativeCorpusJSONShape(item.Type)})
	}
	return fields
}

// ValidateDailyInsightNarrativeCorpus validates the frozen gate input before
// any provider call. It rejects an under-sized hand-picked happy path, a
// missing required product state, and malformed claim eligibility.
func ValidateDailyInsightNarrativeCorpus(corpus DailyInsightNarrativeCorpus) error {
	if corpus.Version != DailyInsightNarrativeCorpusVersionV1 && corpus.Version != DailyInsightNarrativeCorpusVersionV2 {
		return fmt.Errorf("unsupported corpus version %q", corpus.Version)
	}
	v2 := corpus.Version == DailyInsightNarrativeCorpusVersionV2
	if len(corpus.Cases) < DailyInsightNarrativeCorpusMinCases || len(corpus.Cases) > DailyInsightNarrativeCorpusMaxCases {
		return fmt.Errorf("corpus has %d cases; want %d to %d", len(corpus.Cases), DailyInsightNarrativeCorpusMinCases, DailyInsightNarrativeCorpusMaxCases)
	}
	ids := make(map[string]struct{}, len(corpus.Cases))
	claimCoverage := make(map[string]map[string]struct{})
	meaningCoverage := make(map[string]map[string]struct{})
	syntheticCases := 0
	for index, item := range corpus.Cases {
		if item.ID == "" {
			return fmt.Errorf("case %d has empty id", index)
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("duplicate case id %q", item.ID)
		}
		ids[item.ID] = struct{}{}
		if item.Locale != "en" && item.Locale != "ru" && item.Locale != "sr" {
			return fmt.Errorf("case %q has unsupported locale %q", item.ID, item.Locale)
		}
		origin := item.Origin
		if origin == "" {
			// Keep old manually prepared v1 corpus files readable while the
			// scaffold and candidate exporter write the explicit value.
			origin = DailyInsightNarrativeOriginObserved
		}
		switch origin {
		case DailyInsightNarrativeOriginObserved:
			if containsCorpusTag(item.Tags, DailyInsightNarrativeOriginSynthetic) {
				return fmt.Errorf("case %q marks observed aggregate data as synthetic", item.ID)
			}
		case DailyInsightNarrativeOriginSynthetic:
			syntheticCases++
			if !containsCorpusTag(item.Tags, DailyInsightNarrativeOriginSynthetic) {
				return fmt.Errorf("synthetic case %q must carry the synthetic_controlled tag", item.ID)
			}
			if (!v2 && !containsRequiredCorpusTag(item.Tags)) || (v2 && !containsAnyCorpusTag(item.Tags, RequiredDailyInsightNarrativeV2Tags)) {
				return fmt.Errorf("synthetic case %q must cover at least one required product state", item.ID)
			}
		default:
			return fmt.Errorf("case %q has unsupported origin %q", item.ID, item.Origin)
		}
		if (!v2 && item.Snapshot.Date == "") || item.Snapshot.Version == "" || len(item.Snapshot.Domains) == 0 {
			return fmt.Errorf("case %q has incomplete sanitized snapshot", item.ID)
		}
		if !v2 && !narrativeCorpusPrimaryMeaningMatches(snapshotForCorpusCase(item), item.PrimaryMeaningID) {
			return fmt.Errorf("case %q has missing or unsupported primary meaning id", item.ID)
		}
		if v2 {
			if err := validateV2CorpusPrivacy(item); err != nil {
				return err
			}
		}
		for _, tag := range item.Tags {
			if tag == "" {
				return fmt.Errorf("case %q has empty tag", item.ID)
			}
		}
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			return err
		}
		if !v2 {
			if err := validateDailyInsightNarrativeCorpusCase(item, snapshot); err != nil {
				return err
			}
		}
		for _, domain := range dailyInsightNarrativeCorpusCoverageInputs(snapshot, item.Locale) {
			for _, claim := range domain.Claims {
				if claimCoverage[item.Locale] == nil {
					claimCoverage[item.Locale] = make(map[string]struct{})
				}
				claimCoverage[item.Locale][claim.ID] = struct{}{}
				if meaningCoverage[item.Locale] == nil {
					meaningCoverage[item.Locale] = make(map[string]struct{})
				}
				for _, meaning := range claim.MeaningLinks {
					meaningCoverage[item.Locale][meaning.ID] = struct{}{}
				}
			}
		}
	}
	if v2 {
		return validateV2CorpusCoverage(corpus)
	}
	tags := make(map[string]struct{})
	for _, item := range corpus.Cases {
		for _, tag := range item.Tags {
			tags[tag] = struct{}{}
		}
	}
	missing := make([]string, 0)
	for _, required := range RequiredDailyInsightNarrativeCorpusTags {
		if _, present := tags[required]; !present {
			missing = append(missing, required)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("corpus is missing required tags: %v", missing)
	}
	missingClaimCoverage := make([]string, 0)
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, claimID := range RequiredDailyInsightNarrativeClaimIDs {
			if _, present := claimCoverage[locale][claimID]; !present {
				missingClaimCoverage = append(missingClaimCoverage, locale+":"+claimID)
			}
		}
	}
	if len(missingClaimCoverage) != 0 {
		return fmt.Errorf("corpus is missing narrative claim coverage: %v", missingClaimCoverage)
	}
	missingMeaningCoverage := make([]string, 0)
	for _, locale := range []string{"en", "ru", "sr"} {
		for _, meaningID := range RequiredDailyInsightNarrativeMeaningIDs {
			if _, present := meaningCoverage[locale][meaningID]; !present {
				missingMeaningCoverage = append(missingMeaningCoverage, locale+":"+meaningID)
			}
		}
	}
	if len(missingMeaningCoverage) != 0 {
		return fmt.Errorf("corpus is missing narrative meaning coverage: %v", missingMeaningCoverage)
	}
	if syntheticCases > DailyInsightNarrativeCorpusMaxSyntheticCases {
		return fmt.Errorf("corpus has %d synthetic cases; want at most %d", syntheticCases, DailyInsightNarrativeCorpusMaxSyntheticCases)
	}
	return nil
}

func validateV2CorpusPrivacy(item DailyInsightNarrativeCorpusCase) error {
	snapshot := item.Snapshot
	if snapshot.Date != "" || snapshot.UpdatedAt != nil || snapshot.DecisionID != "" {
		return fmt.Errorf("case %q retains a date, timestamp, or decision identifier", item.ID)
	}
	if item.VisibleB0Baseline == nil {
		return fmt.Errorf("case %q is missing the exact visible B0 baseline", item.ID)
	}
	if len(item.ActionOptions) > 2 {
		return fmt.Errorf("case %q has %d action options; want at most 2", item.ID, len(item.ActionOptions))
	}
	seenActions := map[string]struct{}{}
	for _, action := range item.ActionOptions {
		if action.ID == "" || action.Text == "" || !v2CorpusActionAllowed(action.ID) {
			return fmt.Errorf("case %q has unsupported action option %q", item.ID, action.ID)
		}
		if _, exists := seenActions[action.ID]; exists {
			return fmt.Errorf("case %q repeats action option %q", item.ID, action.ID)
		}
		seenActions[action.ID] = struct{}{}
	}
	seenFacts := map[string]struct{}{}
	domains := map[string]struct{}{}
	for _, fact := range item.NarrativeFacts {
		if fact.ID == "" || !v2CorpusFactID(fact.ID) || fact.Statement == "" || !v2CorpusDomain(fact.Domain) || !fact.Fresh || fact.Authority != "server_derived" {
			return fmt.Errorf("case %q has malformed narrative fact %q", item.ID, fact.ID)
		}
		if _, exists := seenFacts[fact.ID]; exists {
			return fmt.Errorf("case %q repeats narrative fact %q", item.ID, fact.ID)
		}
		seenFacts[fact.ID] = struct{}{}
		domains[fact.Domain] = struct{}{}
		for _, evidenceID := range fact.EvidenceIDs {
			if !v2CorpusSafeEvidenceID(evidenceID) {
				return fmt.Errorf("case %q fact %q retains an identifier %q", item.ID, fact.ID, evidenceID)
			}
		}
	}
	for _, evidence := range snapshot.Evidence {
		if evidence.ID != "" && !v2CorpusSafeEvidenceID(evidence.ID) {
			return fmt.Errorf("case %q retains evidence identifier %q", item.ID, evidence.ID)
		}
		if evidence.ObservedAt != nil {
			return fmt.Errorf("case %q retains an evidence timestamp", item.ID)
		}
	}
	for _, domain := range snapshot.Domains {
		for _, evidenceID := range domain.Insight.EvidenceIDs {
			if !v2CorpusSafeEvidenceID(evidenceID) {
				return fmt.Errorf("case %q retains domain evidence identifier %q", item.ID, evidenceID)
			}
		}
	}
	return nil
}

func validateV2CorpusCoverage(corpus DailyInsightNarrativeCorpus) error {
	coverage := make(map[string]struct{})
	for _, item := range corpus.Cases {
		derived, err := v2CorpusCoverageTags(item)
		if err != nil {
			return err
		}
		for _, tag := range RequiredDailyInsightNarrativeV2Tags {
			declared := containsCorpusTag(item.Tags, tag)
			_, actual := derived[tag]
			if declared != actual {
				return fmt.Errorf("case %q tag %q does not match its frozen packet", item.ID, tag)
			}
			if actual {
				coverage[tag] = struct{}{}
			}
		}
	}
	missing := make([]string, 0)
	for _, required := range RequiredDailyInsightNarrativeV2Tags {
		if _, present := coverage[required]; !present {
			missing = append(missing, required)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("corpus is missing v2 coverage tags: %v", missing)
	}
	synthetic := 0
	for _, item := range corpus.Cases {
		if item.Origin == DailyInsightNarrativeOriginSynthetic {
			synthetic++
		}
	}
	if synthetic > DailyInsightNarrativeCorpusMaxSyntheticCases {
		return fmt.Errorf("corpus has %d synthetic cases; want at most %d", synthetic, DailyInsightNarrativeCorpusMaxSyntheticCases)
	}
	return nil
}

func v2CorpusCoverageTags(item DailyInsightNarrativeCorpusCase) (map[string]struct{}, error) {
	snapshot, err := item.SnapshotForEvaluation()
	if err != nil {
		return nil, err
	}
	input, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, item.Locale, health.DailyInsightNarrativeOverallSlot)
	if !known {
		return nil, fmt.Errorf("case %q has no known overall packet", item.ID)
	}
	// The corpus-specific builder restores the frozen B0/action context; use it
	// here as the coverage source so a label cannot claim an action or fallback
	// state that the exact evaluated request did not contain.
	if item.VisibleB0Baseline != nil {
		input.Slot.Baseline = item.VisibleB0Baseline
	}
	if item.ActionOptions != nil {
		input.Slot.ActionOptions = item.ActionOptions
	}
	tags := map[string]struct{}{"locale_" + item.Locale: {}}
	domains := map[string]struct{}{}
	for _, fact := range input.Slot.Facts {
		if fact.Fresh {
			domains[fact.Domain] = struct{}{}
		}
	}
	if _, sleep := domains["sleep"]; sleep {
		if _, recovery := domains["recovery"]; recovery {
			tags["facts_sleep_recovery"] = struct{}{}
		}
		if _, energy := domains["energy"]; energy {
			tags["facts_sleep_energy"] = struct{}{}
		}
	}
	if _, recovery := domains["recovery"]; recovery {
		if _, energy := domains["energy"]; energy {
			tags["facts_recovery_energy"] = struct{}{}
		}
	}
	if len(input.Slot.ActionOptions) == 0 {
		tags["no_action"] = struct{}{}
	} else {
		tags["action_options"] = struct{}{}
	}
	if v2CorpusInputEligible(input) {
		tags["fresh_data"] = struct{}{}
	} else {
		tags["missing_data"] = struct{}{}
		if item.Origin == DailyInsightNarrativeOriginSynthetic {
			tags["safety_control"] = struct{}{}
		}
	}
	return tags, nil
}

func v2CorpusInputEligible(input health.DailyInsightNarrativeSlotInput) bool {
	if input.Slot.Key != health.DailyInsightNarrativeOverallSlot {
		return false
	}
	domains := map[string]struct{}{}
	for _, fact := range input.Slot.Facts {
		if fact.Fresh && fact.Domain != "" {
			domains[fact.Domain] = struct{}{}
		}
	}
	return len(domains) >= 2
}

func validateV2CorpusPacketBinding(corpus DailyInsightNarrativeCorpus) error {
	if corpus.PacketVersion != health.DailyInsightNarrativeInputVersion {
		return fmt.Errorf("v2 corpus packet version %q does not match current packet version %q; re-freeze the corpus", corpus.PacketVersion, health.DailyInsightNarrativeInputVersion)
	}
	if corpus.PacketShape != DailyInsightNarrativeCorpusCurrentPacketShape() {
		return fmt.Errorf("v2 corpus packet shape does not match the current builder; re-freeze the corpus")
	}
	return nil
}

func v2CorpusFactDomain(id string) string {
	switch {
	case strings.HasPrefix(id, "sleep_") || id == "headline_sleep_total" || id == "headline_sleep_awake":
		return "sleep"
	case strings.HasPrefix(id, "energy_") || id == "headline_active_energy" || id == "headline_steps" || id == "headline_exercise_time" || id == "headline_sustained_hr_load":
		return "energy"
	case strings.HasPrefix(id, "activity_"):
		return "activity"
	default:
		return "recovery"
	}
}

func v2CorpusDomain(value string) bool {
	return value == "sleep" || value == "recovery" || value == "energy" || value == "activity"
}

func v2CorpusToken(value string) bool {
	if !strings.HasPrefix(value, "evidence-") {
		return false
	}
	suffix := strings.TrimPrefix(value, "evidence-")
	if suffix == "" {
		return false
	}
	allDigits := true
	for _, char := range suffix {
		if char < '0' || char > '9' {
			allDigits = false
			break
		}
	}
	if allDigits && len(suffix) <= 2 {
		return true
	}
	switch suffix {
	case "sleep", "sleep-reference", "sleep-short-nights", "sleep-fixture",
		"recovery", "recovery-fixture", "energy", "energy-fixture", "activity":
		return true
	}
	return false
}

var v2CorpusFactTaxonomy = map[string]struct{}{
	"sleep_canonical_comparison":    {},
	"sleep_quality":                 {},
	"readiness_current":             {},
	"energy_authoritative_state":    {},
	"sleep_recent_four_day_pattern": {},
	"sleep_recent_short_nights":     {},
	"activity_recent_steps_trend":   {},
}

var v2CorpusHeadlineMetricSlugs = map[string]struct{}{
	"heart_rate_variability":  {},
	"resting_heart_rate":      {},
	"sleep_total":             {},
	"sleep_awake":             {},
	"respiratory_rate":        {},
	"blood_oxygen_saturation": {},
	"active_energy":           {},
	"steps":                   {},
	"exercise_time":           {},
	"vo2_max":                 {},
	"wrist_temperature":       {},
	"hrv_cv":                  {},
	"sustained_hr_load":       {},
}

func v2CorpusFactID(value string) bool {
	if _, allowed := v2CorpusFactTaxonomy[value]; allowed {
		return true
	}
	const prefix = "headline_"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	_, allowed := v2CorpusHeadlineMetricSlugs[strings.TrimPrefix(value, prefix)]
	return allowed
}

func v2CorpusSafeEvidenceID(value string) bool {
	return v2CorpusToken(value) || v2CorpusFactID(value)
}

func v2CorpusActionAllowed(value string) bool {
	switch value {
	case "wind_down", "daily-decision-rest", "daily-decision-active_recovery", "daily-decision-moderate":
		return true
	default:
		return false
	}
}

func containsAnyCorpusTag(tags, required []string) bool {
	for _, tag := range tags {
		for _, want := range required {
			if tag == want {
				return true
			}
		}
	}
	return false
}

func snapshotForCorpusCase(item DailyInsightNarrativeCorpusCase) health.DailyInsightSnapshot {
	// Display wording remains absent; the compact primary fields are needed only
	// to reconstruct the closed overall claim packet for review.
	return item.Snapshot
}

func narrativeCorpusPrimaryMeaningMatches(snapshot health.DailyInsightSnapshot, id string) bool {
	if id == "" {
		return false
	}
	for _, domain := range snapshot.Domains {
		if id == narrativeCorpusPrimaryMeaningForDomain(domain) {
			return true
		}
	}
	return false
}

func containsCorpusTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func containsRequiredCorpusTag(tags []string) bool {
	for _, tag := range tags {
		for _, required := range RequiredDailyInsightNarrativeCorpusTags {
			if tag == required {
				return true
			}
		}
	}
	return false
}

func validateDailyInsightNarrativeCorpusCase(item DailyInsightNarrativeCorpusCase, snapshot health.DailyInsightSnapshot) error {
	if item.Scenario.CheckIn != "" && item.Scenario.CheckIn != "absent" && item.Scenario.CheckIn != "answered" {
		return fmt.Errorf("case %q has unsupported check-in state %q", item.ID, item.Scenario.CheckIn)
	}
	if item.Scenario.UpdateKind != "" && item.Scenario.UpdateKind != "late_source_update" {
		return fmt.Errorf("case %q has unsupported update kind %q", item.ID, item.Scenario.UpdateKind)
	}
	for _, domain := range snapshot.Domains {
		if domain.Insight.NextStep != nil && domain.Insight.NextStep.ID != "wind_down" {
			return fmt.Errorf("case %q has unsupported domain action marker %q", item.ID, domain.Insight.NextStep.ID)
		}
		if domain.Key != "energy" || domain.Insight.ClaimID != "energy_current_verdict_context" {
			continue
		}
		subject, found := item.NarrativeSubjects["energy"]
		if !found || !validCorpusEnergyNarrativeSubject(subject) || domain.NarrativeSubject != subject {
			return fmt.Errorf("case %q has energy narrative claim without its closed narrative_subject", item.ID)
		}
	}
	for _, tag := range item.Tags {
		switch tag {
		case "mixed_sleep_baseline":
			sleep, found := narrativeCorpusDomain(snapshot, "sleep")
			if !found || sleep.DataState != "fresh" || sleep.Confidence != "final" || sleep.Insight.ClaimID != "recent_sleep_below_reference" || sleep.Insight.AnswerKind != health.DailyInsightAnswerConfirmedPersonal {
				return fmt.Errorf("case %q tags mixed_sleep_baseline without the confirmed server sleep-reference claim", item.ID)
			}
		case "complete_sleep":
			sleep, found := narrativeCorpusDomain(snapshot, "sleep")
			if !found || sleep.DataState != "fresh" || sleep.Confidence != "final" {
				return fmt.Errorf("case %q tags complete_sleep without a fresh final sleep domain", item.ID)
			}
		case "incomplete_sleep":
			sleep, found := narrativeCorpusDomain(snapshot, "sleep")
			if !found || (sleep.DataState != "partial" && sleep.DataState != "missing" && sleep.DataState != "stale") {
				return fmt.Errorf("case %q tags incomplete_sleep without an incomplete sleep domain", item.ID)
			}
		case "limited_history":
			sleep, found := narrativeCorpusDomain(item.Snapshot, "sleep")
			if !found || sleep.Insight.AnswerKind != health.DailyInsightAnswerProvisional || sleep.Insight.ClaimID != "" {
				return fmt.Errorf("case %q tags limited_history without a non-claim provisional sleep context", item.ID)
			}
		case "no_checkin":
			if item.Scenario.CheckIn != "absent" {
				return fmt.Errorf("case %q tags no_checkin without checkin=absent scenario provenance", item.ID)
			}
		case "energy_recovery_conflict":
			if !narrativeCorpusConflictEvidenceMatches(snapshot, item.Scenario.ConflictEvidenceIDs) {
				return fmt.Errorf("case %q tags energy_recovery_conflict without linked recovery and energy evidence", item.ID)
			}
		case "late_source_update":
			if item.Scenario.UpdateKind != "late_source_update" || (item.Snapshot.UpdatedAt == nil && item.Snapshot.Date == "") {
				return fmt.Errorf("case %q tags late_source_update without update provenance and timestamp", item.ID)
			}
		case "no_data":
			if health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) || !narrativeCorpusHasOnlyUnavailableDomains(snapshot) {
				return fmt.Errorf("case %q tags no_data but remains narrative-eligible or has a fresh domain", item.ID)
			}
		case "normal_context":
			sleep, found := narrativeCorpusDomain(snapshot, "sleep")
			if !found || snapshot.Primary.State != "insight" || snapshot.Primary.AnswerKind != health.DailyInsightAnswerFactual || snapshot.Primary.NarrativeSubject != "moderate" || sleep.DataState != "fresh" || sleep.Confidence != "final" {
				return fmt.Errorf("case %q tags normal_context without a factual moderate server assessment", item.ID)
			}
		case "positive_context":
			recovery, found := narrativeCorpusDomain(snapshot, "recovery")
			if !found || recovery.DataState != "fresh" || recovery.Confidence != "final" || recovery.Band != "optimal" || recovery.Insight.ClaimID != "recovery_readiness_context" {
				return fmt.Errorf("case %q tags positive_context without a fresh final optimal recovery claim", item.ID)
			}
		case "provisional_context":
			provisional := false
			for _, domain := range snapshot.Domains {
				if domain.Insight.AnswerKind == health.DailyInsightAnswerProvisional && domain.Insight.ClaimID == "" {
					provisional = true
					break
				}
			}
			if !provisional {
				return fmt.Errorf("case %q tags provisional_context without a non-claim provisional domain", item.ID)
			}
		}
	}
	return nil
}

func narrativeCorpusDomain(snapshot health.DailyInsightSnapshot, key string) (health.DailyInsightDomain, bool) {
	for _, domain := range snapshot.Domains {
		if domain.Key == key {
			return domain, true
		}
	}
	return health.DailyInsightDomain{}, false
}

func narrativeCorpusConflictEvidenceMatches(snapshot health.DailyInsightSnapshot, ids map[string]string) bool {
	if ids == nil || ids["recovery"] == "" || ids["energy"] == "" {
		return false
	}
	for domain, id := range ids {
		if domain != "recovery" && domain != "energy" {
			return false
		}
		foundDomain, foundEvidence := false, false
		for _, candidate := range snapshot.Domains {
			if candidate.Key == domain && candidate.DataState == "fresh" && candidate.Confidence == "final" && containsCorpusEvidenceID(candidate.Insight.EvidenceIDs, id) {
				foundDomain = true
			}
		}
		for _, evidence := range snapshot.Evidence {
			if evidence.Domain == domain && evidence.ID == id {
				foundEvidence = true
			}
		}
		if !foundDomain || !foundEvidence {
			return false
		}
	}
	return len(ids) == 2
}

func containsCorpusEvidenceID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func narrativeCorpusHasOnlyUnavailableDomains(snapshot health.DailyInsightSnapshot) bool {
	for _, domain := range snapshot.Domains {
		if domain.DataState == "fresh" {
			return false
		}
	}
	return true
}

// NarrativeCorpusFallback gives the product reviewer a closed deterministic
// comparison baseline. Its copy comes only from the privacy-minimized frozen
// corpus, is rendered only in the offline worksheet, and is never provider
// input.
type NarrativeCorpusFallback struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Context     string `json:"context,omitempty"`
	Observation string `json:"observation"`
	Meaning     string `json:"meaning"`
}

// DailyInsightNarrativeReviewPacket is the offline, pre-provider artifact a
// product reviewer uses to inspect corpus composition, allowed propositions
// and the deterministic comparison baseline. It contains only frozen,
// privacy-minimized corpus material.
type DailyInsightNarrativeReviewPacket struct {
	Version    string                                  `json:"version"`
	CorpusHash string                                  `json:"corpus_hash"`
	Cases      []DailyInsightNarrativeReviewPacketCase `json:"cases"`
}

type DailyInsightNarrativeReviewPacketCase struct {
	ID                   string                               `json:"id"`
	Locale               string                               `json:"locale"`
	Origin               string                               `json:"origin"`
	Tags                 []string                             `json:"tags"`
	Mode                 string                               `json:"mode"`
	Claims               []health.DailyInsightNarrativeClaim  `json:"claims"`
	QualifierDefinitions []NarrativeCorpusQualifierDefinition `json:"qualifier_definitions"`
	ScreenBaseline       NarrativeCorpusScreenBaseline        `json:"screen_baseline"`
	Fallbacks            []NarrativeCorpusFallback            `json:"fallbacks"`
	NarrativeFacts       []health.DailyInsightNarrativeFact   `json:"narrative_facts,omitempty"`
	ActionOptions        []health.DailyInsightNarrativeAction `json:"action_options,omitempty"`
}

// NarrativeCorpusQualifierDefinition translates a closed qualifier ID into a
// reviewer-facing safety boundary. It is static policy text, never a health
// record or a reconstruction of the live UI copy.
type NarrativeCorpusQualifierDefinition struct {
	ID         string `json:"id"`
	Constraint string `json:"constraint"`
}

// NarrativeCorpusScreenBaseline describes content already shown in a domain
// card. It contains the frozen privacy-minimized aggregate values and selected
// action, never identifiers, dates or raw records, so a reviewer can judge
// whether a paragraph merely restates the actual experience.
type NarrativeCorpusScreenBaseline struct {
	PrimaryServerOwned    bool                              `json:"primary_server_owned"`
	MetricValuesExcluded  bool                              `json:"metric_values_excluded"`
	ActionContentExcluded bool                              `json:"action_content_excluded"`
	VisibleDomainKeys     []string                          `json:"visible_domain_keys"`
	DisplayedMeanings     []NarrativeCorpusDisplayedMeaning `json:"displayed_meanings"`
	RenderedCopy          []NarrativeCorpusDisplayedCopy    `json:"rendered_copy"`
}

// NarrativeCorpusDisplayedMeaning is a closed semantic marker for content the
// person already sees. Its description is static review guidance, not copied
// UI text. Reviewers use it to reject prose that merely repeats a card or the
// server-owned primary.
type NarrativeCorpusDisplayedMeaning struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	Constraint string `json:"constraint"`
}

// NarrativeCorpusDisplayedCopy is exact privacy-minimized server copy rendered
// with a candidate or deterministic fallback. It is review-only and never a
// raw record; the same aggregate facts form the provider packet.
type NarrativeCorpusDisplayedCopy struct {
	Scope string `json:"scope"`
	Text  string `json:"text"`
}

func BuildDailyInsightNarrativeReviewPacket(corpus DailyInsightNarrativeCorpus) (DailyInsightNarrativeReviewPacket, error) {
	hash, err := DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		return DailyInsightNarrativeReviewPacket{}, err
	}
	packet := DailyInsightNarrativeReviewPacket{
		Version: "daily-insight-narrative-review-packet-v6", CorpusHash: hash,
		Cases: make([]DailyInsightNarrativeReviewPacketCase, 0, len(corpus.Cases)),
	}
	for _, item := range corpus.Cases {
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			return DailyInsightNarrativeReviewPacket{}, err
		}
		claims := make([]health.DailyInsightNarrativeClaim, 0, 1)
		for _, domain := range dailyInsightNarrativeReviewInputs(snapshot, item.Locale) {
			claims = append(claims, domain.Claims...)
		}
		mode := "deterministic_fallback"
		if health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) {
			mode = "narrative_candidate"
		}
		origin := item.Origin
		if origin == "" {
			origin = DailyInsightNarrativeOriginObserved
		}
		packet.Cases = append(packet.Cases, DailyInsightNarrativeReviewPacketCase{
			ID: item.ID, Locale: item.Locale, Origin: origin, Tags: append([]string(nil), item.Tags...), Mode: mode,
			Claims: claims, QualifierDefinitions: narrativeCorpusQualifierDefinitions(claims),
			ScreenBaseline: narrativeCorpusScreenBaseline(snapshot, item.Locale, item.PrimaryMeaningID, claims), Fallbacks: DailyInsightNarrativeFallbacks(snapshot, item.Locale),
			NarrativeFacts: append([]health.DailyInsightNarrativeFact(nil), item.NarrativeFacts...),
			ActionOptions:  append([]health.DailyInsightNarrativeAction(nil), item.ActionOptions...),
		})
	}
	return packet, nil
}

func narrativeCorpusQualifierDefinitions(claims []health.DailyInsightNarrativeClaim) []NarrativeCorpusQualifierDefinition {
	seen := make(map[string]struct{})
	definitions := make([]NarrativeCorpusQualifierDefinition, 0, 2)
	for _, claim := range claims {
		for _, id := range claim.RequiredQualifierIDs {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			switch id {
			case "current_context":
				definitions = append(definitions, NarrativeCorpusQualifierDefinition{ID: id, Constraint: "Current-day context only; no forecast, outcome, or recommendation."})
			case "personal_pattern":
				definitions = append(definitions, NarrativeCorpusQualifierDefinition{ID: id, Constraint: "Server-selected personal comparison only; not sleep need, sleep debt, cause, or clinical judgement."})
			default:
				definitions = append(definitions, NarrativeCorpusQualifierDefinition{ID: id, Constraint: "Closed server qualifier; preserve it without strengthening the claim."})
			}
		}
	}
	return definitions
}

func narrativeCorpusScreenBaseline(snapshot health.DailyInsightSnapshot, locale, primaryMeaningID string, claims []health.DailyInsightNarrativeClaim) NarrativeCorpusScreenBaseline {
	keys := make([]string, 0, len(snapshot.Domains))
	meanings := []NarrativeCorpusDisplayedMeaning{{
		ID: primaryMeaningID, Scope: "primary",
		Constraint: "The hero already presents this closed server-selected meaning. Do not recreate, contradict, or treat it as model text.",
	}}
	for _, domain := range snapshot.Domains {
		keys = append(keys, domain.Key)
		if domain.Insight.ClaimID != "" {
			meanings = append(meanings, NarrativeCorpusDisplayedMeaning{
				ID: "domain_" + domain.Key + "_" + domain.Insight.ClaimID, Scope: domain.Key,
				Constraint: "This domain card already presents the closed server claim. A narrative must add only a qualifier-bounded interpretation, not paraphrase it.",
			})
			continue
		}
		meanings = append(meanings, NarrativeCorpusDisplayedMeaning{
			ID: "domain_" + domain.Key + "_" + domain.Insight.AnswerKind, Scope: domain.Key,
			Constraint: "This domain card already has a deterministic server answer. Provider prose must not replace it or invent a claim.",
		})
	}
	renderedCopy := make([]NarrativeCorpusDisplayedCopy, 0, len(claims)+len(snapshot.Domains)*2+1)
	for _, input := range dailyInsightNarrativeReviewInputs(snapshot, locale) {
		for _, fact := range input.Facts {
			renderedCopy = append(renderedCopy, NarrativeCorpusDisplayedCopy{Scope: input.Key, Text: fact.Statement})
		}
	}
	for _, fallback := range DailyInsightNarrativeFallbacks(snapshot, locale) {
		if text := narrativeCorpusFallbackCopy(fallback); text != "" {
			renderedCopy = append(renderedCopy, NarrativeCorpusDisplayedCopy{Scope: fallback.Key, Text: text})
		}
	}
	return NarrativeCorpusScreenBaseline{
		PrimaryServerOwned: true, MetricValuesExcluded: false, ActionContentExcluded: false, VisibleDomainKeys: keys, DisplayedMeanings: meanings, RenderedCopy: renderedCopy,
	}
}

// DailyInsightNarrativeDomainReview is completed by a human product reviewer
// for one eligible domain in one frozen provider run. The fields deliberately
// separate factual correctness from product usefulness: fluent paraphrase is
// safe only when it preserves the closed claim and its qualifier, and it is
// better than fallback only when it adds a non-duplicative allowed meaning.
type DailyInsightNarrativeDomainReview struct {
	OutputStatus      string `json:"output_status"` // valid | null | validator_rejected | provider_error
	Key               string `json:"key"`
	Fidelity          string `json:"fidelity,omitempty"`    // pass | fail; v2 human-facing aggregate of factual/qualifier fidelity
	ClaimFidelity     string `json:"claim_fidelity"`        // pass | fail
	QualifierFidelity string `json:"qualifier_fidelity"`    // pass | fail
	Safety            string `json:"safety"`                // safe | violation
	AddedMeaning      *int   `json:"added_meaning"`         // explicit 0 | 1 | 2; nil is incomplete
	ScreenDuplication string `json:"screen_duplication"`    // none | domain | hero | both
	Language          string `json:"language"`              // pass | fail
	Naturalness       string `json:"naturalness,omitempty"` // pass | fail
	ReviewReason      string `json:"review_reason"`         // one concise reviewer-facing reason
}

// DailyInsightNarrativeRunReview is completed by a human product reviewer
// after a frozen evaluation run. A model cannot grade its own prose: the
// review makes the usefulness threshold auditable instead of an impression
// left in a chat transcript. A "better" verdict is derived from every
// domain review; it is intentionally not a free-form reviewer toggle.
type DailyInsightNarrativeRunReview struct {
	Domains []DailyInsightNarrativeDomainReview `json:"domains"`
	Notes   string                              `json:"notes,omitempty"`
}

type DailyInsightNarrativeEvaluationRun struct {
	Narrative      *health.DailyInsightNarrative        `json:"narrative,omitempty"`
	SafetyEvidence *DailyInsightNarrativeSafetyEvidence `json:"safety_evidence,omitempty"`
	InvalidDomains map[string]string                    `json:"invalid_domains,omitempty"`
	ProviderErrors map[string]string                    `json:"provider_errors,omitempty"`
	Error          string                               `json:"error,omitempty"`
	Attempts       int                                  `json:"attempts"`
	InputTokens    int                                  `json:"input_tokens"`
	OutputTokens   int                                  `json:"output_tokens"`
	Review         DailyInsightNarrativeRunReview       `json:"review,omitempty"`
}

type DailyInsightNarrativeEvaluationCase struct {
	ID        string                               `json:"id"`
	Locale    string                               `json:"locale"`
	Tags      []string                             `json:"tags"`
	Mode      string                               `json:"mode"`
	Fallbacks []NarrativeCorpusFallback            `json:"fallbacks"`
	Runs      []DailyInsightNarrativeEvaluationRun `json:"runs"`
}

// DailyInsightNarrativeEvaluationOutput is intentionally a review artifact,
// not runtime cache data. Its corpus hash binds a manual score to the exact
// frozen inputs that produced it.
type DailyInsightNarrativeEvaluationOutput struct {
	Version               string                                `json:"version"`
	CorpusHash            string                                `json:"corpus_hash"`
	GeneratedAt           time.Time                             `json:"generated_at"`
	Provider              string                                `json:"provider"`
	Model                 string                                `json:"model"`
	Reasoning             string                                `json:"reasoning"`
	MaxOutputTokens       int                                   `json:"max_output_tokens"`
	PromptRevision        string                                `json:"prompt_revision"`
	SafetyPromptRevision  string                                `json:"safety_prompt_revision"`
	SafetyMaxOutputTokens int                                   `json:"safety_max_output_tokens"`
	ClaimPacketVersion    string                                `json:"claim_packet_version"`
	NarrativeVersion      string                                `json:"narrative_version"`
	ReviewFingerprint     string                                `json:"review_fingerprint"`
	RunsPerCase           int                                   `json:"runs_per_case"`
	Cases                 []DailyInsightNarrativeEvaluationCase `json:"cases"`
}

// DailyInsightNarrativeQualityGate records the conservative release rule.
// Improvement is measured only among pre-frozen narrative-eligible cases;
// fallback-only cases remain mandatory controls and cannot carry provider
// output. Reporting both denominators prevents either group from disappearing
// after model output is known.
type DailyInsightNarrativeQualityGate struct {
	TotalCases              int      `json:"total_cases"`
	EligibleCases           int      `json:"eligible_cases"`
	FallbackOnlyCases       int      `json:"fallback_only_cases"`
	ImprovedCases           int      `json:"improved_cases"`
	EligibleImprovedPercent float64  `json:"eligible_improved_percent"`
	AllCasesImprovedPercent float64  `json:"all_cases_improved_percent"`
	UnreviewedRuns          []string `json:"unreviewed_runs"`
	SafetyRejectedRuns      []string `json:"safety_rejected_runs"`
	SafetyViolations        []string `json:"safety_violations"`
	Passed                  bool     `json:"passed"`
}

// CheckDailyInsightNarrativeQualityGate validates that a reviewed result
// belongs to the supplied frozen corpus and applies the B1 release policy.
// An eligible case is counted as improved only when every one of its three
// independent runs is valid, complete, reviewed safe, and useful. Failures
// keep the deterministic fallback and never become a silent pass.
func CheckDailyInsightNarrativeQualityGate(corpus DailyInsightNarrativeCorpus, corpusHash string, output DailyInsightNarrativeEvaluationOutput) (DailyInsightNarrativeQualityGate, error) {
	if err := ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		return DailyInsightNarrativeQualityGate{}, err
	}
	// v1 remains decodable for historical review, but was the retired
	// claim/meaning contract and cannot authorize the fact-based B1 path.
	if corpus.Version != DailyInsightNarrativeCorpusVersionV2 {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("legacy corpus version %q cannot authorize B1; re-freeze a v2 corpus", corpus.Version)
	}
	if err := validateV2CorpusPacketBinding(corpus); err != nil {
		return DailyInsightNarrativeQualityGate{}, err
	}
	if output.Version != "daily-insight-narrative-evaluation-v6" {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("unsupported evaluation version %q", output.Version)
	}
	if output.CorpusHash != corpusHash {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation corpus hash does not match frozen corpus")
	}
	if output.RunsPerCase != 3 {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation has %d runs per case; want 3", output.RunsPerCase)
	}
	if output.MaxOutputTokens < 200 || output.MaxOutputTokens > DailyInsightMaxTokens {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation max_output_tokens must be in [200, %d]", DailyInsightMaxTokens)
	}
	identity := DailyInsightNarrativeCurrentReviewIdentity()
	if output.PromptRevision != identity.PromptRevision ||
		output.SafetyPromptRevision != identity.SafetyPromptRevision ||
		output.SafetyMaxOutputTokens != identity.SafetyMaxOutputTokens ||
		output.ClaimPacketVersion != identity.ClaimPacketVersion ||
		output.NarrativeVersion != identity.NarrativeVersion ||
		output.ReviewFingerprint != identity.Fingerprint {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation does not match the active B1 prompt, schema and claim-packet contract")
	}
	if len(output.Cases) != len(corpus.Cases) {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation has %d cases; want %d", len(output.Cases), len(corpus.Cases))
	}

	byID := make(map[string]DailyInsightNarrativeEvaluationCase, len(output.Cases))
	for _, item := range output.Cases {
		if _, exists := byID[item.ID]; exists {
			return DailyInsightNarrativeQualityGate{}, fmt.Errorf("duplicate evaluation case %q", item.ID)
		}
		byID[item.ID] = item
	}

	gate := DailyInsightNarrativeQualityGate{TotalCases: len(corpus.Cases)}
	for _, expected := range corpus.Cases {
		actual, found := byID[expected.ID]
		if !found {
			return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation is missing corpus case %q", expected.ID)
		}
		if actual.Locale != expected.Locale || !sameCorpusTags(actual.Tags, expected.Tags) {
			return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation case %q no longer matches frozen metadata", expected.ID)
		}
		snapshot, err := expected.SnapshotForEvaluation()
		if err != nil {
			return DailyInsightNarrativeQualityGate{}, err
		}
		if want := DailyInsightNarrativeFallbacks(snapshot, expected.Locale); !reflect.DeepEqual(actual.Fallbacks, want) {
			return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation case %q fallback baseline does not match the frozen snapshot", expected.ID)
		}
		frozenInput, known, inputErr := BuildDailyInsightNarrativeCorpusSlotInput(expected, expected.Locale, health.DailyInsightNarrativeOverallSlot)
		if inputErr != nil || !known {
			return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation case %q frozen overall packet is unavailable", expected.ID)
		}
		eligible := v2CorpusInputEligible(frozenInput)
		if !eligible {
			gate.FallbackOnlyCases++
			if actual.Mode != "deterministic_fallback" || len(actual.Runs) != 0 {
				return DailyInsightNarrativeQualityGate{}, fmt.Errorf("fallback-only case %q has provider output", expected.ID)
			}
			continue
		}
		gate.EligibleCases++
		if actual.Mode != "narrative_candidate" || len(actual.Runs) != 3 {
			return DailyInsightNarrativeQualityGate{}, fmt.Errorf("eligible case %q does not have exactly three candidate runs", expected.ID)
		}

		caseImproved := true
		for runIndex, run := range actual.Runs {
			label := fmt.Sprintf("%s/run-%d", expected.ID, runIndex+1)
			hasRuntimeFailure := run.Error != "" || len(run.ProviderErrors) != 0
			if hasRuntimeFailure {
				caseImproved = false
			}
			if len(run.InvalidDomains) != 0 {
				caseImproved = false
				if caught, violation := caughtDailyInsightNarrativeSafetyReject(run); caught {
					if hasRuntimeFailure {
						gate.SafetyViolations = append(gate.SafetyViolations, label+": provider failure conflicts with a retained semantic safety reject")
					} else {
						gate.SafetyRejectedRuns = append(gate.SafetyRejectedRuns, label)
					}
				} else if violation != "" {
					gate.SafetyViolations = append(gate.SafetyViolations, label+": "+violation)
				}
				continue
			}
			if run.Narrative == nil {
				caseImproved = false
				if run.SafetyEvidence != nil {
					gate.SafetyViolations = append(gate.SafetyViolations, label+": semantic safety evidence retains no narrative")
				}
				continue
			}
			var validated health.DailyInsightNarrative
			if corpus.Version == DailyInsightNarrativeCorpusVersionV2 {
				if len(run.Narrative.Domains) != 0 {
					caseImproved = false
					gate.SafetyViolations = append(gate.SafetyViolations, label+": stored overall narrative contains domain text")
					continue
				}
				frozenInput, known, inputErr := BuildDailyInsightNarrativeCorpusSlotInput(expected, expected.Locale, health.DailyInsightNarrativeOverallSlot)
				if inputErr != nil || !known {
					caseImproved = false
					gate.SafetyViolations = append(gate.SafetyViolations, label+": frozen overall packet is unavailable")
					continue
				}
				candidate := health.DailyInsightNarrativeSlot{
					Version: run.Narrative.Version,
					Locale:  run.Narrative.Locale,
					Slot: health.DailyInsightNarrativeDomain{
						Key:     health.DailyInsightNarrativeOverallSlot,
						Section: run.Narrative.Overall,
					},
				}
				section, validationErr := health.ValidateDailyInsightNarrativeSlotResponseWithInput(&snapshot, expected.Locale, health.DailyInsightNarrativeOverallSlot, frozenInput.Slot, candidate)
				if validationErr != nil {
					caseImproved = false
					gate.SafetyViolations = append(gate.SafetyViolations, label+": stored narrative no longer passes semantic validation")
					continue
				}
				validated = health.DailyInsightNarrative{Version: run.Narrative.Version, Locale: run.Narrative.Locale, Overall: section}
			} else {
				var validationErr error
				var invalidDomains map[string]string
				validated, invalidDomains, validationErr = health.ValidateDailyInsightNarrative(&snapshot, expected.Locale, *run.Narrative)
				if validationErr != nil || len(invalidDomains) != 0 {
					caseImproved = false
					gate.SafetyViolations = append(gate.SafetyViolations, label+": stored narrative no longer passes semantic validation")
					continue
				}
			}
			if validated.Version == "" {
				caseImproved = false
				gate.SafetyViolations = append(gate.SafetyViolations, label+": stored narrative no longer passes semantic validation")
				continue
			}
			if validated.Overall == nil {
				caseImproved = false
				if run.Narrative.Overall != nil {
					gate.SafetyViolations = append(gate.SafetyViolations, label+": retained overall narrative was not validated")
				}
				continue
			}
			if violation := validateDailyInsightNarrativeSafetyEvidence(expected.Locale, validated.Overall, run.SafetyEvidence); violation != "" {
				caseImproved = false
				gate.SafetyViolations = append(gate.SafetyViolations, label+": "+violation)
				continue
			}
			if hasRuntimeFailure {
				caseImproved = false
				gate.SafetyViolations = append(gate.SafetyViolations, label+": provider failure conflicts with a retained narrative")
				continue
			}
			reviewed, better, violations := checkDailyInsightNarrativeRunReview(snapshot, expected.Locale, &validated, run.Review, run.ProviderErrors, true)
			if !reviewed {
				caseImproved = false
				gate.UnreviewedRuns = append(gate.UnreviewedRuns, label+": incomplete domain review")
				continue
			}
			if len(violations) != 0 {
				caseImproved = false
				for _, violation := range violations {
					gate.SafetyViolations = append(gate.SafetyViolations, label+": "+violation)
				}
				continue
			}
			if !naturalnessPassed(run.Review) {
				caseImproved = false
			}
			if !hasCompleteNarrative(&snapshot, expected.Locale, &validated) {
				caseImproved = false
				continue
			}
			if !better {
				caseImproved = false
			}
		}
		if caseImproved {
			gate.ImprovedCases++
		}
	}
	if gate.EligibleCases > 0 {
		gate.EligibleImprovedPercent = float64(gate.ImprovedCases) * 100 / float64(gate.EligibleCases)
	}
	if gate.TotalCases > 0 {
		gate.AllCasesImprovedPercent = float64(gate.ImprovedCases) * 100 / float64(gate.TotalCases)
	}
	gate.Passed = gate.EligibleCases > 0 && len(gate.UnreviewedRuns) == 0 && len(gate.SafetyViolations) == 0 && gate.EligibleImprovedPercent >= 70
	return gate, nil
}

func validateDailyInsightNarrativeSafetyEvidence(locale string, section *health.DailyInsightNarrativeSection, evidence *DailyInsightNarrativeSafetyEvidence) string {
	if evidence == nil {
		return "missing semantic safety evidence"
	}
	identity := DailyInsightNarrativeCurrentReviewIdentity()
	if evidence.CandidateHash != DailyInsightNarrativeSafetyCandidateHash(locale, section) {
		return "semantic safety evidence does not match the stored candidate"
	}
	if evidence.SafetyPromptRevision != identity.SafetyPromptRevision || evidence.SafetyMaxOutputTokens != identity.SafetyMaxOutputTokens || evidence.ReviewFingerprint != identity.Fingerprint {
		return "semantic safety evidence does not match the active safety contract"
	}
	if evidence.Verdict != "allow" {
		return "semantic safety evidence did not allow the candidate"
	}
	// v6 receipts produced before explicit empty-slice serialization may decode
	// an allow verdict's required empty set as nil. The provider decoder had
	// already required categories, so this is a deterministic representation
	// normalization only for allow; reject evidence remains strict below.
	if evidence.Categories != nil && len(evidence.Categories) != 0 {
		return "semantic safety evidence has non-empty or missing categories"
	}
	return ""
}

// NormalizeDailyInsightNarrativeEvaluationOutput materializes the canonical
// empty category array for legacy v6 allow receipts. It never fills reject
// categories, so incomplete reject evidence remains fail-closed.
func NormalizeDailyInsightNarrativeEvaluationOutput(output *DailyInsightNarrativeEvaluationOutput) {
	if output == nil {
		return
	}
	for caseIndex := range output.Cases {
		for runIndex := range output.Cases[caseIndex].Runs {
			evidence := output.Cases[caseIndex].Runs[runIndex].SafetyEvidence
			if evidence != nil && evidence.Verdict == "allow" && evidence.Categories == nil {
				evidence.Categories = []string{}
			}
		}
	}
}

// caughtDailyInsightNarrativeSafetyReject recognizes the one non-renderable
// semantic-review outcome that is expected to be retained for audit. It is
// deliberately narrower than a generic validator rejection: the candidate
// text has been withheld, but the current reviewer receipt must still prove a
// structured reject with at least one closed category.
func caughtDailyInsightNarrativeSafetyReject(run DailyInsightNarrativeEvaluationRun) (bool, string) {
	if len(run.InvalidDomains) != 1 || run.InvalidDomains[health.DailyInsightNarrativeOverallSlot] == "" {
		return false, ""
	}
	if !strings.Contains(run.InvalidDomains[health.DailyInsightNarrativeOverallSlot], "daily insight safety review rejected candidate") {
		if run.SafetyEvidence != nil {
			return false, "semantic safety evidence is attached to a non-safety rejection"
		}
		if run.Narrative != nil && (run.Narrative.Overall != nil || len(run.Narrative.Domains) != 0) {
			return false, "generic validator rejection retains narrative content"
		}
		if len(run.Review.Domains) != 1 || run.Review.Domains[0].Key != health.DailyInsightNarrativeOverallSlot || run.Review.Domains[0].OutputStatus != "validator_rejected" {
			return false, "generic validator rejection has inconsistent review state"
		}
		return false, ""
	}
	if run.Narrative == nil || run.Narrative.Overall != nil || len(run.Narrative.Domains) != 0 {
		return false, "semantic safety reject retains renderable narrative content"
	}
	if len(run.Review.Domains) != 1 || run.Review.Domains[0].Key != health.DailyInsightNarrativeOverallSlot || run.Review.Domains[0].OutputStatus != "validator_rejected" {
		return false, "semantic safety reject has inconsistent review state"
	}
	if violation := validateDailyInsightNarrativeSafetyRejectEvidence(run.SafetyEvidence); violation != "" {
		return false, violation
	}
	return true, ""
}

func validateDailyInsightNarrativeSafetyRejectEvidence(evidence *DailyInsightNarrativeSafetyEvidence) string {
	if evidence == nil {
		return "missing semantic safety evidence for rejected candidate"
	}
	identity := DailyInsightNarrativeCurrentReviewIdentity()
	if evidence.SafetyPromptRevision != identity.SafetyPromptRevision || evidence.SafetyMaxOutputTokens != identity.SafetyMaxOutputTokens || evidence.ReviewFingerprint != identity.Fingerprint {
		return "semantic safety evidence does not match the active safety contract"
	}
	if evidence.Verdict != "reject" {
		return "semantic safety evidence does not record a safety reject"
	}
	if evidence.Categories == nil || len(evidence.Categories) == 0 {
		return "semantic safety reject has missing categories"
	}
	if len(evidence.CandidateHash) != sha256.Size*2 {
		return "semantic safety reject has invalid candidate hash"
	}
	if _, err := hex.DecodeString(evidence.CandidateHash); err != nil {
		return "semantic safety reject has invalid candidate hash"
	}
	allowed := make(map[string]struct{}, len(dailyInsightNarrativeSafetyCategories))
	for _, category := range dailyInsightNarrativeSafetyCategories {
		allowed[category] = struct{}{}
	}
	seen := make(map[string]struct{}, len(evidence.Categories))
	for _, category := range evidence.Categories {
		if _, ok := allowed[category]; !ok {
			return "semantic safety reject has invalid category"
		}
		if _, duplicate := seen[category]; duplicate {
			return "semantic safety reject has duplicate category"
		}
		seen[category] = struct{}{}
	}
	return ""
}

func hasNaturalnessScores(review DailyInsightNarrativeRunReview) bool {
	for _, row := range review.Domains {
		if row.OutputStatus == "valid" && (row.Naturalness != "pass" && row.Naturalness != "fail") {
			return false
		}
	}
	return true
}

func naturalnessPassed(review DailyInsightNarrativeRunReview) bool {
	for _, row := range review.Domains {
		if row.OutputStatus == "valid" && row.Naturalness != "pass" {
			return false
		}
	}
	return true
}

// checkDailyInsightNarrativeRunReview verifies a complete, one-per-domain
// worksheet. It returns whether the worksheet itself is complete, whether it
// is strictly better than fallback, and any reviewer-detected safety breach.
func checkDailyInsightNarrativeRunReview(snapshot health.DailyInsightSnapshot, locale string, narrative *health.DailyInsightNarrative, review DailyInsightNarrativeRunReview, providerErrors map[string]string, requireV4HumanScores bool) (bool, bool, []string) {
	expected := dailyInsightNarrativeReviewStatuses(snapshot, locale, narrative, nil, providerErrors)
	if len(review.Domains) != len(expected) {
		return false, false, nil
	}
	seen := make(map[string]struct{}, len(review.Domains))
	better := true
	violations := make([]string, 0)
	for _, domain := range review.Domains {
		status, wanted := expected[domain.Key]
		if !wanted {
			return false, false, nil
		}
		if _, duplicate := seen[domain.Key]; duplicate {
			return false, false, nil
		}
		seen[domain.Key] = struct{}{}
		if domain.OutputStatus != status || !validDailyInsightNarrativeDomainReview(domain, requireV4HumanScores) {
			return false, false, nil
		}
		if status != "valid" {
			better = false
			continue
		}
		if requireV4HumanScores && !v4FidelityConsistent(domain) {
			violations = append(violations, domain.Key+": aggregate fidelity contradicts claim, qualifier, or safety review")
		}
		if domain.ClaimFidelity == "fail" {
			violations = append(violations, domain.Key+": reviewer marked claim-fidelity failure")
		}
		if domain.QualifierFidelity == "fail" {
			violations = append(violations, domain.Key+": reviewer marked qualifier-fidelity failure")
		}
		if domain.Safety == "violation" {
			violations = append(violations, domain.Key+": reviewer marked a factual or safety violation")
		}
		if domain.Fidelity != "pass" || domain.ClaimFidelity != "pass" || domain.QualifierFidelity != "pass" || domain.Safety != "safe" || *domain.AddedMeaning != 2 || domain.ScreenDuplication != "none" || domain.Language != "pass" {
			better = false
		}
	}
	return len(seen) == len(expected), better, violations
}

// DailyInsightNarrativeRunReviewWorksheet creates the structural portion of
// a manual review. The evaluator writes an entry for every eligible domain,
// so a partial provider bundle cannot hide review of a returned neighbour.
func DailyInsightNarrativeRunReviewWorksheet(snapshot health.DailyInsightSnapshot, locale string, narrative *health.DailyInsightNarrative, invalidDomains, providerErrors map[string]string) DailyInsightNarrativeRunReview {
	statuses := dailyInsightNarrativeReviewStatuses(snapshot, locale, narrative, invalidDomains, providerErrors)
	domains := make([]DailyInsightNarrativeDomainReview, 0, len(statuses))
	for key, status := range statuses {
		domains = append(domains, DailyInsightNarrativeDomainReview{Key: key, OutputStatus: status})
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].Key < domains[j].Key })
	return DailyInsightNarrativeRunReview{Domains: domains}
}

func dailyInsightNarrativeReviewStatuses(snapshot health.DailyInsightSnapshot, locale string, narrative *health.DailyInsightNarrative, invalidDomains, providerErrors map[string]string) map[string]string {
	statuses := make(map[string]string)
	sections := make(map[string]*health.DailyInsightNarrativeSection)
	if narrative != nil {
		sections[health.DailyInsightNarrativeOverallSlot] = narrative.Overall
		for _, domain := range narrative.Domains {
			sections[domain.Key] = domain.Section
		}
	}
	for _, domain := range dailyInsightNarrativeReviewInputs(snapshot, locale) {
		// Overall-only v2 packets carry server-owned Facts rather than the
		// retired claim list. Keep legacy claim-only artifacts readable while
		// still emitting exactly one worksheet row for fact-based overall runs.
		if len(domain.Claims) == 0 && len(domain.Facts) == 0 {
			continue
		}
		status := "null"
		switch {
		case providerErrors[domain.Key] != "":
			status = "provider_error"
		case invalidDomains[domain.Key] != "":
			status = "validator_rejected"
		case sections[domain.Key] != nil:
			status = "valid"
		}
		statuses[domain.Key] = status
	}
	return statuses
}

func validDailyInsightNarrativeDomainReview(review DailyInsightNarrativeDomainReview, requireV4HumanScores bool) bool {
	if review.Key == "" {
		return false
	}
	if review.OutputStatus != "valid" {
		return review.OutputStatus == "null" || review.OutputStatus == "validator_rejected" || review.OutputStatus == "provider_error"
	}
	if review.ReviewReason == "" || review.AddedMeaning == nil {
		return false
	}
	if requireV4HumanScores && review.Fidelity != "pass" && review.Fidelity != "fail" {
		return false
	}
	if review.ClaimFidelity != "pass" && review.ClaimFidelity != "fail" {
		return false
	}
	if review.QualifierFidelity != "pass" && review.QualifierFidelity != "fail" {
		return false
	}
	if review.Safety != "safe" && review.Safety != "violation" {
		return false
	}
	if *review.AddedMeaning < 0 || *review.AddedMeaning > 2 {
		return false
	}
	if review.ScreenDuplication != "none" && review.ScreenDuplication != "domain" && review.ScreenDuplication != "hero" && review.ScreenDuplication != "both" {
		return false
	}
	if review.Language != "pass" && review.Language != "fail" {
		return false
	}
	return !requireV4HumanScores || review.Naturalness == "pass" || review.Naturalness == "fail"
}

func v4FidelityConsistent(review DailyInsightNarrativeDomainReview) bool {
	faithful := review.ClaimFidelity == "pass" && review.QualifierFidelity == "pass" && review.Safety == "safe"
	return (review.Fidelity == "pass") == faithful
}

func hasCompleteNarrative(snapshot *health.DailyInsightSnapshot, locale string, narrative *health.DailyInsightNarrative) bool {
	if narrative == nil {
		return false
	}
	sections := make(map[string]*health.DailyInsightNarrativeSection, len(narrative.Domains))
	sections[health.DailyInsightNarrativeOverallSlot] = narrative.Overall
	for _, domain := range narrative.Domains {
		sections[domain.Key] = domain.Section
	}
	for _, domain := range dailyInsightNarrativeReviewInputs(*snapshot, locale) {
		if len(domain.Claims) > 0 && sections[domain.Key] == nil {
			return false
		}
	}
	return true
}

func dailyInsightNarrativeReviewInputs(snapshot health.DailyInsightSnapshot, locale string) []health.DailyInsightNarrativeDomainInput {
	inputs := make([]health.DailyInsightNarrativeDomainInput, 0, 1)
	input, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, health.DailyInsightNarrativeOverallSlot)
	if known && health.HasEligibleDailyInsightNarrativeSlot(&snapshot, locale, health.DailyInsightNarrativeOverallSlot) {
		inputs = append(inputs, input.Slot)
	}
	return inputs
}

// dailyInsightNarrativeCorpusCoverageInputs includes all structurally valid
// packet variants. It is distinct from the serving/review inputs because an
// action-like or generic packet can be valid history while still lacking a
// meaning that justifies a provider call.
func dailyInsightNarrativeCorpusCoverageInputs(snapshot health.DailyInsightSnapshot, locale string) []health.DailyInsightNarrativeDomainInput {
	inputs := make([]health.DailyInsightNarrativeDomainInput, 0, 4)
	if overall, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, health.DailyInsightNarrativeOverallSlot); known {
		inputs = append(inputs, overall.Slot)
	}
	inputs = append(inputs, health.BuildDailyInsightNarrativeInput(&snapshot, locale).Domains...)
	return inputs
}

func sameCorpusTags(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func DailyInsightNarrativeFallbacks(snapshot health.DailyInsightSnapshot, locale string) []NarrativeCorpusFallback {
	fallbacks := make([]NarrativeCorpusFallback, 0, len(snapshot.Domains)+1)
	claimsByDomain := make(map[string]health.DailyInsightNarrativeClaim)
	if overall, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, health.DailyInsightNarrativeOverallSlot); known && len(overall.Slot.Claims) == 1 {
		claimsByDomain[overall.Slot.Key] = overall.Slot.Claims[0]
		fallbacks = append(fallbacks, narrativeCorpusFallback(health.DailyInsightNarrativeOverallSlot, "server_claim", snapshot.Primary.Title, snapshot.Primary.Observation, snapshot.Primary.Meaning))
	} else {
		fallbacks = append(fallbacks, narrativeCorpusFallback(health.DailyInsightNarrativeOverallSlot, "deterministic_fallback", snapshot.Primary.Title, snapshot.Primary.Observation, snapshot.Primary.Meaning))
	}
	for _, domain := range health.BuildDailyInsightNarrativeInput(&snapshot, locale).Domains {
		if len(domain.Claims) == 1 {
			claimsByDomain[domain.Key] = domain.Claims[0]
		}
	}
	for _, domain := range snapshot.Domains {
		if _, found := claimsByDomain[domain.Key]; found {
			fallbacks = append(fallbacks, narrativeCorpusFallback(domain.Key, "server_claim", domain.Summary, domain.Insight.Observation, domain.Insight.Meaning))
			continue
		}
		fallbacks = append(fallbacks, narrativeCorpusFallback(domain.Key, "deterministic_fallback", domain.Summary, domain.Insight.Observation, domain.Insight.Meaning))
	}
	return fallbacks
}

func narrativeCorpusFallback(key, summary, context, observation, meaning string) NarrativeCorpusFallback {
	observation = strings.TrimSpace(observation)
	if observation == "" {
		observation = strings.TrimSpace(context)
	}
	return NarrativeCorpusFallback{
		Key: key, Summary: summary, Context: strings.TrimSpace(context), Observation: observation, Meaning: strings.TrimSpace(meaning),
	}
}

func narrativeCorpusFallbackCopy(fallback NarrativeCorpusFallback) string {
	parts := make([]string, 0, 3)
	if fallback.Context != "" {
		parts = append(parts, fallback.Context)
	}
	if fallback.Observation != "" {
		parts = append(parts, fallback.Observation)
	}
	if fallback.Meaning != "" {
		parts = append(parts, fallback.Meaning)
	}
	return strings.Join(parts, " — ")
}
