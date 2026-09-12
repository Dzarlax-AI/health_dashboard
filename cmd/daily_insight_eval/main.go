// daily_insight_eval runs a frozen, anonymized B1 corpus against one explicit
// provider/model configuration. By default it is offline from production. Its
// explicit --database-config mode reads only the configured provider settings
// from an already-authorized backend database connection; it never reads raw
// health data, changes tenant flags, or writes anywhere except the caller-
// selected review artifact.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func main() {
	corpusPath := flag.String("corpus", "", "path to a frozen anonymized corpus JSON file")
	outPath := flag.String("out", "", "path for JSON review output")
	checkPath := flag.String("check", "", "validate a manually reviewed evaluation JSON against --corpus")
	validateOnly := flag.Bool("validate", false, "validate and print the checksum of --corpus without calling a provider")
	prepareReview := flag.Bool("prepare-review", false, "write offline claim/fallback review packet without calling a provider")
	checkConfig := flag.Bool("check-config", false, "verify provider configuration without reading a corpus or calling a provider")
	providerID := flag.String("provider", "", "explicit provider id")
	model := flag.String("model", "", "explicit model id; provider default if empty")
	reasoning := flag.String("reasoning", "", "explicit reasoning effort; provider default if empty")
	apiKeyEnv := flag.String("api-key-env", "", "environment variable containing the provider key")
	databaseConfig := flag.Bool("database-config", false, "load the selected provider configuration from installation-wide Admin settings")
	databaseURLEnv := flag.String("database-url-env", "DATABASE_URL", "environment variable containing the database URL when --database-config is set")
	runs := flag.Int("runs", 3, "independent generations per eligible case")
	flag.Parse()

	if *checkPath != "" {
		checkReviewedOutput(*corpusPath, *checkPath)
		return
	}
	if *validateOnly {
		validateCorpus(*corpusPath)
		return
	}
	if *prepareReview {
		prepareOfflineReview(*corpusPath, *outPath)
		return
	}
	if *checkConfig {
		inspectProviderConfig(*providerID, *model, *reasoning, *apiKeyEnv, *databaseConfig, *databaseURLEnv)
		return
	}
	if *corpusPath == "" || *outPath == "" || *providerID == "" || (!*databaseConfig && *apiKeyEnv == "") {
		log.Fatal("--corpus, --out, --provider and either --api-key-env or --database-config are required")
	}
	if *runs != 3 {
		log.Fatal("--runs must be exactly 3 for the B1 quality gate")
	}
	corpus := loadCorpus(*corpusPath)
	corpusHash, err := ai.DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		log.Fatal(err)
	}
	provider, err := ai.GetProvider(*providerID)
	if err != nil {
		log.Fatal(err)
	}
	descriptor := provider.Descriptor()
	modelExplicit := *model != ""
	reasoningExplicit := *reasoning != ""
	if *model == "" {
		*model = descriptor.DefaultModel
	}
	if *reasoning == "" {
		*reasoning = descriptor.DefaultReasoning
	}
	key := os.Getenv(*apiKeyEnv)
	if *databaseConfig {
		stored, err := loadProviderConfigFromDatabase(*databaseURLEnv, *providerID)
		if err != nil {
			log.Fatal(err)
		}
		key = stored.APIKey
		if !modelExplicit && stored.Model != "" {
			*model = stored.Model
		}
		if !reasoningExplicit && stored.ReasoningEffort != "" {
			*reasoning = stored.ReasoningEffort
		}
	}
	if key == "" {
		if *databaseConfig {
			log.Fatalf("database settings have no API key for provider %q", *providerID)
		}
		log.Fatalf("environment variable %q is empty", *apiKeyEnv)
	}

	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	output := ai.DailyInsightNarrativeEvaluationOutput{
		Version: "daily-insight-narrative-evaluation-v2", CorpusHash: corpusHash, GeneratedAt: time.Now().UTC(),
		Provider: *providerID, Model: *model, Reasoning: *reasoning,
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		RunsPerCase: *runs,
		Cases:       make([]ai.DailyInsightNarrativeEvaluationCase, 0, len(corpus.Cases)),
	}
	config := ai.ProviderConfig{APIKey: key, Model: *model, ReasoningEffort: *reasoning, MaxOutputTokens: ai.DailyInsightMaxTokens}
	for _, item := range corpus.Cases {
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			log.Fatal(err)
		}
		result := ai.DailyInsightNarrativeEvaluationCase{ID: item.ID, Locale: item.Locale, Tags: item.Tags, Fallbacks: ai.DailyInsightNarrativeFallbacks(snapshot, item.Locale)}
		if !health.HasEligibleDailyInsightNarrativeClaims(&snapshot, item.Locale) {
			result.Mode = "deterministic_fallback"
			output.Cases = append(output.Cases, result)
			continue
		}
		result.Mode = "narrative_candidate"
		result.Runs = make([]ai.DailyInsightNarrativeEvaluationRun, 0, *runs)
		for run := 0; run < *runs; run++ {
			entry := ai.DailyInsightNarrativeEvaluationRun{InvalidDomains: map[string]string{}}
			candidate := health.DailyInsightNarrative{Version: health.DailyInsightNarrativeVersion, Locale: item.Locale, Domains: make([]health.DailyInsightNarrativeDomain, 0, 3)}
			for _, slot := range []string{health.DailyInsightNarrativeOverallSlot, "sleep", "recovery", "energy"} {
				input, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, item.Locale, slot)
				if !known || len(input.Slot.Claims) == 0 {
					if slot != health.DailyInsightNarrativeOverallSlot {
						candidate.Domains = append(candidate.Domains, health.DailyInsightNarrativeDomain{Key: slot})
					}
					continue
				}
				generationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				generated, generationErr := ai.GenerateDailyInsightNarrativeSlot(generationCtx, provider, config, &snapshot, item.Locale, slot)
				cancel()
				entry.Attempts += generated.Attempts
				entry.InputTokens += int(generated.InputTokens)
				entry.OutputTokens += int(generated.OutputTokens)
				if generationErr != nil {
					entry.Error = fmt.Sprintf("%s: %v", slot, generationErr)
					break
				}
				if slot == health.DailyInsightNarrativeOverallSlot {
					candidate.Overall = generated.Section
				} else {
					candidate.Domains = append(candidate.Domains, health.DailyInsightNarrativeDomain{Key: slot, Section: generated.Section})
				}
			}
			if entry.Error == "" {
				entry.Narrative = &candidate
			}
			if len(entry.InvalidDomains) == 0 {
				entry.InvalidDomains = nil
			}
			entry.Review = ai.DailyInsightNarrativeRunReviewWorksheet(snapshot, item.Locale, entry.Narrative, entry.InvalidDomains, entry.Error != "")
			result.Runs = append(result.Runs, entry)
		}
		output.Cases = append(output.Cases, result)
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*outPath, append(encoded, '\n'), 0o600); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s for %d frozen cases; corpus_sha256=%s\n", *outPath, len(output.Cases), output.CorpusHash)
}

