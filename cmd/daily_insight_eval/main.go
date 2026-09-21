// daily_insight_eval runs a frozen, privacy-minimized B1 corpus against one explicit
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
	"html/template"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

// evaluationRunConcurrency bounds offline evaluator traffic. Each run still
// generates its slots sequentially, so a run's narrative and review remain
// exactly as they would in the production generation path.
const evaluationRunConcurrency = 3

func main() {
	corpusPath := flag.String("corpus", "", "path to a frozen privacy-minimized corpus JSON file")
	outPath := flag.String("out", "", "path for JSON review output")
	checkPath := flag.String("check", "", "validate a manually reviewed evaluation JSON against --corpus")
	validateOnly := flag.Bool("validate", false, "validate and print the checksum of --corpus without calling a provider")
	prepareReview := flag.Bool("prepare-review", false, "write offline claim/fallback review packet without calling a provider")
	renderReview := flag.Bool("render-review", false, "write a local interactive HTML worksheet from --check without calling a provider")
	checkConfig := flag.Bool("check-config", false, "verify provider configuration without reading a corpus or calling a provider")
	providerID := flag.String("provider", "", "explicit provider id; with --database-config must match the active Admin provider")
	model := flag.String("model", "", "explicit model id; with --database-config must match the active Admin model")
	reasoning := flag.String("reasoning", "", "explicit reasoning effort; with --database-config must match the active Admin setting")
	apiKeyEnv := flag.String("api-key-env", "", "environment variable containing the provider key")
	databaseConfig := flag.Bool("database-config", false, "load only the active B1 provider configuration from installation-wide Admin settings")
	databaseURLEnv := flag.String("database-url-env", "DATABASE_URL", "environment variable containing the database URL when --database-config is set")
	runs := flag.Int("runs", 3, "independent generations per eligible case")
	flag.Parse()

	if *validateOnly {
		validateCorpus(*corpusPath)
		return
	}
	if *prepareReview {
		prepareOfflineReview(*corpusPath, *outPath)
		return
	}
	if *renderReview {
		renderOfflineReview(*corpusPath, *checkPath, *outPath)
		return
	}
	if *checkPath != "" {
		checkReviewedOutput(*corpusPath, *checkPath)
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
		Version: "daily-insight-narrative-evaluation-v6", CorpusHash: corpusHash, GeneratedAt: time.Now().UTC(),
		Provider: *providerID, Model: config.Model, Reasoning: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens,
		PromptRevision: identity.PromptRevision, SafetyPromptRevision: identity.SafetyPromptRevision, SafetyMaxOutputTokens: identity.SafetyMaxOutputTokens, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		RunsPerCase: *runs,
		Cases:       make([]ai.DailyInsightNarrativeEvaluationCase, 0, len(corpus.Cases)),
	}
	for _, item := range corpus.Cases {
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			log.Fatal(err)
		}
		frozenInput, known, err := ai.BuildDailyInsightNarrativeCorpusSlotInput(item, item.Locale, health.DailyInsightNarrativeOverallSlot)
		if err != nil || !known {
			if err == nil {
				err = fmt.Errorf("unknown frozen overall packet")
			}
			log.Fatal(err)
		}
		result := ai.DailyInsightNarrativeEvaluationCase{ID: item.ID, Locale: item.Locale, Tags: item.Tags, Fallbacks: ai.DailyInsightNarrativeFallbacks(snapshot, item.Locale)}
		if !hasEligibleFrozenOverallInput(frozenInput) {
			result.Mode = "deterministic_fallback"
			output.Cases = append(output.Cases, result)
			continue
		}
		result.Mode = "narrative_candidate"
		result.Runs = runIndependentEvaluationRuns(*runs, func(_ int) ai.DailyInsightNarrativeEvaluationRun {
			return evaluateNarrativeRun(snapshot, item.Locale, provider, config, frozenInput)
		})
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

func runIndependentEvaluationRuns(runs int, evaluate func(int) ai.DailyInsightNarrativeEvaluationRun) []ai.DailyInsightNarrativeEvaluationRun {
	results := make([]ai.DailyInsightNarrativeEvaluationRun, runs)
	workers := min(runs, evaluationRunConcurrency)
	jobs := make(chan int)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for run := range jobs {
				results[run] = evaluate(run)
			}
		}()
	}
	for run := 0; run < runs; run++ {
		jobs <- run
	}
	close(jobs)
	group.Wait()
	return results
}

