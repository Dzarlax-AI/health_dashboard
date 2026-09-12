package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"health-receiver/internal/health"
)

// DailyInsightNarrativeCorpus is the versioned, anonymized input to the B1
// product-quality gate. Snapshot values are never sent verbatim to a model:
// GenerateDailyInsightNarrative derives its closed claim packet first.
//
// A corpus is deliberately external to the production database. Freezing its
// exact JSON and its checksum makes a model/prompt comparison reproducible
// without retaining raw records or treating a later live request as evidence.
type DailyInsightNarrativeCorpus struct {
	Version string                            `json:"version"`
	Cases   []DailyInsightNarrativeCorpusCase `json:"cases"`
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

// DailyInsightNarrativeCandidate is an anonymized review input plus structural
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
}

// SnapshotForEvaluation restores the closed, non-display variants that are
// intentionally excluded from a live client snapshot. A frozen corpus must
// preserve these variants or it would review a different provider packet from
// the one that production serves. Today only energy has such a variant, and
// its value is a small server-owned enum rather than free text or a number.
func (item DailyInsightNarrativeCorpusCase) SnapshotForEvaluation() (health.DailyInsightSnapshot, error) {
	snapshot := item.Snapshot
	snapshot.Domains = append([]health.DailyInsightDomain(nil), item.Snapshot.Domains...)
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

func validCorpusEnergyNarrativeSubject(value string) bool {
	switch value {
	case "rest", "active_recovery", "push_hard":
		return true
	default:
		return false
	}
}

// SanitizeDailyInsightNarrativeCorpusCandidate removes identifiers, dates,
// display copy, actions and measurements from a snapshot while retaining the
// closed state needed to derive the exact B1 claim packet. The result is a
// candidate, not a frozen corpus: callers must supply review tags and any
// non-serving scenario provenance separately.
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
		Date:       "review-day",
		Version:    snapshot.Version,
		Domains:    make([]health.DailyInsightDomain, 0, len(snapshot.Domains)),
		Evidence:   make([]health.DailyInsightEvidence, 0, len(snapshot.Evidence)),
		Changes:    []health.DailyInsightChange{},
		HasMore:    false,
		DecisionID: "",
		Primary:    health.DailyInsight{},
	}
	if snapshot.UpdatedAt != nil {
		fixedUpdate := time.Date(2000, time.January, 1, 12, 0, 0, 0, time.UTC)
		sanitized.UpdatedAt = &fixedUpdate
	}
	for _, domain := range snapshot.Domains {
		sanitized.Domains = append(sanitized.Domains, health.DailyInsightDomain{
			Key:        domain.Key,
			Band:       domain.Band,
			DataState:  domain.DataState,
			Confidence: domain.Confidence,
			Insight: health.DailyInsight{
				State:       domain.Insight.State,
				AnswerKind:  domain.Insight.AnswerKind,
				ClaimID:     domain.Insight.ClaimID,
				GapReason:   domain.Insight.GapReason,
				Remediation: domain.Insight.Remediation,
				EvidenceIDs: mapEvidenceIDs(domain.Insight.EvidenceIDs),
				Fallback:    domain.Insight.Fallback,
			},
		})
	}
	for index, evidence := range snapshot.Evidence {
		sanitized.Evidence = append(sanitized.Evidence, health.DailyInsightEvidence{
			ID:               fmt.Sprintf("evidence-%02d", index+1),
			Domain:           evidence.Domain,
			ComparisonPeriod: evidence.ComparisonPeriod,
			DataState:        evidence.DataState,
			Confidence:       evidence.Confidence,
		})
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
	return DailyInsightNarrativeCorpusCase{
		ID:                candidateID,
		Locale:            locale,
		Origin:            DailyInsightNarrativeOriginObserved,
		Snapshot:          sanitized,
		NarrativeSubjects: subjects,
	}
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
}