// inspectProviderConfig verifies only configuration reachability. Its output
// deliberately contains no key material, corpus data, or provider response.
func inspectProviderConfig(providerID, model, reasoning, apiKeyEnv string, databaseConfig bool, databaseURLEnv string) {
	if providerID == "" || (!databaseConfig && apiKeyEnv == "") {
		log.Fatal("--provider and either --api-key-env or --database-config are required with --check-config")
	}
	provider, err := ai.GetProvider(providerID)
	if err != nil {
		log.Fatal(err)
	}
	descriptor := provider.Descriptor()
	modelExplicit := model != ""
	reasoningExplicit := reasoning != ""
	if model == "" {
		model = descriptor.DefaultModel
	}
	if reasoning == "" {
		reasoning = descriptor.DefaultReasoning
	}
	key := os.Getenv(apiKeyEnv)
	if databaseConfig {
		stored, err := loadProviderConfigFromDatabase(databaseURLEnv, providerID)
		if err != nil {
			log.Fatal(err)
		}
		key = stored.APIKey
		if !modelExplicit && stored.Model != "" {
			model = stored.Model
		}
		if !reasoningExplicit && stored.ReasoningEffort != "" {
			reasoning = stored.ReasoningEffort
		}
	}
	if key == "" {
		log.Fatalf("provider %q is not configured", providerID)
	}
	fmt.Printf("provider=%s configured=true model=%s reasoning=%s\n", providerID, model, reasoning)
}