func evaluateNarrativeRun(snapshot health.DailyInsightSnapshot, locale string, provider ai.Provider, config ai.ProviderConfig, frozenInput health.DailyInsightNarrativeSlotInput) ai.DailyInsightNarrativeEvaluationRun {
	entry := ai.DailyInsightNarrativeEvaluationRun{InvalidDomains: map[string]string{}, ProviderErrors: map[string]string{}}
	candidate := health.DailyInsightNarrative{Version: health.DailyInsightNarrativeVersion, Locale: locale}
	if hasEligibleFrozenOverallInput(frozenInput) {
		generationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		generated, generationErr := ai.GenerateDailyInsightNarrativeSlotFromInput(generationCtx, provider, config, &snapshot, locale, health.DailyInsightNarrativeOverallSlot, frozenInput)
		cancel()
		entry.Attempts += generated.Attempts
		entry.InputTokens += int(generated.InputTokens)
		entry.OutputTokens += int(generated.OutputTokens)
		entry.SafetyEvidence = generated.SafetyEvidence
		if generated.SafetyEvidence != nil {
			entry.Attempts += generated.SafetyEvidence.ProviderAttempts
			entry.InputTokens += int(generated.SafetyEvidence.ProviderInputTokens)
			entry.OutputTokens += int(generated.SafetyEvidence.ProviderOutputTokens)
		}
		if generationErr != nil {
			var semanticErr *ai.DailyInsightNarrativeSemanticError
			if errors.As(generationErr, &semanticErr) {
				entry.InvalidDomains[health.DailyInsightNarrativeOverallSlot] = semanticErr.Error()
			} else {
				entry.ProviderErrors[health.DailyInsightNarrativeOverallSlot] = generationErr.Error()
			}
		} else {
			candidate.Overall = generated.Section
		}
	}
	entry.Narrative = &candidate
	if len(entry.InvalidDomains) == 0 {
		entry.InvalidDomains = nil
	}
	if len(entry.ProviderErrors) == 0 {
		entry.ProviderErrors = nil
	}
	entry.Review = ai.DailyInsightNarrativeRunReviewWorksheet(snapshot, locale, entry.Narrative, entry.InvalidDomains, entry.ProviderErrors)
	return entry
}

