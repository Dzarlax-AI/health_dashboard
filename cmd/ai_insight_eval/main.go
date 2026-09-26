// ai_insight_eval evaluates one reviewed, immutable derived corpus against the
// new independent AI Insight contract. It never reads a tenant database or
// changes serving state. Provider calls require a separate explicit flag.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

type evaluation struct {
	Version           string               `json:"version"`
	CorpusHash        string               `json:"corpus_hash"`
	Selection         *evaluationSelection `json:"selection,omitempty"`
	Model             string               `json:"model"`
	Reasoning         string               `json:"reasoning"`
	PromptRevision    string               `json:"prompt_revision"`
	ReviewFingerprint string               `json:"review_fingerprint"`
	RunsPerCase       int                  `json:"runs_per_case"`
	Cases             []evaluationCase     `json:"cases"`
}

// evaluationSelection records an explicit sentinel run while CorpusHash stays
// bound to the fully reviewed, unfiltered corpus.
type evaluationSelection struct {
	Mode    string   `json:"mode"`
	CaseIDs []string `json:"case_ids"`
}

type evaluationCase struct {
	ID     string          `json:"id"`
	Locale string          `json:"locale"`
	Origin string          `json:"origin"`
	Tags   []string        `json:"tags"`
	Runs   []evaluationRun `json:"runs"`
}

type evaluationRun struct {
	Slots []evaluationSlot `json:"slots"`
}

type evaluationSlot struct {
	Key                string                        `json:"key"`
	InputHash          string                        `json:"input_hash,omitempty"`
	Status             string                        `json:"status"`
	FailureKind        string                        `json:"failure_kind,omitempty"`
	FailureCode        string                        `json:"failure_code,omitempty"`
	Insight            *health.DailyInsightAIInsight `json:"insight,omitempty"`
	Review             *ai.AIInsightReviewReceipt    `json:"review,omitempty"`
	InputTokens        int64                         `json:"input_tokens,omitempty"`
	OutputTokens       int64                         `json:"output_tokens,omitempty"`
	LatencyMS          int64                         `json:"latency_ms,omitempty"`
	Attempts           int                           `json:"attempts,omitempty"`
	AuthorInputTokens  int64                         `json:"author_input_tokens,omitempty"`
	AuthorOutputTokens int64                         `json:"author_output_tokens,omitempty"`
	AuthorLatencyMS    int64                         `json:"author_latency_ms,omitempty"`
	AuthorAttempts     int                           `json:"author_attempts,omitempty"`
	ReviewInputTokens  int64                         `json:"review_input_tokens,omitempty"`
	ReviewOutputTokens int64                         `json:"review_output_tokens,omitempty"`
	ReviewLatencyMS    int64                         `json:"review_latency_ms,omitempty"`
	ReviewAttempts     int                           `json:"review_attempts,omitempty"`
}

func main() {
	corpusPath := flag.String("corpus", "", "reviewed ai-insight-corpus-v1 JSON")
	outPath := flag.String("out", "", "new JSON result file under the system temporary directory")
	validateOnly := flag.Bool("validate", false, "validate corpus and print its SHA-256 without provider calls")
	confirmed := flag.Bool("confirmed-anonymized", false, "operator confirms manual review of every derived corpus case before transfer")
	apiKeyEnv := flag.String("api-key-env", "OPENAI_API_KEY", "environment variable containing the configured provider key")
	databaseConfig := flag.Bool("database-config", false, "read active provider settings from the backend registry without exposing its key")
	checkConfig := flag.Bool("check-config", false, "verify the active provider configuration without a corpus or provider call")
	runs := flag.Int("runs", 3, "independent runs per case; quality gate requires exactly three")
	caseIDs := flag.String("case-ids", "", "optional exact comma-separated reviewed corpus case IDs to evaluate as sentinels")
	flag.Parse()
	if *checkConfig {
		_, cfg, err := evaluationProviderConfig(*databaseConfig, *apiKeyEnv)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("provider=openai configured=true model=%s reasoning=%s\n", cfg.Model, cfg.ReasoningEffort)
		return
	}
	if *corpusPath == "" {
		log.Fatal("--corpus is required")
	}
	_, selectedCases, hash, selection, err := prepareEvaluationCorpus(*corpusPath, *caseIDs)
	if err != nil {
		log.Fatal(err)
	}
	if *validateOnly {
		fmt.Println(hash)
		return
	}
	if !*confirmed || *runs != 3 || *outPath == "" {
		log.Fatal("provider evaluation requires --confirmed-anonymized, --runs=3 and --out")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(*outPath))
	if err != nil || !temporaryOutputDirectory(parent) {
		log.Fatal("--out must be in an existing directory under /tmp or /private/tmp")
	}
	provider, cfg, err := evaluationProviderConfig(*databaseConfig, *apiKeyEnv)
	if err != nil {
		log.Fatal(err)
	}
	result := evaluation{Version: "ai-insight-evaluation-v2", CorpusHash: hash, Model: cfg.Model, Reasoning: cfg.ReasoningEffort,
		PromptRevision: ai.AIInsightPromptRevision, ReviewFingerprint: ai.AIInsightReviewFingerprint(), RunsPerCase: *runs,
		Selection: selection, Cases: make([]evaluationCase, 0, len(selectedCases))}
	for index, item := range selectedCases {
		caseResult := evaluationCase{ID: item.ID, Locale: item.Locale, Origin: item.Origin, Tags: item.Tags}
		for run := 0; run < *runs; run++ {
			caseResult.Runs = append(caseResult.Runs, evaluateRun(provider, cfg, item))
		}
		result.Cases = append(result.Cases, caseResult)
		log.Printf("evaluated case %d/%d", index+1, len(selectedCases))
	}
	file, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("AI Insight evaluation saved: %d cases, 3 runs, corpus %s\n", len(result.Cases), hash)
}

