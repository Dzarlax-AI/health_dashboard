package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestEvaluationProviderConfigFromEnvironment(t *testing.T) {
	t.Setenv("AI_INSIGHT_EVAL_TEST_KEY", "test-only-key")
	provider, config, err := evaluationProviderConfig(false, "AI_INSIGHT_EVAL_TEST_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Descriptor().ID != "openai" || config.Model != "gpt-6-luna" || config.ReasoningEffort != "medium" || config.APIKey != "test-only-key" {
		t.Fatalf("unexpected evaluation configuration: provider=%s model=%s reasoning=%s key_present=%t",
			provider.Descriptor().ID, config.Model, config.ReasoningEffort, config.APIKey != "")
	}
}

func TestClassifyAIInsightFailure(t *testing.T) {
	cases := []struct {
		err     error
		receipt *ai.AIInsightReviewReceipt
		want    string
	}{
		{errors.New("upstream"), &ai.AIInsightReviewReceipt{Verdict: "reject"}, "review_rejected"},
		{&ai.AIInsightReviewError{Kind: "provider", Err: errors.New("upstream")}, nil, "review_provider"},
		{&ai.DailyInsightNarrativeSemanticError{Err: errors.New("invalid")}, nil, "candidate_rejected"},
		{context.DeadlineExceeded, nil, "timeout"},
		{errors.New("upstream"), nil, "provider_error"},
	}
	for _, item := range cases {
		if got := classifyAIInsightFailure(item.err, item.receipt); got != item.want {
			t.Errorf("classification = %q, want %q", got, item.want)
		}
	}
}

func TestEvaluationSiblingStateMatchesServing(t *testing.T) {
	for status, want := range map[string]string{
		"valid": "ready", "null": "disabled", "ineligible": "disabled",
		"rejected_or_provider_error": "failed",
	} {
		if got := evaluationSiblingState(status); got != want {
			t.Errorf("status %q mapped to %q, want %q", status, got, want)
		}
	}
}

type evaluationTestProvider struct {
	responses []ai.GenerationResult
	errors    []error
	calls     int
}

func (p *evaluationTestProvider) Descriptor() ai.ProviderDescriptor {
	return ai.ProviderDescriptor{ID: "test"}
}
func (p *evaluationTestProvider) ListModels(context.Context, string) ([]ai.Model, error) {
	return nil, nil
}
func (p *evaluationTestProvider) Generate(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
	index := p.calls
	p.calls++
	if index >= len(p.responses) {
		return ai.GenerationResult{}, fmt.Errorf("unexpected call %d", index)
	}
	var err error
	if index < len(p.errors) {
		err = p.errors[index]
	}
	return p.responses[index], err
}

func TestEvaluateSlotAccountsForAuthorAndReviewer(t *testing.T) {
	item := testEvaluationCorpus().Cases[0]
	input, eligible := health.BuildAIInsightInput(item.Snapshot(), item.Locale, "overall", nil)
	if !eligible {
		t.Fatal("test input not eligible")
	}
	author := ai.GenerationResult{Text: fmt.Sprintf(`{"version":%q,"locale":"en","slot":"overall","insight":{"text":"Sleep was shorter than usual, so keep the day flexible.","fact_ids":["sleep-fact"],"stance":"qualify","alternative_action":""}}`, health.AIInsightVersion),
		InputTokens: 100, OutputTokens: 20, Attempts: 1, Latency: 10 * time.Millisecond}
	review := ai.GenerationResult{Text: `{"version":"today-ai-second-opinion-review-v5.1","verdict":"allow","categories":[],"language_review":{"text":{"reader_gender":"neutral","serbian_ai_voice":"not_applicable","evidence":""},"alternative_action":{"reader_gender":"neutral","serbian_ai_voice":"not_applicable","evidence":""}}}`,
		InputTokens: 60, OutputTokens: 12, Attempts: 2, Latency: 25 * time.Millisecond}
	provider := &evaluationTestProvider{responses: []ai.GenerationResult{author, review}}
	got := evaluateSlot(provider, ai.ProviderConfig{}, input, "overall", true)
	if got.Status != "valid" || got.InputTokens != 160 || got.OutputTokens != 32 || got.LatencyMS != 35 || got.Attempts != 3 ||
		got.AuthorInputTokens != 100 || got.ReviewInputTokens != 60 || got.ReviewAttempts != 2 || provider.calls != 2 {
		t.Fatalf("usage = %#v calls=%d", got, provider.calls)
	}

	provider = &evaluationTestProvider{responses: []ai.GenerationResult{author, review}, errors: []error{nil, errors.New("review unavailable")}}
	got = evaluateSlot(provider, ai.ProviderConfig{}, input, "overall", true)
	if got.FailureKind != "review_provider" || got.ReviewInputTokens != 60 || got.InputTokens != 160 {
		t.Fatalf("review failure dropped usage: %#v", got)
	}

	provider = &evaluationTestProvider{responses: []ai.GenerationResult{{Text: fmt.Sprintf(`{"version":%q,"locale":"en","slot":"overall","insight":null}`, health.AIInsightVersion), InputTokens: 40, Attempts: 1}}}
	got = evaluateSlot(provider, ai.ProviderConfig{}, input, "overall", true)
	if got.Status != "null" || got.ReviewAttempts != 0 || got.InputTokens != 40 || provider.calls != 1 {
		t.Fatalf("null usage = %#v calls=%d", got, provider.calls)
	}
}