func hasEligibleFrozenOverallInput(input health.DailyInsightNarrativeSlotInput) bool {
	if input.Slot.Key != health.DailyInsightNarrativeOverallSlot || len(input.Slot.Facts) == 0 {
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
	settings, err := reg.LoadAllGlobalSettings(ctx)
	if err != nil {
		return storage.AIConfig{}, fmt.Errorf("read global provider configuration: %w", err)
	}
	return globalAIConfig(settings), nil
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
	review := loadEvaluationOutput(checkPath)
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

func loadEvaluationOutput(path string) ai.DailyInsightNarrativeEvaluationOutput {
	if path == "" {
		log.Fatal("--check is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var review ai.DailyInsightNarrativeEvaluationOutput
	if err := json.Unmarshal(raw, &review); err != nil {
		log.Fatalf("decode review: %v", err)
	}
	ai.NormalizeDailyInsightNarrativeEvaluationOutput(&review)
	return review
}

func renderOfflineReview(corpusPath, checkPath, outPath string) {
	if outPath == "" {
		log.Fatal("--out is required with --render-review")
	}
	corpus := loadCorpus(corpusPath)
	corpusHash, err := ai.DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		log.Fatal(err)
	}
	review := loadEvaluationOutput(checkPath)
	if err := validateOfflineReviewArtifact(corpus, corpusHash, review); err != nil {
		log.Fatalf("validate review artifact: %v", err)
	}
	encoded, err := json.Marshal(review)
	if err != nil {
		log.Fatalf("encode review for HTML: %v", err)
	}
	anchors, err := offlineReviewServerAnchors(corpus, review)
	if err != nil {
		log.Fatalf("prepare server anchors for offline review: %v", err)
	}
	encodedAnchors, err := json.Marshal(anchors)
	if err != nil {
		log.Fatalf("encode server anchors for HTML: %v", err)
	}
	packet, err := ai.BuildDailyInsightNarrativeReviewPacket(corpus)
	if err != nil {
		log.Fatalf("build reviewer evidence packet: %v", err)
	}
	if packet.CorpusHash != corpusHash {
		log.Fatal("reviewer evidence packet does not match the frozen corpus")
	}
	encodedPacket, err := json.Marshal(packet)
	if err != nil {
		log.Fatalf("encode reviewer evidence packet for HTML: %v", err)
	}
	data := offlineReviewHTMLData{CorpusHash: corpusHash, EvaluationJSON: string(encoded), AnchorJSON: string(encodedAnchors), ReviewPacketJSON: string(encodedPacket)}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		log.Fatal(err)
	}
	file, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	if err := offlineReviewHTMLTemplate.Execute(file, data); err != nil {
		log.Fatalf("render offline review: %v", err)
	}
	fmt.Printf("wrote offline HTML worksheet for %d frozen cases; corpus_sha256=%s\n", len(review.Cases), corpusHash)
}

// validateOfflineReviewArtifact applies the same frozen-corpus and semantic
// structure checks as the B1 approval gate before rendering a worksheet. An
// incomplete human rubric is deliberately allowed here: it is what the
// worksheet is for. Structural substitution, changed fallbacks, stale runtime
// identity, invalid stored prose, malformed worksheet rows, or provider output
// in a control case are not.
func validateOfflineReviewArtifact(corpus ai.DailyInsightNarrativeCorpus, corpusHash string, review ai.DailyInsightNarrativeEvaluationOutput) error {
	gate, err := ai.CheckDailyInsightNarrativeQualityGate(corpus, corpusHash, review)
	if err != nil {
		return err
	}
	for _, violation := range gate.SafetyViolations {
		if strings.Contains(violation, "stored narrative no longer passes semantic validation") {
			return fmt.Errorf("review artifact contains invalid stored narrative: %s", violation)
		}
	}

	byID := make(map[string]ai.DailyInsightNarrativeEvaluationCase, len(review.Cases))
	for _, item := range review.Cases {
		byID[item.ID] = item
	}
	for _, frozen := range corpus.Cases {
		snapshot, err := frozen.SnapshotForEvaluation()
		if err != nil {
			return fmt.Errorf("restore corpus case %q: %w", frozen.ID, err)
		}
		if !health.HasEligibleDailyInsightNarrativeClaims(&snapshot, frozen.Locale) {
			continue
		}
		evaluated := byID[frozen.ID]
		for runIndex, run := range evaluated.Runs {
			if err := validateOfflineNarrativeRun(corpus.Version, frozen, snapshot, frozen.Locale, run); err != nil {
				return fmt.Errorf("case %q run %d: %w", frozen.ID, runIndex+1, err)
			}
			want := ai.DailyInsightNarrativeRunReviewWorksheet(snapshot, frozen.Locale, run.Narrative, run.InvalidDomains, run.ProviderErrors)
			if err := validateOfflineReviewRows(want, run.Review); err != nil {
				return fmt.Errorf("case %q run %d: %w", frozen.ID, runIndex+1, err)
			}
		}
	}
	return nil
}

// validateOfflineNarrativeRun makes a rejected or failed slot genuinely
// absent from the review artifact and revalidates every surviving independent
// section against its exact closed packet. CheckDailyInsightNarrativeQualityGate
// deliberately preserves rejected siblings for product reporting; this helper
// prevents an altered artifact from smuggling their raw model prose into the
// human worksheet.
func validateOfflineNarrativeRun(corpusVersion string, frozen ai.DailyInsightNarrativeCorpusCase, snapshot health.DailyInsightSnapshot, locale string, run ai.DailyInsightNarrativeEvaluationRun) error {
	sections := make(map[string]*health.DailyInsightNarrativeSection)
	if run.Narrative != nil {
		if run.Narrative.Version != health.DailyInsightNarrativeVersion || run.Narrative.Locale != locale {
			return fmt.Errorf("stored narrative has unexpected version or locale")
		}
		sections[health.DailyInsightNarrativeOverallSlot] = run.Narrative.Overall
		for _, domain := range run.Narrative.Domains {
			if _, duplicate := sections[domain.Key]; duplicate {
				return fmt.Errorf("duplicate stored narrative slot %q", domain.Key)
			}
			if _, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, domain.Key); !known {
				return fmt.Errorf("unknown stored narrative slot %q", domain.Key)
			}
			sections[domain.Key] = domain.Section
		}
	}
	for slot := range run.InvalidDomains {
		if _, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, slot); !known {
			return fmt.Errorf("unknown validator-rejected slot %q", slot)
		}
		if run.ProviderErrors[slot] != "" {
			return fmt.Errorf("slot %q is both validator-rejected and provider-error", slot)
		}
		if sections[slot] != nil {
			return fmt.Errorf("validator-rejected slot %q retains model text", slot)
		}
	}
	for slot := range run.ProviderErrors {
		if _, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, locale, slot); !known {
			return fmt.Errorf("unknown provider-error slot %q", slot)
		}
		if sections[slot] != nil {
			return fmt.Errorf("provider-error slot %q retains model text", slot)
		}
	}
	for slot, section := range sections {
		if section == nil {
			continue
		}
		if run.InvalidDomains[slot] != "" || run.ProviderErrors[slot] != "" {
			return fmt.Errorf("failed slot %q retains model text", slot)
		}
		if corpusVersion == ai.DailyInsightNarrativeCorpusVersionV2 && slot == health.DailyInsightNarrativeOverallSlot {
			if len(run.Narrative.Domains) != 0 {
				return fmt.Errorf("stored overall narrative contains domain text")
			}
			frozenInput, known, inputErr := ai.BuildDailyInsightNarrativeCorpusSlotInput(frozen, locale, slot)
			if inputErr != nil || !known {
				return fmt.Errorf("frozen overall packet is unavailable")
			}
			candidate := health.DailyInsightNarrativeSlot{
				Version: run.Narrative.Version,
				Locale:  run.Narrative.Locale,
				Slot:    health.DailyInsightNarrativeDomain{Key: slot, Section: section},
			}
			if _, err := health.ValidateDailyInsightNarrativeSlotResponseWithInput(&snapshot, locale, slot, frozenInput.Slot, candidate); err != nil {
				return fmt.Errorf("stored narrative slot %q no longer passes semantic validation: %w", slot, err)
			}
			continue
		}
		candidate := health.DailyInsightNarrative{Version: run.Narrative.Version, Locale: run.Narrative.Locale}
		if slot == health.DailyInsightNarrativeOverallSlot {
			candidate.Overall = section
		} else {
			candidate.Domains = []health.DailyInsightNarrativeDomain{{Key: slot, Section: section}}
		}
		if _, err := health.ValidateDailyInsightNarrativeSlot(&snapshot, locale, slot, candidate); err != nil {
			return fmt.Errorf("stored narrative slot %q no longer passes semantic validation: %w", slot, err)
		}
	}
	return nil
}