const (
	DailyInsightNarrativeCorpusMinCases          = 20
	DailyInsightNarrativeCorpusMaxCases          = 30
	DailyInsightNarrativeCorpusMaxSyntheticCases = 4

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

// ValidateDailyInsightNarrativeCorpus validates the frozen gate input before
// any provider call. It rejects an under-sized hand-picked happy path, a
// missing required product state, and malformed claim eligibility.
func ValidateDailyInsightNarrativeCorpus(corpus DailyInsightNarrativeCorpus) error {
	if corpus.Version != "daily-insight-narrative-corpus-v1" {
		return fmt.Errorf("unsupported corpus version %q", corpus.Version)
	}
	if len(corpus.Cases) < DailyInsightNarrativeCorpusMinCases || len(corpus.Cases) > DailyInsightNarrativeCorpusMaxCases {
		return fmt.Errorf("corpus has %d cases; want %d to %d", len(corpus.Cases), DailyInsightNarrativeCorpusMinCases, DailyInsightNarrativeCorpusMaxCases)
	}
	ids := make(map[string]struct{}, len(corpus.Cases))
	tags := make(map[string]struct{})
	eligibleLocales := make(map[string]struct{})
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
			if !containsRequiredCorpusTag(item.Tags) {
				return fmt.Errorf("synthetic case %q must cover at least one required product state", item.ID)
			}
		default:
			return fmt.Errorf("case %q has unsupported origin %q", item.ID, item.Origin)
		}
		if item.Snapshot.Date == "" || item.Snapshot.Version == "" || len(item.Snapshot.Domains) == 0 {
			return fmt.Errorf("case %q has incomplete sanitized snapshot", item.ID)
		}
		for _, tag := range item.Tags {
			if tag == "" {
				return fmt.Errorf("case %q has empty tag", item.ID)
			}
			tags[tag] = struct{}{}
		}
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			return err
		}
		if err := validateDailyInsightNarrativeCorpusCase(item, snapshot); err != nil {
			return err
		}
		if health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) {
			eligibleLocales[item.Locale] = struct{}{}
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
	missingLocales := make([]string, 0, 3)
	for _, locale := range []string{"en", "ru", "sr"} {
		if _, present := eligibleLocales[locale]; !present {
			missingLocales = append(missingLocales, locale)
		}
	}
	if len(missingLocales) != 0 {
		return fmt.Errorf("corpus has no narrative-eligible cases for locales: %v", missingLocales)
	}
	if syntheticCases > DailyInsightNarrativeCorpusMaxSyntheticCases {
		return fmt.Errorf("corpus has %d synthetic cases; want at most %d", syntheticCases, DailyInsightNarrativeCorpusMaxSyntheticCases)
	}
	return nil
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
			if item.Scenario.UpdateKind != "late_source_update" || item.Snapshot.UpdatedAt == nil {
				return fmt.Errorf("case %q tags late_source_update without update provenance and timestamp", item.ID)
			}
		case "no_data":
			if health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) || !narrativeCorpusHasOnlyUnavailableDomains(snapshot) {
				return fmt.Errorf("case %q tags no_data but remains narrative-eligible or has a fresh domain", item.ID)
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
// comparison baseline. Original server display copy is deliberately absent
// from the anonymized corpus, so this derives a locale-safe reference from the
// same server-owned claim packet rather than retaining personal copy.
// It is output only from an already anonymized corpus and is never provider
// input.
type NarrativeCorpusFallback struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Observation string `json:"observation"`
	Meaning     string `json:"meaning"`
}

// DailyInsightNarrativeReviewPacket is the offline, pre-provider artifact a
// product reviewer uses to inspect corpus composition, allowed propositions
// and the deterministic comparison baseline. It contains only frozen,
// anonymized corpus material.
type DailyInsightNarrativeReviewPacket struct {
	Version    string                                  `json:"version"`
	CorpusHash string                                  `json:"corpus_hash"`
	Cases      []DailyInsightNarrativeReviewPacketCase `json:"cases"`
}

type DailyInsightNarrativeReviewPacketCase struct {
	ID        string                              `json:"id"`
	Locale    string                              `json:"locale"`
	Origin    string                              `json:"origin"`
	Tags      []string                            `json:"tags"`
	Mode      string                              `json:"mode"`
	Claims    []health.DailyInsightNarrativeClaim `json:"claims"`
	Fallbacks []NarrativeCorpusFallback           `json:"fallbacks"`
}