// loadProviderConfigFromDatabase reads the installation-wide Admin provider
// configuration from health_registry through the same registry connection as
// serving. It deliberately does not open a tenant data pool: B1 evaluation
// must receive its corpus from the caller and needs no health-data access.
// Callers must never log or serialize APIKey.
func loadProviderConfigFromDatabase(databaseURLEnv, providerID string) (storage.AIProviderSettings, error) {
	databaseURL := os.Getenv(databaseURLEnv)
	if databaseURL == "" {
		return storage.AIProviderSettings{}, fmt.Errorf("environment variable %q is empty", databaseURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	registryDSN, err := evaluatorRegistryDSN(databaseURL, os.LookupEnv)
	if err != nil {
		return storage.AIProviderSettings{}, err
	}
	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		return storage.AIProviderSettings{}, fmt.Errorf("connect for global provider configuration: %w", err)
	}
	defer reg.Close()
	return globalAIConfig(reg.GetAllGlobalSettings(ctx)).SettingsFor(providerID), nil
}

func evaluatorRegistryDSN(databaseURL string, lookup func(string) (string, bool)) (string, error) {
	isolation, err := tenants.ParseTenantIsolationConfig(lookup)
	if err != nil {
		return "", fmt.Errorf("parse tenant isolation configuration: %w", err)
	}
	if isolation.Enabled {
		return isolation.RegistryDSN, nil
	}
	return databaseURL, nil
}

// globalAIConfig converts the registry's installation-wide Admin values into
// the same defaults that a tenant DB receives at serving time. It deliberately
// accepts only the closed provider registry and never exposes key values.
func globalAIConfig(settings map[string]string) storage.AIConfig {
	config := storage.AIConfig{Provider: settings["ai_provider"], Providers: make(map[string]storage.AIProviderSettings)}
	for _, descriptor := range ai.ProviderDescriptors() {
		config.SetSettingsFor(descriptor.ID, storage.AIProviderSettings{
			APIKey:          settings[descriptor.ID+"_api_key"],
			Model:           settings[descriptor.ID+"_model"],
			ReasoningEffort: settings[descriptor.ID+"_reasoning_effort"],
		})
	}
	return config
}

func prepareOfflineReview(corpusPath, outPath string) {
	if corpusPath == "" || outPath == "" {
		log.Fatal("--corpus and --out are required with --prepare-review")
	}
	packet, err := ai.BuildDailyInsightNarrativeReviewPacket(loadCorpus(corpusPath))
	if err != nil {
		log.Fatal(err)
	}
	encoded, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0o600); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote offline review packet for %d frozen cases; corpus_sha256=%s\n", len(packet.Cases), packet.CorpusHash)
}

func checkReviewedOutput(corpusPath, checkPath string) {
	corpus := loadCorpus(corpusPath)
	corpusHash, err := ai.DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		log.Fatal(err)
	}
	rawReview, err := os.ReadFile(checkPath)
	if err != nil {
		log.Fatal(err)
	}
	var review ai.DailyInsightNarrativeEvaluationOutput
	if err := json.Unmarshal(rawReview, &review); err != nil {
		log.Fatalf("decode review: %v", err)
	}
	gate, err := ai.CheckDailyInsightNarrativeQualityGate(corpus, corpusHash, review)
	if err != nil {
		log.Fatal(err)
	}
	encoded, err := json.MarshalIndent(gate, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
	if !gate.Passed {
		os.Exit(2)
	}
}

func validateCorpus(corpusPath string) {
	corpus := loadCorpus(corpusPath)
	corpusHash, err := ai.DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("validated %d frozen cases; corpus_sha256=%s\n", len(corpus.Cases), corpusHash)
}

func loadCorpus(corpusPath string) ai.DailyInsightNarrativeCorpus {
	if corpusPath == "" {
		log.Fatal("--corpus is required")
	}
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		log.Fatal(err)
	}
	var corpus ai.DailyInsightNarrativeCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		log.Fatalf("decode corpus: %v", err)
	}
	if err := ai.ValidateDailyInsightNarrativeCorpus(corpus); err != nil {
		log.Fatalf("validate corpus: %v", err)
	}
	return corpus
}