// validateOfflineReviewRows requires the evaluator's immutable structural
// worksheet rows while allowing the reviewer-owned rubric fields to remain
// blank. Without this distinction a malformed artifact can render no valid
// slots and still present its download as a completed review.
func validateOfflineReviewRows(want, got ai.DailyInsightNarrativeRunReview) error {
	if len(got.Domains) != len(want.Domains) {
		return fmt.Errorf("worksheet has %d rows; want %d", len(got.Domains), len(want.Domains))
	}
	wanted := make(map[string]string, len(want.Domains))
	for _, row := range want.Domains {
		wanted[row.Key] = row.OutputStatus
	}
	seen := make(map[string]struct{}, len(got.Domains))
	for _, row := range got.Domains {
		if _, duplicate := seen[row.Key]; duplicate {
			return fmt.Errorf("duplicate worksheet row %q", row.Key)
		}
		seen[row.Key] = struct{}{}
		if status, found := wanted[row.Key]; !found || status != row.OutputStatus {
			return fmt.Errorf("worksheet row %q has status %q; want %q", row.Key, row.OutputStatus, status)
		}
	}
	return nil
}

type offlineReviewHTMLData struct {
	CorpusHash       string
	EvaluationJSON   string
	AnchorJSON       string
	ReviewPacketJSON string
}

// offlineReviewSlotEvidence keeps source facts distinct from the server-owned
// story. The frozen corpus can deliberately omit all displayable facts while
// retaining the claim/meaning packet for privacy review. Rendering a story as
// a fact would mislead the reviewer, while rejecting that valid slot would
// make the review gate unusable.
type offlineReviewSlotEvidence struct {
	Facts string `json:"facts"`
	Story string `json:"story"`
}

