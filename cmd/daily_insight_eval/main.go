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
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
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

func main() {
	corpusPath := flag.String("corpus", "", "path to a frozen anonymized corpus JSON file")
	outPath := flag.String("out", "", "path for JSON review output")
	checkPath := flag.String("check", "", "validate a manually reviewed evaluation JSON against --corpus")
	validateOnly := flag.Bool("validate", false, "validate and print the checksum of --corpus without calling a provider")
	prepareReview := flag.Bool("prepare-review", false, "write offline claim/fallback review packet without calling a provider")
	checkConfig := flag.Bool("check-config", false, "verify provider configuration without reading a corpus or calling a provider")
	providerID := flag.String("provider", "", "explicit provider id; with --database-config must match the active Admin provider")
	model := flag.String("model", "", "explicit model id; with --database-config must match the active Admin model")
	reasoning := flag.String("reasoning", "", "explicit reasoning effort; with --database-config must match the active Admin setting")
	apiKeyEnv := flag.String("api-key-env", "", "environment variable containing the provider key")
	databaseConfig := flag.Bool("database-config", false, "load only the active B1 provider configuration from installation-wide Admin settings")
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
	if *corpusPath == "" || *outPath == "" || (!*databaseConfig && (*providerID == "" || *apiKeyEnv == "")) {
		log.Fatal("--corpus and --out are required; outside --database-config also provide --provider and --api-key-env")
	}
	if *runs != 3 {
		log.Fatal("--runs must be exactly 3 for the B1 quality gate")
	}
	corpus := loadCorpus(*corpusPath)
	corpusHash, err := ai.DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		log.Fatal(err)
	}
	var provider ai.Provider
	var config ai.ProviderConfig
	if *databaseConfig {
		storedConfig, err := loadAIConfigFromDatabase(*databaseURLEnv)
		if err != nil {
			log.Fatal(err)
		}
		provider, config, err = resolveActiveDatabaseProviderConfig(storedConfig, *providerID, *model, *reasoning)
		if err != nil {
			log.Fatal(err)
		}
		if config.APIKey == "" {
			log.Fatalf("active database provider %q is not configured", provider.Descriptor().ID)
		}
		*providerID = provider.Descriptor().ID
		*model, *reasoning = config.Model, config.ReasoningEffort
	} else {
		provider, err = ai.GetProvider(*providerID)
		if err != nil {
			log.Fatal(err)
		}
		descriptor := provider.Descriptor()
		if *model == "" {
			*model = descriptor.DefaultModel
		}
		if *reasoning == "" {
			*reasoning = descriptor.DefaultReasoning
		}
		key := os.Getenv(*apiKeyEnv)
		if key == "" {
			log.Fatalf("environment variable %q is empty", *apiKeyEnv)
		}
		config = ai.ProviderConfig{APIKey: key, Model: *model, ReasoningEffort: *reasoning, MaxOutputTokens: ai.DailyInsightMaxTokens}
	}
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	output := ai.DailyInsightNarrativeEvaluationOutput{
		Version: "daily-insight-narrative-evaluation-v3", CorpusHash: corpusHash, GeneratedAt: time.Now().UTC(),
		Provider: *providerID, Model: config.Model, Reasoning: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		RunsPerCase: *runs,
		Cases:       make([]ai.DailyInsightNarrativeEvaluationCase, 0, len(corpus.Cases)),
	}
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
			entry := ai.DailyInsightNarrativeEvaluationRun{InvalidDomains: map[string]string{}, ProviderErrors: map[string]string{}}
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
					// A slot is the runtime unit of generation. Keep successful
					// siblings in the review artifact and mark only this slot as a
					// provider failure; otherwise an unsafe successful section could
					// be hidden by a later unrelated request failure.
					var semanticErr *ai.DailyInsightNarrativeSemanticError
					if errors.As(generationErr, &semanticErr) {
						entry.InvalidDomains[slot] = semanticErr.Error()
					} else {
						entry.ProviderErrors[slot] = generationErr.Error()
					}
					if slot != health.DailyInsightNarrativeOverallSlot {
						candidate.Domains = append(candidate.Domains, health.DailyInsightNarrativeDomain{Key: slot})
					}
					continue
				}
				if slot == health.DailyInsightNarrativeOverallSlot {
					candidate.Overall = generated.Section
				} else {
					candidate.Domains = append(candidate.Domains, health.DailyInsightNarrativeDomain{Key: slot, Section: generated.Section})
				}
			}
			entry.Narrative = &candidate
			if len(entry.InvalidDomains) == 0 {
				entry.InvalidDomains = nil
			}
			if len(entry.ProviderErrors) == 0 {
				entry.ProviderErrors = nil
			}
			entry.Review = ai.DailyInsightNarrativeRunReviewWorksheet(snapshot, item.Locale, entry.Narrative, entry.InvalidDomains, entry.ProviderErrors)
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
	if !databaseConfig && (providerID == "" || apiKeyEnv == "") {
		log.Fatal("outside --database-config, --provider and --api-key-env are required with --check-config")
	}
	if databaseConfig {
		stored, err := loadAIConfigFromDatabase(databaseURLEnv)
		if err != nil {
			log.Fatal(err)
		}
		provider, active, err := resolveActiveDatabaseProviderConfig(stored, providerID, model, reasoning)
		if err != nil {
			log.Fatal(err)
		}
		if active.APIKey == "" {
			log.Fatalf("active database provider %q is not configured", provider.Descriptor().ID)
		}
		fmt.Printf("provider=%s configured=true model=%s reasoning=%s max_output_tokens=%d\n", provider.Descriptor().ID, active.Model, active.ReasoningEffort, active.MaxOutputTokens)
		return
	}
	provider, err := ai.GetProvider(providerID)
	if err != nil {
		log.Fatal(err)
	}
	descriptor := provider.Descriptor()
	if model == "" {
		model = descriptor.DefaultModel
	}
	if reasoning == "" {
		reasoning = descriptor.DefaultReasoning
	}
	if key := os.Getenv(apiKeyEnv); key == "" {
		log.Fatalf("environment variable %q is empty", apiKeyEnv)
	}
	fmt.Printf("provider=%s configured=true model=%s reasoning=%s\n", providerID, model, reasoning)
}