func BuildDailyInsightNarrativeReviewPacket(corpus DailyInsightNarrativeCorpus) (DailyInsightNarrativeReviewPacket, error) {
	hash, err := DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		return DailyInsightNarrativeReviewPacket{}, err
	}
	packet := DailyInsightNarrativeReviewPacket{
		Version: "daily-insight-narrative-review-packet-v1", CorpusHash: hash,
		Cases: make([]DailyInsightNarrativeReviewPacketCase, 0, len(corpus.Cases)),
	}
	for _, item := range corpus.Cases {
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			return DailyInsightNarrativeReviewPacket{}, err
		}
		claims := make([]health.DailyInsightNarrativeClaim, 0, len(snapshot.Domains))
		for _, domain := range health.BuildDailyInsightNarrativeInput(&snapshot, item.Locale).Domains {
			claims = append(claims, domain.Claims...)
		}
		mode := "deterministic_fallback"
		if len(claims) > 0 {
			mode = "narrative_candidate"
		}
		origin := item.Origin
		if origin == "" {
			origin = DailyInsightNarrativeOriginObserved
		}
		packet.Cases = append(packet.Cases, DailyInsightNarrativeReviewPacketCase{
			ID: item.ID, Locale: item.Locale, Origin: origin, Tags: append([]string(nil), item.Tags...), Mode: mode,
			Claims: claims, Fallbacks: DailyInsightNarrativeFallbacks(snapshot, item.Locale),
		})
	}
	return packet, nil
}

// DailyInsightNarrativeRunReview is completed by a human product reviewer
// after a frozen evaluation run. A model cannot grade its own prose: the
// review makes the usefulness threshold auditable instead of an impression
// left in a chat transcript.
type DailyInsightNarrativeRunReview struct {
	Usefulness string `json:"usefulness,omitempty"` // better_than_fallback | not_better
	Safety     string `json:"safety,omitempty"`     // safe | violation
	Notes      string `json:"notes,omitempty"`
}

type DailyInsightNarrativeEvaluationRun struct {
	Narrative      *health.DailyInsightNarrative  `json:"narrative,omitempty"`
	InvalidDomains map[string]string              `json:"invalid_domains,omitempty"`
	Error          string                         `json:"error,omitempty"`
	Attempts       int                            `json:"attempts"`
	InputTokens    int                            `json:"input_tokens"`
	OutputTokens   int                            `json:"output_tokens"`
	Review         DailyInsightNarrativeRunReview `json:"review,omitempty"`
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
	Version            string                                `json:"version"`
	CorpusHash         string                                `json:"corpus_hash"`
	GeneratedAt        time.Time                             `json:"generated_at"`
	Provider           string                                `json:"provider"`
	Model              string                                `json:"model"`
	Reasoning          string                                `json:"reasoning"`
	PromptRevision     string                                `json:"prompt_revision"`
	ClaimPacketVersion string                                `json:"claim_packet_version"`
	NarrativeVersion   string                                `json:"narrative_version"`
	ReviewFingerprint  string                                `json:"review_fingerprint"`
	RunsPerCase        int                                   `json:"runs_per_case"`
	Cases              []DailyInsightNarrativeEvaluationCase `json:"cases"`
}