// offlineReviewServerAnchors rebuilds the server-selected rich material for
// each slot. The v13 story contract renders one complete model paragraph
// rather than prepending an anchor, but the reviewer still needs the exact
// factual material and allowed relationship against which to judge fidelity
// and duplication.
func offlineReviewServerAnchors(corpus ai.DailyInsightNarrativeCorpus, review ai.DailyInsightNarrativeEvaluationOutput) (map[string][]map[string]offlineReviewSlotEvidence, error) {
	corpusCases := make(map[string]ai.DailyInsightNarrativeCorpusCase, len(corpus.Cases))
	for _, item := range corpus.Cases {
		corpusCases[item.ID] = item
	}
	anchors := make(map[string][]map[string]offlineReviewSlotEvidence, len(review.Cases))
	for _, evaluated := range review.Cases {
		item, found := corpusCases[evaluated.ID]
		if !found {
			return nil, fmt.Errorf("evaluation references unknown corpus case %q", evaluated.ID)
		}
		snapshot, err := item.SnapshotForEvaluation()
		if err != nil {
			return nil, fmt.Errorf("restore corpus case %q: %w", item.ID, err)
		}
		runs := make([]map[string]offlineReviewSlotEvidence, len(evaluated.Runs))
		for runIndex, run := range evaluated.Runs {
			anchorsForRun := make(map[string]offlineReviewSlotEvidence)
			if run.Narrative == nil {
				runs[runIndex] = anchorsForRun
				continue
			}
			sections := map[string]*health.DailyInsightNarrativeSection{health.DailyInsightNarrativeOverallSlot: run.Narrative.Overall}
			for _, domain := range run.Narrative.Domains {
				sections[domain.Key] = domain.Section
			}
			for slot, section := range sections {
				if section == nil {
					continue
				}
				// The corpus locale is authoritative. The evaluation artifact is
				// review input and must not be able to select a different localized
				// server fact just by changing its copied case metadata.
				input, known := health.BuildDailyInsightNarrativeSlotInput(&snapshot, item.Locale, slot)
				if !known {
					return nil, fmt.Errorf("case %q run %d has unknown slot %q", evaluated.ID, runIndex+1, slot)
				}
				factText := make([]string, 0, len(input.Slot.Facts))
				for _, fact := range input.Slot.Facts {
					if strings.TrimSpace(fact.Statement) != "" {
						factText = append(factText, fact.Statement)
					}
				}
				story := ""
				if input.Slot.Story != nil {
					story = strings.TrimSpace(input.Slot.Story.Statement)
				}
				// A privacy-minimized evaluation case may retain its claim and
				// meaning only in the review packet. In that case there is no
				// separate fact/story panel to render here; reviewRules() still
				// renders the exact allowed proposition and cited meaning.
				if len(factText) == 0 && story == "" {
					continue
				}
				anchorsForRun[slot] = offlineReviewSlotEvidence{Facts: strings.Join(factText, " "), Story: story}
			}
			runs[runIndex] = anchorsForRun
		}
		anchors[evaluated.ID] = runs
	}
	return anchors, nil
}

