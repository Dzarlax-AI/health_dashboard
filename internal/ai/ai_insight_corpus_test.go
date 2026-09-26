package ai

import (
	"fmt"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func testAIInsightCorpus() AIInsightCorpus {
	locales := []string{"en", "ru", "sr"}
	tags := []string{"agreement", "justified_disagreement", "unjustified_disagreement", "conflicting_actions", "partial", "stale", "no_data"}
	corpus := AIInsightCorpus{Version: AIInsightCorpusVersion}
	for index := 0; index < 20; index++ {
		tag := tags[index%len(tags)]
		domainState := "fresh"
		if tag == "partial" {
			domainState = "partial"
		}
		if tag == "stale" {
			domainState = "stale"
		}
		facts := []health.DailyInsightNarrativeFact{{ID: "sleep-fact", Domain: "sleep", Statement: "Sleep was shorter than usual.", Fresh: true, Authority: "server_derived", EvidenceIDs: []string{"sleep"}}}
		if tag == "partial" {
			facts = append(facts, health.DailyInsightNarrativeFact{ID: "energy-fact", Domain: "energy", Statement: "Energy context is available.", Fresh: true, Authority: "server_derived", EvidenceIDs: []string{"energy"}})
		}
		if tag == "no_data" {
			facts = nil
		}
		primary := health.DailyInsight{State: "insight", Observation: "A measured day is reasonable."}
		domain := health.DailyInsight{State: "insight", Observation: "Sleep context is available."}
		if tag == "conflicting_actions" {
			primary.NextStep = &health.DailyInsightAction{ID: "rest", Text: "Rest today."}
			domain.NextStep = &health.DailyInsightAction{ID: "walk", Text: "Take a walk."}
		}
		corpus.Cases = append(corpus.Cases, AIInsightCorpusCase{
			ID: fmt.Sprintf("observed-%03d", index+1), Locale: locales[index%3], Origin: "observed_aggregate", Tags: []string{tag},
			Primary: primary,
			Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: domainState, Insight: domain}},
			Facts:   facts,
		})
	}
	return corpus
}

func TestAIInsightCorpusRequiresCoverageAndRejectsOverlays(t *testing.T) {
	corpus := testAIInsightCorpus()
	first, err := AIInsightCorpusHash(corpus)
	if err != nil || first == "" {
		t.Fatalf("hash=%q err=%v", first, err)
	}
	corpus.Cases[0].Facts[0].Statement = "A different derived fact."
	second, err := AIInsightCorpusHash(corpus)
	if err != nil || first == second {
		t.Fatalf("changed fact did not change hash: %q %q err=%v", first, second, err)
	}
	corpus.Cases[0].Domains[0].AsOf = new(time.Time)
	if err := ValidateAIInsightCorpus(corpus); err == nil {
		t.Fatal("timestamp-bearing case was accepted")
	}
	corpus = testAIInsightCorpus()
	corpus.Cases[0].Tags = nil
	for index := 1; index < len(corpus.Cases); index++ {
		if corpus.Cases[index].Tags[0] == "agreement" {
			corpus.Cases[index].Tags = nil
		}
	}
	if err := ValidateAIInsightCorpus(corpus); err == nil {
		t.Fatal("missing agreement coverage was accepted")
	}
}