func temporaryOutputDirectory(parent string) bool {
	return parent == "/tmp" || strings.HasPrefix(parent, "/tmp/") ||
		parent == "/private/tmp" || strings.HasPrefix(parent, "/private/tmp/")
}

func evaluationProviderConfig(databaseConfig bool, apiKeyEnv string) (ai.Provider, ai.ProviderConfig, error) {
	provider, err := ai.GetProvider(ai.ProviderOpenAI)
	if err != nil {
		return nil, ai.ProviderConfig{}, err
	}
	config := ai.ProviderConfig{Model: "gpt-6-luna", ReasoningEffort: "medium", MaxOutputTokens: ai.DailyInsightMaxTokens}
	if !databaseConfig {
		config.APIKey = os.Getenv(apiKeyEnv)
		if config.APIKey == "" {
			return nil, ai.ProviderConfig{}, fmt.Errorf("configured provider key is unavailable in the named environment variable")
		}
		return provider, config, nil
	}
	isolation, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil {
		return nil, ai.ProviderConfig{}, fmt.Errorf("parse tenant isolation configuration: %w", err)
	}
	if !isolation.Enabled {
		return nil, ai.ProviderConfig{}, fmt.Errorf("--database-config requires the isolated backend registry environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	reg, err := registry.New(ctx, isolation.RegistryDSN)
	if err != nil {
		return nil, ai.ProviderConfig{}, fmt.Errorf("connect to provider registry: %w", err)
	}
	defer reg.Close()
	settings, err := reg.LoadAllGlobalSettings(ctx)
	if err != nil {
		return nil, ai.ProviderConfig{}, fmt.Errorf("read provider configuration: %w", err)
	}
	maxTokens, _ := strconv.Atoi(settings["ai_max_output_tokens"])
	if maxTokens == 0 {
		maxTokens, _ = strconv.Atoi(settings["gemini_max_tokens"])
	}
	stored := storage.AIConfig{Provider: settings["ai_provider"], Providers: make(map[string]storage.AIProviderSettings), MaxOutputTokens: maxTokens}
	for _, descriptor := range ai.ProviderDescriptors() {
		stored.SetSettingsFor(descriptor.ID, storage.AIProviderSettings{
			APIKey: settings[descriptor.ID+"_api_key"], Model: settings[descriptor.ID+"_model"], ReasoningEffort: settings[descriptor.ID+"_reasoning_effort"],
		})
	}
	activeProvider, active, err := storage.ResolveTodayInsightsB1ProviderConfig(stored)
	if err != nil {
		return nil, ai.ProviderConfig{}, err
	}
	if activeProvider.Descriptor().ID != ai.ProviderOpenAI || active.Model != config.Model || active.ReasoningEffort != config.ReasoningEffort {
		return nil, ai.ProviderConfig{}, fmt.Errorf("active Admin provider/model/reasoning does not match openai/gpt-6-luna/medium evaluation contract")
	}
	if active.APIKey == "" {
		return nil, ai.ProviderConfig{}, fmt.Errorf("active Admin provider key is unavailable")
	}
	config.APIKey = active.APIKey
	return provider, config, nil
}

func loadCorpus(path string) (ai.AIInsightCorpus, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ai.AIInsightCorpus{}, "", err
	}
	var corpus ai.AIInsightCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		return corpus, "", err
	}
	hash, err := ai.AIInsightCorpusHash(corpus)
	return corpus, hash, err
}

// prepareEvaluationCorpus validates the complete reviewed corpus before any
// optional sentinel selection. It performs no provider configuration or call.
func prepareEvaluationCorpus(path, requestedCaseIDs string) (ai.AIInsightCorpus, []ai.AIInsightCorpusCase, string, *evaluationSelection, error) {
	corpus, hash, err := loadCorpus(path)
	if err != nil {
		return ai.AIInsightCorpus{}, nil, "", nil, err
	}
	cases, selection, err := selectEvaluationCases(corpus, requestedCaseIDs)
	if err != nil {
		return ai.AIInsightCorpus{}, nil, "", nil, err
	}
	return corpus, cases, hash, selection, nil
}