// resolveActiveDatabaseProviderConfig makes the evaluator follow exactly the
// same active B1 configuration that serving uses. A database-backed evaluation
// is evidence for that configuration only; silently selecting an alternate
// configured provider would make its corpus review inapplicable to serving.
func resolveActiveDatabaseProviderConfig(stored storage.AIConfig, requestedProvider, requestedModel, requestedReasoning string) (ai.Provider, ai.ProviderConfig, error) {
	provider, active, err := storage.ResolveTodayInsightsB1ProviderConfig(stored)
	if err != nil {
		return nil, ai.ProviderConfig{}, err
	}
	activeID := provider.Descriptor().ID
	if requestedProvider != "" && requestedProvider != activeID {
		return nil, ai.ProviderConfig{}, fmt.Errorf("--database-config refuses provider %q: active Admin provider is %q", requestedProvider, activeID)
	}
	if requestedModel != "" && requestedModel != active.Model {
		return nil, ai.ProviderConfig{}, fmt.Errorf("--database-config refuses model %q: active Admin model is %q", requestedModel, active.Model)
	}
	if requestedReasoning != "" && requestedReasoning != active.ReasoningEffort {
		return nil, ai.ProviderConfig{}, fmt.Errorf("--database-config refuses reasoning %q: active Admin reasoning is %q", requestedReasoning, active.ReasoningEffort)
	}
	return provider, active, nil
}

// loadProviderConfigFromDatabase reads the installation-wide Admin provider
// configuration from health_registry through the same registry connection as
// serving. It deliberately does not open a tenant data pool: B1 evaluation
// must receive its corpus from the caller and needs no health-data access.
// Callers must never log or serialize APIKey.
func loadAIConfigFromDatabase(databaseURLEnv string) (storage.AIConfig, error) {
	databaseURL := os.Getenv(databaseURLEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	registryDSN, err := evaluatorRegistryDSN(databaseURL, os.LookupEnv)
	if err != nil {
		return storage.AIConfig{}, err
	}
	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		return storage.AIConfig{}, fmt.Errorf("connect for global provider configuration: %w", err)
	}
	defer reg.Close()
	return globalAIConfig(reg.GetAllGlobalSettings(ctx)), nil
}

func evaluatorRegistryDSN(databaseURL string, lookup func(string) (string, bool)) (string, error) {
	isolation, err := tenants.ParseTenantIsolationConfig(lookup)
	if err != nil {
		return "", fmt.Errorf("parse tenant isolation configuration: %w", err)
	}
	if isolation.Enabled {
		return isolation.RegistryDSN, nil
	}
	if databaseURL == "" {
		if err := validateStandardPostgresEnvironment(lookup); err == nil {
			// pgx accepts an empty connection string and resolves the standard
			// PG* variables itself. This keeps the evaluator compatible with the
			// local read-only DB profile without constructing or logging a URL
			// containing credentials.
			return "", nil
		} else {
			return "", fmt.Errorf("database URL is empty while tenant isolation is disabled: %w", err)
		}
	}
	return databaseURL, nil
}

func validateStandardPostgresEnvironment(lookup func(string) (string, bool)) error {
	for _, key := range []string{"PGHOST", "PGDATABASE", "PGUSER"} {
		value, ok := lookup(key)
		if !ok || value == "" {
			return fmt.Errorf("%s is required for standard PG* configuration", key)
		}
	}
	host, _ := lookup("PGHOST")
	if postgresHostIsLocal(host) || postgresHostIsTailscale(host) {
		// A Tailscale address is reached through the encrypted tailnet
		// transport. Some private Postgres deployments intentionally do not
		// offer TLS inside that tunnel, so requiring a second TLS layer would
		// make the documented read-only profile unusable.
		return nil
	}
	sslMode, _ := lookup("PGSSLMODE")
	switch strings.ToLower(strings.TrimSpace(sslMode)) {
	case "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("PGSSLMODE must be require, verify-ca, or verify-full for non-local PGHOST")
	}
}

func postgresHostIsLocal(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.HasPrefix(host, "/") || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func postgresHostIsTailscale(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127
}

// globalAIConfig converts the registry's installation-wide Admin values into
// the same defaults that a tenant DB receives at serving time. It deliberately
// accepts only the closed provider registry and never exposes key values.
func globalAIConfig(settings map[string]string) storage.AIConfig {
	maxOutputTokens := parseMaxOutputTokens(settings["ai_max_output_tokens"])
	if maxOutputTokens == 0 {
		maxOutputTokens = parseMaxOutputTokens(settings["gemini_max_tokens"])
	}
	config := storage.AIConfig{Provider: settings["ai_provider"], Providers: make(map[string]storage.AIProviderSettings), MaxOutputTokens: maxOutputTokens}
	for _, descriptor := range ai.ProviderDescriptors() {
		config.SetSettingsFor(descriptor.ID, storage.AIProviderSettings{
			APIKey:          settings[descriptor.ID+"_api_key"],
			Model:           settings[descriptor.ID+"_model"],
			ReasoningEffort: settings[descriptor.ID+"_reasoning_effort"],
		})
	}
	return config
}

func parseMaxOutputTokens(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
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
