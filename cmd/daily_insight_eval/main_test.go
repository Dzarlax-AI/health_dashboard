package main

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func TestOfflineReviewHTMLTemplateEscapesNarrativeAndKeepsDownloadWorkflow(t *testing.T) {
	output := ai.DailyInsightNarrativeEvaluationOutput{
		Provider: "openai", Model: "test-model", Reasoning: "none", RunsPerCase: 3,
		Cases: []ai.DailyInsightNarrativeEvaluationCase{{
			ID: "case-01", Locale: "en", Mode: "narrative_candidate",
			Runs: []ai.DailyInsightNarrativeEvaluationRun{{
				Narrative: &health.DailyInsightNarrative{Domains: []health.DailyInsightNarrativeDomain{{Key: "sleep", Section: &health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{Text: "</script><img src=x>"}}}}}},
				Review:    ai.DailyInsightNarrativeRunReview{Domains: []ai.DailyInsightNarrativeDomainReview{{Key: "sleep", OutputStatus: "valid"}}},
			}},
		}},
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := json.Marshal(ai.DailyInsightNarrativeReviewPacket{Cases: []ai.DailyInsightNarrativeReviewPacketCase{{
		ID: "case-01", Fallbacks: []ai.NarrativeCorpusFallback{{Key: "sleep", Summary: "deterministic_fallback", Context: "Server context", Observation: "Server observation", Meaning: "Server meaning"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	if err := offlineReviewHTMLTemplate.Execute(&rendered, offlineReviewHTMLData{CorpusHash: "abc", EvaluationJSON: string(encoded), AnchorJSON: `{}`, ReviewPacketJSON: string(packet)}); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	for _, want := range []string{"Daily Insight B1 — human review", "Download draft", "Download completed review", "download-complete", "added_meaning", "domain.review_reason=area.value;updateProgress()", "const artifact=JSON.parse(", "const serverAnchors=JSON.parse(", "const reviewPacket=JSON.parse(", "fallbackText(item,key)", "Review the exact server fallback below.", "deterministic fallback", "reviewRules(item,review.key,section)", "Server facts: ", "Server-approved story: ", "Model narrative: ", "Model-cited interpretation: ", "Other permitted interpretations, not cited: ", "Exact visible server copy: ", "const corpusHash=\"abc\";", "JSON.stringify(artifact,null,2)+'\\n'"} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered review missing %q", want)
		}
	}
	if strings.Contains(html, "JSON.stringify(artifact,null,2)+'\\\\n'") {
		t.Fatal("worksheet must append a JSON newline, not literal backslash-n bytes")
	}
	if strings.Contains(html, "</script><img src=x>") {
		t.Fatalf("narrative escaped script boundary leaked: %s", html)
	}
	if strings.Contains(html, "innerHTML") {
		t.Fatal("review worksheet must render provider text through textContent")
	}
}

func TestOfflineReviewServerAnchorsAllowsPrivacyMinimizedClaimOnlySlot(t *testing.T) {
	corpus := ai.DailyInsightNarrativeCorpus{Cases: []ai.DailyInsightNarrativeCorpusCase{{
		ID: "recovery-claim-only", Locale: "en",
		Snapshot: health.DailyInsightSnapshot{
			Domains: []health.DailyInsightDomain{{
				Key: "recovery", Band: "optimal", DataState: "fresh", Confidence: "final",
				Insight: health.DailyInsight{
					State: "insight", AnswerKind: health.DailyInsightAnswerFactual,
					ClaimID: "recovery_readiness_context", EvidenceIDs: []string{"recovery-evidence"},
				},
			}},
			Evidence: []health.DailyInsightEvidence{{
				ID: "recovery-evidence", Domain: "recovery", DataState: "fresh", Confidence: "final",
			}},
		},
	}}}
	review := ai.DailyInsightNarrativeEvaluationOutput{Cases: []ai.DailyInsightNarrativeEvaluationCase{{
		ID: "recovery-claim-only", Locale: "en",
		Runs: []ai.DailyInsightNarrativeEvaluationRun{{Narrative: &health.DailyInsightNarrative{
			Version: health.DailyInsightNarrativeVersion, Locale: "en",
			Domains: []health.DailyInsightNarrativeDomain{{Key: "recovery", Section: &health.DailyInsightNarrativeSection{}}},
		}}},
	}}}

	anchors, err := offlineReviewServerAnchors(corpus, review)
	if err != nil {
		t.Fatalf("claim-only slot rejected: %v", err)
	}
	if _, found := anchors["recovery-claim-only"][0]["recovery"]; found {
		t.Fatal("claim-only slot should rely on the review packet, not invent a fact or story panel")
	}
}

func TestValidateOfflineReviewRowsAllowsBlankRubricButRejectsStructuralEdits(t *testing.T) {
	want := ai.DailyInsightNarrativeRunReview{Domains: []ai.DailyInsightNarrativeDomainReview{{Key: "sleep", OutputStatus: "valid"}, {Key: "energy", OutputStatus: "validator_rejected"}}}
	if err := validateOfflineReviewRows(want, want); err != nil {
		t.Fatalf("blank reviewer rubric rejected: %v", err)
	}
	if err := validateOfflineReviewRows(want, ai.DailyInsightNarrativeRunReview{}); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Fatalf("missing worksheet rows accepted: %v", err)
	}
	changed := want
	changed.Domains = append([]ai.DailyInsightNarrativeDomainReview(nil), want.Domains...)
	changed.Domains[0].OutputStatus = "null"
	if err := validateOfflineReviewRows(want, changed); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("changed worksheet status accepted: %v", err)
	}
}

func TestValidateOfflineNarrativeRunRejectsTextInRejectedSlot(t *testing.T) {
	snapshot := health.DailyInsightSnapshot{}
	run := ai.DailyInsightNarrativeEvaluationRun{
		Narrative: &health.DailyInsightNarrative{
			Version: health.DailyInsightNarrativeVersion,
			Locale:  "en",
			Overall: &health.DailyInsightNarrativeSection{},
		},
		InvalidDomains: map[string]string{health.DailyInsightNarrativeOverallSlot: "semantic validation failed"},
	}
	if err := validateOfflineNarrativeRun(snapshot, "en", run); err == nil || !strings.Contains(err.Error(), "retains model text") {
		t.Fatalf("rejected slot text was accepted: %v", err)
	}
}

func TestRunIndependentEvaluationRunsPreservesOrderAndBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, evaluationRunConcurrency)
	release := make(chan struct{})
	finished := make(chan []ai.DailyInsightNarrativeEvaluationRun, 1)
	var inFlight, maxInFlight int32

	go func() {
		finished <- runIndependentEvaluationRuns(3, func(run int) ai.DailyInsightNarrativeEvaluationRun {
			current := atomic.AddInt32(&inFlight, 1)
			for {
				previous := atomic.LoadInt32(&maxInFlight)
				if current <= previous || atomic.CompareAndSwapInt32(&maxInFlight, previous, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			atomic.AddInt32(&inFlight, -1)
			return ai.DailyInsightNarrativeEvaluationRun{Attempts: run + 1}
		})
	}()

	for range 3 {
		<-started
	}
	close(release)
	runs := <-finished
	if maxInFlight != evaluationRunConcurrency {
		t.Fatalf("maximum concurrency = %d, want %d", maxInFlight, evaluationRunConcurrency)
	}
	for index, run := range runs {
		if want := index + 1; run.Attempts != want {
			t.Fatalf("run %d attempts = %d, want %d", index, run.Attempts, want)
		}
	}
}

func TestEvaluatorRegistryDSNUsesIsolationRegistryWhenEnabled(t *testing.T) {
	values := map[string]string{
		"TENANT_DB_ISOLATION_ENABLED":     "true",
		"ADMIN_DATABASE_URL":              "postgres://admin@example/health",
		"REGISTRY_DATABASE_URL":           "postgres://registry@example/health",
		"TENANT_DATABASE_URL_BASE":        "postgres://example/health",
		"TENANT_DB_MASTER_SECRET":         "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"TENANT_DB_MASTER_SECRET_VERSION": "1",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("postgres://tenant@example/health", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if want := values["REGISTRY_DATABASE_URL"]; got != want {
		t.Fatalf("registry dsn = %q, want %q", got, want)
	}
}

func TestResolveActiveDatabaseProviderConfigUsesOnlyActiveAdminConfig(t *testing.T) {
	stored := storage.AIConfig{
		Provider: ai.ProviderOpenAI,
		Providers: map[string]storage.AIProviderSettings{
			ai.ProviderOpenAI: {APIKey: "configured-openai-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
			ai.ProviderGemini: {APIKey: "configured-gemini-key", Model: "gemini-3-flash-preview", ReasoningEffort: "minimal"},
		},
		MaxOutputTokens: ai.DailyInsightMaxTokens,
	}
	provider, active, err := resolveActiveDatabaseProviderConfig(stored, "", "", "")
	if err != nil {
		t.Fatalf("resolve active database config: %v", err)
	}
	if provider.Descriptor().ID != ai.ProviderOpenAI || active.Model != "gpt-5.6-luna" || active.ReasoningEffort != "none" {
		t.Fatalf("resolved active config = provider=%s config=%+v", provider.Descriptor().ID, active)
	}
}

func TestResolveActiveDatabaseProviderConfigRejectsAlternateConfiguredProvider(t *testing.T) {
	stored := storage.AIConfig{
		Provider: ai.ProviderOpenAI,
		Providers: map[string]storage.AIProviderSettings{
			ai.ProviderOpenAI: {APIKey: "configured-openai-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
			ai.ProviderGemini: {APIKey: "configured-gemini-key", Model: "gemini-3-flash-preview", ReasoningEffort: "minimal"},
		},
		MaxOutputTokens: ai.DailyInsightMaxTokens,
	}
	_, _, err := resolveActiveDatabaseProviderConfig(stored, ai.ProviderGemini, "", "")
	if err == nil || !strings.Contains(err.Error(), "active Admin provider") {
		t.Fatalf("alternate provider error = %v", err)
	}
}

func TestResolveActiveDatabaseProviderConfigRejectsActiveConfigOverrides(t *testing.T) {
	stored := storage.AIConfig{
		Provider: ai.ProviderOpenAI,
		Providers: map[string]storage.AIProviderSettings{
			ai.ProviderOpenAI: {APIKey: "configured-openai-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
		},
		MaxOutputTokens: ai.DailyInsightMaxTokens,
	}
	for _, request := range []struct{ model, reasoning string }{
		{model: "gpt-5.6-sol"},
		{reasoning: "low"},
	} {
		_, _, err := resolveActiveDatabaseProviderConfig(stored, ai.ProviderOpenAI, request.model, request.reasoning)
		if err == nil {
			t.Fatalf("override model=%q reasoning=%q unexpectedly passed", request.model, request.reasoning)
		}
	}
}

func TestEvaluatorRegistryDSNAllowsNoLegacyURLWhenIsolationIsEnabled(t *testing.T) {
	values := map[string]string{
		"TENANT_DB_ISOLATION_ENABLED":     "true",
		"ADMIN_DATABASE_URL":              "postgres://admin@example/health",
		"REGISTRY_DATABASE_URL":           "postgres://registry@example/health",
		"TENANT_DATABASE_URL_BASE":        "postgres://example/health",
		"TENANT_DB_MASTER_SECRET":         "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"TENANT_DB_MASTER_SECRET_VERSION": "1",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if want := values["REGISTRY_DATABASE_URL"]; got != want {
		t.Fatalf("registry dsn = %q, want %q", got, want)
	}
}

func TestEvaluatorRegistryDSNAllowsStandardPostgresEnvironment(t *testing.T) {
	values := map[string]string{
		"PGHOST":     "database.example",
		"PGDATABASE": "health",
		"PGUSER":     "readonly",
		"PGSSLMODE":  "verify-full",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if got != "" {
		t.Fatalf("registry dsn = %q, want empty PG* resolved DSN", got)
	}
}

func TestEvaluatorRegistryDSNRejectsRemoteStandardPostgresWithoutTLS(t *testing.T) {
	values := map[string]string{
		"PGHOST":     "database.example",
		"PGDATABASE": "health",
		"PGUSER":     "readonly",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	_, err := evaluatorRegistryDSN("", lookup)
	if err == nil || !strings.Contains(err.Error(), "PGSSLMODE") {
		t.Fatalf("error = %v, want remote TLS rejection", err)
	}
}

func TestEvaluatorRegistryDSNAllowsLocalStandardPostgresWithoutTLS(t *testing.T) {
	values := map[string]string{
		"PGHOST":     "127.0.0.1",
		"PGDATABASE": "health",
		"PGUSER":     "readonly",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if got != "" {
		t.Fatalf("registry dsn = %q, want empty PG* resolved DSN", got)
	}
}

func TestEvaluatorRegistryDSNAllowsTailscaleStandardPostgresWithoutTLS(t *testing.T) {
	values := map[string]string{
		"PGHOST":     "100.104.66.65",
		"PGDATABASE": "health",
		"PGUSER":     "readonly",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if got != "" {
		t.Fatalf("registry dsn = %q, want empty PG* resolved DSN", got)
	}
}

func TestGlobalAIConfigUsesInstallationWideProviderSettings(t *testing.T) {
	config := globalAIConfig(map[string]string{
		"ai_provider":                   ai.ProviderOpenAI,
		"openai_api_key":                "configured-openai-key",
		"openai_model":                  "gpt-5.6-luna",
		"openai_reasoning_effort":       "medium",
		"gemini_api_key":                "configured-gemini-key",
		"gemini_model":                  "gemini-2.5-flash",
		"gemini_reasoning_effort":       "low",
		"ai_max_output_tokens":          "1200",
		"unregistered_provider_api_key": "must-not-be-imported",
	})

	if config.Provider != ai.ProviderOpenAI {
		t.Fatalf("provider = %q, want %q", config.Provider, ai.ProviderOpenAI)
	}
	if config.MaxOutputTokens != 1200 {
		t.Fatalf("max_output_tokens = %d, want 1200", config.MaxOutputTokens)
	}
	if got := config.SettingsFor(ai.ProviderOpenAI); got.APIKey != "configured-openai-key" || got.Model != "gpt-5.6-luna" || got.ReasoningEffort != "medium" {
		t.Fatalf("openai settings = %#v", got)
	}
	if got := config.SettingsFor(ai.ProviderGemini); got.APIKey != "configured-gemini-key" || got.Model != "gemini-2.5-flash" || got.ReasoningEffort != "low" {
		t.Fatalf("gemini settings = %#v", got)
	}
	if _, exists := config.Providers["unregistered_provider"]; exists {
		t.Fatal("unregistered provider was imported into evaluator defaults")
	}
}