func selectEvaluationCases(corpus ai.AIInsightCorpus, requestedCaseIDs string) ([]ai.AIInsightCorpusCase, *evaluationSelection, error) {
	if requestedCaseIDs == "" {
		return corpus.Cases, nil, nil
	}
	byID := make(map[string]ai.AIInsightCorpusCase, len(corpus.Cases))
	for _, item := range corpus.Cases {
		byID[item.ID] = item
	}
	ids := strings.Split(requestedCaseIDs, ",")
	selected := make([]ai.AIInsightCorpusCase, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, nil, fmt.Errorf("--case-ids must contain exact, non-empty comma-separated case IDs")
		}
		if seen[id] {
			return nil, nil, fmt.Errorf("duplicate selected case ID %q", id)
		}
		item, ok := byID[id]
		if !ok {
			return nil, nil, fmt.Errorf("unknown selected case ID %q", id)
		}
		seen[id] = true
		selected = append(selected, item)
	}
	return selected, &evaluationSelection{Mode: "case_ids", CaseIDs: append([]string(nil), ids...)}, nil
}

func evaluateRun(provider ai.Provider, cfg ai.ProviderConfig, item ai.AIInsightCorpusCase) evaluationRun {
	snapshot := item.Snapshot()
	out := evaluationRun{Slots: make([]evaluationSlot, 0, 4)}
	siblings := make([]health.AIInsightSibling, 0, 3)
	for _, slot := range []string{"sleep", "recovery", "energy"} {
		input, eligible := health.BuildAIInsightInput(snapshot, item.Locale, slot, nil)
		result := evaluateSlot(provider, cfg, input, slot, eligible)
		out.Slots = append(out.Slots, result)
		sibling := health.AIInsightSibling{Slot: slot, State: result.Status}
		if result.Insight != nil {
			sibling.Text, sibling.AlternativeAction = result.Insight.Text, result.Insight.AlternativeAction
		}
		siblings = append(siblings, sibling)
	}
	input, eligible := health.BuildAIInsightInput(snapshot, item.Locale, "overall", siblings)
	out.Slots = append(out.Slots, evaluateSlot(provider, cfg, input, "overall", eligible))
	return out
}

func evaluateSlot(provider ai.Provider, cfg ai.ProviderConfig, input health.AIInsightInput, slot string, eligible bool) evaluationSlot {
	result := evaluationSlot{Key: slot, Status: "ineligible"}
	if !eligible {
		return result
	}
	result.InputHash = health.AIInsightInputHash(input)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	generated, err := ai.GenerateAIInsightSlot(ctx, provider, cfg, input)
	result.AuthorInputTokens, result.AuthorOutputTokens, result.AuthorLatencyMS, result.AuthorAttempts =
		generated.InputTokens, generated.OutputTokens, generated.Latency.Milliseconds(), generated.Attempts
	result.ReviewInputTokens, result.ReviewOutputTokens, result.ReviewLatencyMS, result.ReviewAttempts =
		generated.ReviewUsage.InputTokens, generated.ReviewUsage.OutputTokens, generated.ReviewUsage.Latency.Milliseconds(), generated.ReviewUsage.Attempts
	result.InputTokens = result.AuthorInputTokens + result.ReviewInputTokens
	result.OutputTokens = result.AuthorOutputTokens + result.ReviewOutputTokens
	result.LatencyMS = result.AuthorLatencyMS + result.ReviewLatencyMS
	result.Attempts = result.AuthorAttempts + result.ReviewAttempts
	result.Review = generated.Review
	if err != nil {
		result.Status = "rejected_or_provider_error"
		result.FailureKind = classifyAIInsightFailure(err, result.Review)
		result.FailureCode = classifyAIInsightFailureCode(err)
		return result
	}
	if generated.Insight == nil {
		result.Status = "null"
		return result
	}
	result.Status, result.Insight = "valid", generated.Insight
	return result
}

func classifyAIInsightFailureCode(err error) string {
	var validation *health.AIInsightValidationError
	if errors.As(err, &validation) {
		return validation.Code
	}
	var syntax *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &typeError) {
		return "invalid_json"
	}
	return ""
}

func classifyAIInsightFailure(err error, receipt *ai.AIInsightReviewReceipt) string {
	if receipt != nil && receipt.Verdict == "reject" {
		return "review_rejected"
	}
	var reviewErr *ai.AIInsightReviewError
	if errors.As(err, &reviewErr) {
		return "review_" + reviewErr.Kind
	}
	var semanticErr *ai.DailyInsightNarrativeSemanticError
	if errors.As(err, &semanticErr) {
		return "candidate_rejected"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "provider_error"
}
