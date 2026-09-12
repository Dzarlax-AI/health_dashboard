// daily_insight_eval runs a frozen, anonymized B1 corpus against one explicit
// provider/model configuration. It is intentionally offline from production:
// it never reads DATABASE_URL, never changes tenant flags, and writes results
// only to the caller-selected path for a human product review.
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
)

func main() {
	corpusPath := flag.String("corpus", "", "path to a frozen anonymized corpus JSON file")
	outPath := flag.String("out", "", "path for JSON review output")
	checkPath := flag.String("check", "", "validate a manually reviewed evaluation JSON against --corpus")
	validateOnly := flag.Bool("validate", false, "validate and print the checksum of --corpus without calling a provider")
	prepareReview := flag.Bool("prepare-review", false, "write offline claim/fallback review packet without calling a provider")
	providerID := flag.String("provider", "", "explicit provider id")
	model := flag.String("model", "", "explicit model id; provider default if empty")
	reasoning := flag.String("reasoning", "", "explicit reasoning effort; provider default if empty")
	apiKeyEnv := flag.String("api-key-env", "", "environment variable containing the provider key")
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
	if *corpusPath == "" || *outPath == "" || *providerID == "" || *apiKeyEnv == "" {
		log.Fatal("--corpus, --out, --provider and --api-key-env are required")
	}
	if *runs != 3 {
		log.Fatal("--runs must be exactly 3 for the B1 quality gate")
	}
	key := os.Getenv(*apiKeyEnv)
	if key == "" {
		log.Fatalf("environment variable %q is empty", *apiKeyEnv)
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
	if *model == "" {
		*model = descriptor.DefaultModel
	}
	if *reasoning == "" {
		*reasoning = descriptor.DefaultReasoning
	}

	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	output := ai.DailyInsightNarrativeEvaluationOutput{
		Version: "daily-insight-narrative-evaluation-v1", CorpusHash: corpusHash, GeneratedAt: time.Now().UTC(),
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
			generationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			generated, generationErr := ai.GenerateDailyInsightNarrative(generationCtx, provider, config, &snapshot, item.Locale)
			cancel()
			entry := ai.DailyInsightNarrativeEvaluationRun{InvalidDomains: generated.InvalidDomains, Attempts: generated.Attempts, InputTokens: int(generated.InputTokens), OutputTokens: int(generated.OutputTokens)}
			if generationErr != nil {
				entry.Error = generationErr.Error()
			} else {
				candidate := generated.Narrative
				entry.Narrative = &candidate
			}
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