// DailyInsightNarrativeQualityGate records the conservative release rule.
// The denominator is every preselected corpus case, including fallback-only
// data states, so an evaluator cannot improve its score by dropping hard
// cases after seeing model outputs.
type DailyInsightNarrativeQualityGate struct {
	TotalCases       int      `json:"total_cases"`
	EligibleCases    int      `json:"eligible_cases"`
	ImprovedCases    int      `json:"improved_cases"`
	ImprovedPercent  float64  `json:"improved_percent"`
	UnreviewedRuns   []string `json:"unreviewed_runs"`
	SafetyViolations []string `json:"safety_violations"`
	Passed           bool     `json:"passed"`
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
	if output.Version != "daily-insight-narrative-evaluation-v1" {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("unsupported evaluation version %q", output.Version)
	}
	if output.CorpusHash != corpusHash {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation corpus hash does not match frozen corpus")
	}
	if output.RunsPerCase != 3 {
		return DailyInsightNarrativeQualityGate{}, fmt.Errorf("evaluation has %d runs per case; want 3", output.RunsPerCase)
	}
	identity := DailyInsightNarrativeCurrentReviewIdentity()
	if output.PromptRevision != identity.PromptRevision ||
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
		eligible := health.HasEligibleDailyInsightNarrativeClaims(&snapshot, expected.Locale)
		if !eligible {
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
			if run.Error != "" {
				caseImproved = false
				continue
			}
			if len(run.InvalidDomains) != 0 {
				caseImproved = false
				gate.SafetyViolations = append(gate.SafetyViolations, label+": provider output failed semantic validation")
				continue
			}
			if run.Narrative == nil {
				caseImproved = false
				continue
			}
			validated, invalidDomains, err := health.ValidateDailyInsightNarrative(&snapshot, expected.Locale, *run.Narrative)
			if err != nil || len(invalidDomains) != 0 {
				caseImproved = false
				gate.SafetyViolations = append(gate.SafetyViolations, label+": stored narrative no longer passes semantic validation")
				continue
			}
			if !hasCompleteNarrative(&snapshot, expected.Locale, &validated) {
				caseImproved = false
				continue
			}
			switch run.Review.Safety {
			case "safe":
			case "violation":
				caseImproved = false
				gate.SafetyViolations = append(gate.SafetyViolations, label+": reviewer marked a factual or safety violation")
				continue
			default:
				caseImproved = false
				gate.UnreviewedRuns = append(gate.UnreviewedRuns, label+": missing safety review")
				continue
			}
			switch run.Review.Usefulness {
			case "better_than_fallback":
			case "not_better":
				caseImproved = false
			default:
				caseImproved = false
				gate.UnreviewedRuns = append(gate.UnreviewedRuns, label+": missing usefulness review")
			}
		}
		if caseImproved {
			gate.ImprovedCases++
		}
	}
	if gate.TotalCases > 0 {
		gate.ImprovedPercent = float64(gate.ImprovedCases) * 100 / float64(gate.TotalCases)
	}
	gate.Passed = len(gate.UnreviewedRuns) == 0 && len(gate.SafetyViolations) == 0 && gate.ImprovedPercent >= 70
	return gate, nil
}

func hasCompleteNarrative(snapshot *health.DailyInsightSnapshot, locale string, narrative *health.DailyInsightNarrative) bool {
	if narrative == nil {
		return false
	}
	sections := make(map[string]*health.DailyInsightNarrativeSection, len(narrative.Domains))
	for _, domain := range narrative.Domains {
		sections[domain.Key] = domain.Section
	}
	for _, domain := range health.BuildDailyInsightNarrativeInput(snapshot, locale).Domains {
		if len(domain.Claims) > 0 && sections[domain.Key] == nil {
			return false
		}
	}
	return true
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
	fallbacks := make([]NarrativeCorpusFallback, 0, len(snapshot.Domains))
	claimsByDomain := make(map[string]health.DailyInsightNarrativeClaim)
	for _, domain := range health.BuildDailyInsightNarrativeInput(&snapshot, locale).Domains {
		if len(domain.Claims) == 1 {
			claimsByDomain[domain.Key] = domain.Claims[0]
		}
	}
	for _, domain := range snapshot.Domains {
		if claim, found := claimsByDomain[domain.Key]; found {
			fallbacks = append(fallbacks, NarrativeCorpusFallback{
				Key: domain.Key, Summary: "server_claim", Observation: claim.Proposition, Meaning: "server_owned_context",
			})
			continue
		}
		fallbacks = append(fallbacks, NarrativeCorpusFallback{
			Key: domain.Key, Summary: "deterministic_fallback", Observation: "server_factual_context", Meaning: "no_narrative_claim",
		})
	}
	return fallbacks
}