var offlineReviewHTMLTemplate = template.Must(template.New("daily-insight-review").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Daily Insight B1 review</title><style>
body{font:16px/1.45 system-ui,sans-serif;margin:0;background:#f5f7fb;color:#172033}main{max-width:1000px;margin:auto;padding:28px 18px 60px}.meta,.case,.run,.slot{background:#fff;border:1px solid #dce2ec;border-radius:12px;padding:16px;margin:14px 0}.meta{display:flex;gap:20px;flex-wrap:wrap}.case h2,.run h3{margin:0 0 8px}.fallback,.anchor,.rule{background:#f7f9fc;padding:10px;border-radius:8px}.anchor{margin-top:10px;border-left:3px solid #667085}.rule{margin-top:8px;border-left:3px solid #aeb9ca;font-size:14px}.slot{border-left:4px solid #1768e5}.slot.invalid{border-left-color:#b36a00}.copy{white-space:pre-wrap}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(165px,1fr));gap:10px;margin-top:12px}label{display:grid;gap:4px;font-size:13px;font-weight:600}select,textarea,button{font:inherit;padding:7px;border:1px solid #aeb9ca;border-radius:6px}textarea{min-height:60px;width:100%}.muted{color:#596579}button{background:#1768e5;color:#fff;border:0;font-weight:700;cursor:pointer}button:disabled{cursor:not-allowed;opacity:.55}.sticky{position:sticky;bottom:12px;padding:12px;background:#172033;color:#fff;border-radius:10px;display:flex;justify-content:space-between;align-items:center;gap:12px}.actions{display:flex;gap:8px;flex-wrap:wrap}.sticky button{background:#63d6a3;color:#102218}.sticky button.secondary{background:#dce2ec;color:#172033}</style></head>
<body><main><h1>Daily Insight B1 — human review</h1><p>Review only the privacy-minimized frozen artifact. Compare the server facts, model narrative, deterministic fallback, allowed claim/meaning and the already-visible screen baseline. A valid slot is “better” only when it is faithful, safe, natural, non-duplicative, and adds meaning <strong>2</strong>. Do not alter output status, narratives, fallbacks, or metadata.</p><div class="meta" id="meta"></div><div id="cases"></div><div class="sticky"><span id="progress">0 valid slots scored</span><div class="actions"><button type="button" class="secondary" id="download-draft">Download draft</button><button type="button" id="download-complete" disabled>Download completed review</button></div></div></main>
<script>
const artifact=JSON.parse({{.EvaluationJSON}});
const serverAnchors=JSON.parse({{.AnchorJSON}});
const reviewPacket=JSON.parse({{.ReviewPacketJSON}});
const reviewCases=Object.fromEntries(reviewPacket.cases.map(item=>[item.id,item]));
const corpusHash={{.CorpusHash}};
const optionSets={fidelity:['','pass','fail'],claim_fidelity:['','pass','fail'],qualifier_fidelity:['','pass','fail'],safety:['','safe','violation'],added_meaning:['','0','1','2'],screen_duplication:['','none','domain','hero','both'],language:['','pass','fail'],naturalness:['','pass','fail']};
function node(tag,text,cls){const x=document.createElement(tag);if(text!==undefined)x.textContent=text;if(cls)x.className=cls;return x}
function field(domain,key,label){const wrap=node('label');wrap.append(node('span',label));const select=node('select');for(const value of optionSets[key]){const o=node('option',value||'—');o.value=value;if(String(domain[key]??'')===value)o.selected=true;select.append(o)}select.addEventListener('change',()=>{domain[key]=key==='added_meaning'?(select.value===''?null:Number(select.value)):select.value;updateProgress()});wrap.append(select);return wrap}
function reason(domain){const wrap=node('label');wrap.append(node('span','Reason'));const area=node('textarea');area.value=domain.review_reason||'';area.addEventListener('input',()=>{domain.review_reason=area.value;updateProgress()});wrap.append(area);return wrap}
function fallbackText(item,key){const f=(item.fallbacks||[]).find(x=>x.key===key);if(!f)return'No slot fallback';const copy=[f.context,f.observation,f.meaning].filter(Boolean).join(' — ');return'Fallback ('+f.summary+'): '+(copy||'No server copy available')}
function reviewRules(item,key,section){const packet=reviewCases[item.id];if(!packet||!section)return[];const sentence=(section.sentences||[])[0]||{};const claims=(packet.claims||[]).filter(claim=>(sentence.claim_ids||[]).includes(claim.id));const qualifiers=Object.fromEntries((packet.qualifier_definitions||[]).map(q=>[q.id,q.constraint]));const selectedMeaningIDs=new Set(sentence.meaning_ids||[]);const rules=[];for(const claim of claims){rules.push('Allowed claim: '+claim.id+' — '+claim.proposition);for(const qualifier of claim.required_qualifier_ids||[]){rules.push('Qualifier '+qualifier+': '+(qualifiers[qualifier]||'Preserve without strengthening the claim.'))}const selected=(claim.meaning_links||[]).filter(meaning=>selectedMeaningIDs.has(meaning.id)).map(meaning=>meaning.id+' — '+meaning.statement);if(selected.length)rules.push('Model-cited interpretation: '+selected.join(' | '));const alternatives=(claim.meaning_links||[]).filter(meaning=>!selectedMeaningIDs.has(meaning.id)).map(meaning=>meaning.id+' — '+meaning.statement);if(alternatives.length)rules.push('Other permitted interpretations, not cited: '+alternatives.join(' | '))}const visibleFacts=(packet.screen_baseline?.rendered_copy||[]).filter(copy=>copy.scope===key||(key==='overall'&&copy.scope==='overall')).map(copy=>copy.scope+': '+copy.text);if(visibleFacts.length)rules.push('Exact visible server copy: '+visibleFacts.join(' | '));const visible=(packet.screen_baseline?.displayed_meanings||[]).filter(meaning=>meaning.scope===key||(key==='overall'&&meaning.scope==='primary')).map(meaning=>meaning.scope+': '+meaning.constraint);if(visible.length)rules.push('Already visible on screen: '+visible.join(' | '));return rules}
function render(){const meta=document.querySelector('#meta');meta.append(node('div','Corpus: '+corpusHash));meta.append(node('div','Provider: '+artifact.provider+' / '+artifact.model+' / '+artifact.reasoning));meta.append(node('div','Runs per case: '+artifact.runs_per_case));const root=document.querySelector('#cases');for(const item of artifact.cases){const card=node('section',undefined,'case');card.append(node('h2',item.id+' · '+item.locale+' · '+item.mode));card.append(node('div',(item.tags||[]).join(' · '),'muted'));if(item.mode==='deterministic_fallback'){card.append(node('p','Safety control: no provider text expected. Review the exact server fallback below.','muted'));for(const fallback of item.fallbacks||[]){const slot=node('section',undefined,'slot invalid');slot.append(node('h3',fallback.key+' · deterministic fallback'));slot.append(node('div',fallbackText(item,fallback.key),'fallback'));for(const copy of (reviewCases[item.id]?.screen_baseline?.rendered_copy||[]).filter(copy=>copy.scope===fallback.key)){slot.append(node('div','Exact server copy: '+copy.text,'anchor'))}card.append(slot)}root.append(card);continue}for(const [index,run] of (item.runs||[]).entries()){const runCard=node('article',undefined,'run');runCard.append(node('h3','Run '+(index+1)));if(run.invalid_domains){runCard.append(node('p','Validator fallback: '+JSON.stringify(run.invalid_domains),'muted'))}if(run.provider_errors){runCard.append(node('p','Provider error: '+JSON.stringify(run.provider_errors),'muted'))}const sections={overall:run.narrative?.overall};for(const d of run.narrative?.domains||[])sections[d.key]=d.section;for(const review of run.review?.domains||[]){const slot=node('section',undefined,'slot '+(review.output_status==='valid'?'':'invalid'));slot.append(node('h3',review.key+' · '+review.output_status));slot.append(node('div',fallbackText(item,review.key),'fallback'));const evidence=serverAnchors[item.id]?.[index]?.[review.key];if(evidence?.facts)slot.append(node('div','Server facts: '+evidence.facts,'anchor'));if(evidence?.story)slot.append(node('div','Server-approved story: '+evidence.story,'rule'));const section=sections[review.key];const text=section?.text|| (section?.sentences||[]).map(s=>s.text).join(' ');slot.append(node('p',text?'Model narrative: '+text:'No provider narrative.','copy'));for(const rule of reviewRules(item,review.key,section))slot.append(node('div',rule,'rule'));if(review.output_status==='valid'){const grid=node('div',undefined,'grid');grid.append(field(review,'fidelity','Fidelity'));grid.append(field(review,'claim_fidelity','Claim fidelity'));grid.append(field(review,'qualifier_fidelity','Qualifier fidelity'));grid.append(field(review,'safety','Safety'));grid.append(field(review,'added_meaning','Added meaning'));grid.append(field(review,'screen_duplication','Screen duplication'));grid.append(field(review,'language','Language'));grid.append(field(review,'naturalness','Naturalness'));slot.append(grid);slot.append(reason(review))}runCard.append(slot)}card.append(runCard)}root.append(card)}updateProgress()}
function reviewRows(){return artifact.cases.flatMap(c=>c.runs||[]).flatMap(r=>r.review?.domains||[]).filter(d=>d.output_status==='valid')}
function isReviewed(d){return Boolean((d.fidelity||d.claim_fidelity)&&d.claim_fidelity&&d.qualifier_fidelity&&d.safety&&d.added_meaning!==null&&d.added_meaning!==undefined&&d.screen_duplication&&d.language&&d.naturalness&&d.review_reason.trim())}
function updateProgress(){const all=reviewRows();const done=all.filter(isReviewed).length;document.querySelector('#progress').textContent=done+'/'+all.length+' valid slots scored';document.querySelector('#download-complete').disabled=all.length===0||done!==all.length}
function download(name){const blob=new Blob([JSON.stringify(artifact,null,2)+'\n'],{type:'application/json'});const url=URL.createObjectURL(blob);const a=node('a');a.href=url;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(url),0)}
document.querySelector('#download-draft').addEventListener('click',()=>download('daily-insight-review-draft.json'));document.querySelector('#download-complete').addEventListener('click',()=>download('daily-insight-reviewed.json'));render();
</script></body></html>`))

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