func TestEvaluateSlotRecordsContentFreeValidationCode(t *testing.T) {
	item := testEvaluationCorpus().Cases[0]
	input, _ := health.BuildAIInsightInput(item.Snapshot(), item.Locale, "overall", nil)
	provider := &evaluationTestProvider{responses: []ai.GenerationResult{{Text: fmt.Sprintf(`{"version":%q,"locale":"en","slot":"overall","insight":{"text":"Your score is 99 today.","fact_ids":["sleep-fact"],"stance":"agree","alternative_action":""}}`, health.AIInsightVersion), InputTokens: 40, Attempts: 1}}}
	got := evaluateSlot(provider, ai.ProviderConfig{}, input, "overall", true)
	if got.FailureKind != "candidate_rejected" || got.FailureCode != "unsupported_number" || got.ReviewAttempts != 0 {
		t.Fatalf("validation code = %#v", got)
	}
}

func TestEvaluationProviderConfigRequiresKey(t *testing.T) {
	t.Setenv("AI_INSIGHT_EVAL_TEST_KEY", "")
	_, _, err := evaluationProviderConfig(false, "AI_INSIGHT_EVAL_TEST_KEY")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestTemporaryOutputDirectory(t *testing.T) {
	for _, path := range []string{"/tmp", "/tmp/evaluation", "/private/tmp", "/private/tmp/evaluation"} {
		if !temporaryOutputDirectory(path) {
			t.Fatalf("expected temp path %q to be accepted", path)
		}
	}
	for _, path := range []string{"/", "/tmpother", "/private/tmpother", "/home/user"} {
		if temporaryOutputDirectory(path) {
			t.Fatalf("expected non-temp path %q to be rejected", path)
		}
	}
}

func TestPrepareEvaluationCorpusSelectsExactValidatedSentinelsWithoutChangingCorpusHash(t *testing.T) {
	corpus := testEvaluationCorpus()
	path := writeEvaluationCorpus(t, corpus)
	wantHash, err := ai.AIInsightCorpusHash(corpus)
	if err != nil {
		t.Fatal(err)
	}
	_, selected, hash, selection, err := prepareEvaluationCorpus(path, "observed-003,observed-001")
	if err != nil || hash != wantHash || selection == nil || selection.Mode != "case_ids" || strings.Join(selection.CaseIDs, ",") != "observed-003,observed-001" || len(selected) != 2 || selected[0].ID != "observed-003" || selected[1].ID != "observed-001" {
		t.Fatalf("selection=%#v selected=%#v hash=%q wantHash=%q err=%v", selection, selected, hash, wantHash, err)
	}
	_, full, fullHash, fullSelection, err := prepareEvaluationCorpus(path, "")
	if err != nil || fullSelection != nil || fullHash != wantHash || len(full) != len(corpus.Cases) {
		t.Fatalf("normal full run changed: selected=%d selection=%#v hash=%q err=%v", len(full), fullSelection, fullHash, err)
	}
}

func TestPrepareEvaluationCorpusRejectsUnknownAndDuplicateSelectionAfterValidation(t *testing.T) {
	path := writeEvaluationCorpus(t, testEvaluationCorpus())
	for _, requested := range []string{"unknown-case", "observed-001,observed-001"} {
		if _, _, _, _, err := prepareEvaluationCorpus(path, requested); err == nil {
			t.Fatalf("invalid selection %q was accepted", requested)
		}
	}
}

func TestPrepareEvaluationCorpusValidatesBeforeSelectionOrProviderConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-corpus.json")
	data, err := json.Marshal(ai.AIInsightCorpus{Version: ai.AIInsightCorpusVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := prepareEvaluationCorpus(path, "unknown-case"); err == nil || !strings.Contains(err.Error(), "20-30 cases") {
		t.Fatalf("full-corpus validation did not precede selection: %v", err)
	}
}

func testEvaluationCorpus() ai.AIInsightCorpus {
	locales := []string{"en", "ru", "sr"}
	tags := []string{"agreement", "justified_disagreement", "unjustified_disagreement", "conflicting_actions", "partial", "stale", "no_data"}
	corpus := ai.AIInsightCorpus{Version: ai.AIInsightCorpusVersion}
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
		corpus.Cases = append(corpus.Cases, ai.AIInsightCorpusCase{ID: fmt.Sprintf("observed-%03d", index+1), Locale: locales[index%3], Origin: "observed_aggregate", Tags: []string{tag}, Primary: primary,
			Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: domainState, Insight: domain}}, Facts: facts})
	}
	return corpus
}

func writeEvaluationCorpus(t *testing.T, corpus ai.AIInsightCorpus) string {
	t.Helper()
	data, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
