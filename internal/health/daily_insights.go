package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// DailyInsightSnapshot is the deterministic, client-safe basis for Today.
// It deliberately contains no model output: policy chooses the domain,
// evidence, and allowed action before any narrative provider is involved.
const DailyInsightSnapshotVersion = "daily-insight-v2"

// These versions are part of the material contract. Changing policy or the
// action catalogue must invalidate a previously generated narrative even if
// the visible health values happen to be unchanged.
const (
	DailyInsightPolicyVersion        = "daily-insight-policy-v2"
	DailyInsightActionCatalogVersion = "daily-insight-actions-v1"
	DailyInsightPromptRevision       = "daily-insight-prompt-v3"
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

type DailyInsightDomain struct {
	Key         string                  `json:"key"`
	Band        string                  `json:"band"`
	DataState   string                  `json:"data_state"`
	Confidence  string                  `json:"confidence,omitempty"`
	AsOf        *time.Time              `json:"as_of,omitempty"`
	Summary     string                  `json:"summary"`
	Insight     DailyInsight            `json:"insight"`
	Destination DailyInsightDestination `json:"destination"`
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

// DailyInsightNarrative is a provider acknowledgement of one closed
// server-authored presentation template. It deliberately contains no free
// prose: state, wording, action, destination, and evidence ownership stay
// with the factual snapshot and server policy.
type DailyInsightNarrative struct {
	Primary DailyInsightNarrativeSection  `json:"primary"`
	Domains []DailyInsightNarrativeDomain `json:"domains"`
}

type DailyInsightNarrativeSection struct {
	Template    string   `json:"template"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type DailyInsightNarrativeDomain struct {
	Key string `json:"key"`
	DailyInsightNarrativeSection
}

// ApplyDailyInsightNarrative marks a server-authored template as ready only
// when the provider returns the exact closed template and evidence already
// selected by policy for every visible domain. A partial acknowledgement is
// not ready: deterministic factual copy remains visible while it is retried.
func ApplyDailyInsightNarrative(snapshot *DailyInsightSnapshot, narrative DailyInsightNarrative) (*DailyInsightSnapshot, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("daily insight snapshot is nil")
	}
	out := cloneDailyInsightSnapshot(snapshot)
	if err := applyDailyInsightNarrativeSection(&out.Primary, narrative.Primary, out.Primary.EvidenceIDs); err != nil {
		return nil, fmt.Errorf("primary narrative: %w", err)
	}

	byKey := make(map[string]int, len(out.Domains))
	for index, domain := range out.Domains {
		byKey[domain.Key] = index
	}
	seen := make(map[string]struct{}, len(narrative.Domains))
	for _, candidate := range narrative.Domains {
		index, known := byKey[candidate.Key]
		if !known {
			return nil, fmt.Errorf("unknown narrative domain %q", candidate.Key)
		}
		if _, duplicate := seen[candidate.Key]; duplicate {
			return nil, fmt.Errorf("duplicate narrative domain %q", candidate.Key)
		}
		seen[candidate.Key] = struct{}{}
		if err := applyDailyInsightNarrativeSection(
			&out.Domains[index].Insight,
			candidate.DailyInsightNarrativeSection,
			out.Domains[index].Insight.EvidenceIDs,
		); err != nil {
			return nil, fmt.Errorf("domain %q narrative: %w", candidate.Key, err)
		}
	}
	for _, domain := range out.Domains {
		if _, ok := seen[domain.Key]; !ok {
			return nil, fmt.Errorf("missing narrative domain %q", domain.Key)
		}
	}
	return out, nil
}

func cloneDailyInsightSnapshot(snapshot *DailyInsightSnapshot) *DailyInsightSnapshot {
	out := *snapshot
	out.Primary.EvidenceIDs = append([]string(nil), snapshot.Primary.EvidenceIDs...)
	out.Domains = append([]DailyInsightDomain(nil), snapshot.Domains...)
	for index := range out.Domains {
		out.Domains[index].Insight.EvidenceIDs = append([]string(nil), snapshot.Domains[index].Insight.EvidenceIDs...)
	}
	out.Evidence = append([]DailyInsightEvidence(nil), snapshot.Evidence...)
	out.Changes = append([]DailyInsightChange(nil), snapshot.Changes...)
	return &out
}

func applyDailyInsightNarrativeSection(target *DailyInsight, candidate DailyInsightNarrativeSection, allowedEvidenceIDs []string) error {
	if target == nil {
		return fmt.Errorf("target is nil")
	}
	if candidate.Template != "server_default" {
		return fmt.Errorf("unapproved narrative template %q", candidate.Template)
	}
	if len(candidate.EvidenceIDs) == 0 || len(candidate.EvidenceIDs) > 2 {
		return fmt.Errorf("expected one or two evidence IDs")
	}
	allowed := make(map[string]struct{}, len(allowedEvidenceIDs))
	for _, id := range allowedEvidenceIDs {
		allowed[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(candidate.EvidenceIDs))
	for _, id := range candidate.EvidenceIDs {
		if _, allowed := allowed[id]; !allowed {
			return fmt.Errorf("unapproved evidence ID %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate evidence ID %q", id)
		}
		seen[id] = struct{}{}
	}
	target.EvidenceIDs = append([]string(nil), candidate.EvidenceIDs...)
	// The visible wording remains server-authored. A provider acknowledgement
	// may narrow evidence selection, but it must not reclassify that copy as
	// provider-authored text.
	return nil
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
