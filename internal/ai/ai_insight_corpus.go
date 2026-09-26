package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"health-receiver/internal/health"
)

const AIInsightCorpusVersion = "ai-insight-corpus-v1"

// AIInsightCorpus contains only reviewed, derived B0 display material. It is
// deliberately independent of the older closed-narrative evaluation format.
// A human must inspect every case before any provider call; structural
// validation alone cannot prove that free text contains no identifying data.
type AIInsightCorpus struct {
	Version string                `json:"version"`
	Cases   []AIInsightCorpusCase `json:"cases"`
}

type AIInsightCorpusCase struct {
	ID      string                             `json:"id"`
	Locale  string                             `json:"locale"`
	Origin  string                             `json:"origin"`
	Tags    []string                           `json:"tags"`
	Primary health.DailyInsight                `json:"primary"`
	Domains []health.DailyInsightDomain        `json:"domains"`
	Facts   []health.DailyInsightNarrativeFact `json:"facts"`
}

func (item AIInsightCorpusCase) Snapshot() *health.DailyInsightSnapshot {
	return &health.DailyInsightSnapshot{Primary: item.Primary,
		Domains:        append([]health.DailyInsightDomain(nil), item.Domains...),
		NarrativeFacts: append([]health.DailyInsightNarrativeFact(nil), item.Facts...)}
}

func ValidateAIInsightCorpus(corpus AIInsightCorpus) error {
	if corpus.Version != AIInsightCorpusVersion || len(corpus.Cases) < 20 || len(corpus.Cases) > 30 {
		return fmt.Errorf("AI Insight corpus requires version %q and 20-30 cases", AIInsightCorpusVersion)
	}
	ids, locales, tags := map[string]bool{}, map[string]bool{}, map[string]bool{}
	synthetic := 0
	for _, item := range corpus.Cases {
		if item.ID == "" || ids[item.ID] || strings.ContainsAny(item.ID, " /\\") {
			return fmt.Errorf("invalid or duplicate AI Insight case ID %q", item.ID)
		}
		ids[item.ID] = true
		if item.Locale != "en" && item.Locale != "ru" && item.Locale != "sr" {
			return fmt.Errorf("invalid locale for case %q", item.ID)
		}
		locales[item.Locale] = true
		if item.Origin != "observed_aggregate" && item.Origin != "synthetic_controlled" {
			return fmt.Errorf("invalid origin for case %q", item.ID)
		}
		if item.Origin == "synthetic_controlled" {
			synthetic++
		}
		if item.Primary.State == "" || len(item.Domains) == 0 {
			return fmt.Errorf("case %q lacks the reviewed server view", item.ID)
		}
		if item.Primary.Narrative != nil {
			return fmt.Errorf("case %q retains an old model overlay", item.ID)
		}
		hasPartial, hasStale, hasConflictingAction := false, false, false
		for _, domain := range item.Domains {
			if domain.AsOf != nil || domain.AIInsight != nil || domain.Insight.Narrative != nil {
				return fmt.Errorf("case %q retains timestamps or model overlays", item.ID)
			}
			hasPartial = hasPartial || domain.DataState == "partial"
			hasStale = hasStale || domain.DataState == "stale"
			if item.Primary.NextStep != nil && domain.Insight.NextStep != nil && item.Primary.NextStep.Text != domain.Insight.NextStep.Text {
				hasConflictingAction = true
			}
		}
		if (containsAIInsightTag(item.Tags, "partial") && !hasPartial) ||
			(containsAIInsightTag(item.Tags, "stale") && !hasStale) ||
			(containsAIInsightTag(item.Tags, "conflicting_actions") && !hasConflictingAction) {
			return fmt.Errorf("case %q has unsupported state tags", item.ID)
		}
		for _, tag := range item.Tags {
			tags[tag] = true
		}
		seenFacts := map[string]bool{}
		for _, fact := range item.Facts {
			if fact.ID == "" || seenFacts[fact.ID] || fact.Statement == "" || fact.Authority == "raw" {
				return fmt.Errorf("case %q has invalid derived fact", item.ID)
			}
			seenFacts[fact.ID] = true
		}
		_, overallEligible := health.BuildAIInsightInput(item.Snapshot(), item.Locale, "overall", nil)
		if containsAIInsightTag(item.Tags, "no_data") && overallEligible {
			return fmt.Errorf("case %q marks available facts as no_data", item.ID)
		}
		if !overallEligible &&
			!containsAIInsightTag(item.Tags, "no_data") && !containsAIInsightTag(item.Tags, "stale") {
			return fmt.Errorf("case %q is ineligible without an explicit fallback tag", item.ID)
		}
	}
	if synthetic > 5 || len(locales) != 3 {
		return fmt.Errorf("AI Insight corpus needs EN/RU/SR and at most five controlled synthetic cases")
	}
	for _, tag := range []string{"agreement", "justified_disagreement", "unjustified_disagreement", "conflicting_actions", "partial", "stale", "no_data"} {
		if !tags[tag] {
			return fmt.Errorf("AI Insight corpus lacks %q coverage", tag)
		}
	}
	return nil
}

func containsAIInsightTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}

func AIInsightCorpusHash(corpus AIInsightCorpus) (string, error) {
	if err := ValidateAIInsightCorpus(corpus); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(corpus)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
