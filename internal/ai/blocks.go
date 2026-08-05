package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"health-receiver/internal/health"
)

const (
	BlockSynthesis      = "SYNTHESIS"
	BlockSleep          = "SLEEP"
	BlockYesterday      = "YESTERDAY"
	BlockRecovery       = "RECOVERY"
	BlockRecommendation = "RECOMMENDATION"
)

var GeneratedBlockOrder = []string{
	BlockSynthesis,
	BlockSleep,
	BlockYesterday,
	BlockRecovery,
	BlockRecommendation,
}

// ─── input hashing ────────────────────────────────────────────────────────

// hashInputs marshals the per-block subset of metrics into stable JSON and
// returns a sha256 hex digest. Stable across runs because Go's encoding/json
// emits map keys in sorted order, and we package the subset as a struct.
func hashInputs(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashInsightBundle hashes the exact provider-facing evidence packet. The
// generation fingerprint is layered on by the storage orchestrator.
func HashInsightBundle(evidence health.MorningInsightEvidence) string {
	return hashInputs(evidence)
}

// HashSynthesis is retained as a compatibility alias for callers and tests
// written against AI insight v2.
func HashSynthesis(evidence health.MorningInsightEvidence) string {
	return HashInsightBundle(evidence)
}

type insightBundleEnvelope struct {
	Overview       string `json:"overview"`
	Sleep          string `json:"sleep"`
	Activity       string `json:"activity"`
	Recovery       string `json:"recovery"`
	Recommendation string `json:"recommendation"`
}

var insightBundleResponseSchema = &ResponseSchema{
	Name: "morning_insight_bundle",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"overview": map[string]any{
				"type":        "string",
				"description": "One or two concise sentences explaining the server-selected overall verdict.",
			},
			"sleep": map[string]any{
				"type":        "string",
				"description": "One or two concise sentences explaining the supplied sleep section facts.",
			},
			"activity": map[string]any{
				"type":        "string",
				"description": "One or two concise sentences explaining the supplied activity and cardio facts.",
			},
			"recovery": map[string]any{
				"type":        "string",
				"description": "One or two concise sentences explaining the supplied recovery facts.",
			},
			"recommendation": map[string]any{
				"type":        "string",
				"description": "One or two concise sentences reinforcing the exact server-selected action without changing it.",
			},
		},
		"required":             []string{"overview", "sleep", "activity", "recovery", "recommendation"},
		"additionalProperties": false,
	},
}

var htmlTagPattern = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

type InsightBundleResult struct {
	GenerationResult
	Blocks        map[string]string
	InvalidBlocks map[string]string
}

// GenerateInsightBundle makes one provider call and validates each narrative
// independently. A malformed JSON envelope fails the whole call; an unsafe
// individual field is omitted so valid sibling blocks may still be cached.
func GenerateInsightBundle(ctx context.Context, provider Provider, cfg ProviderConfig, evidenceJSON []byte, lang string) (InsightBundleResult, error) {
	if cfg.MaxOutputTokens <= 0 || cfg.MaxOutputTokens > SynthesisMaxTokens {
		cfg.MaxOutputTokens = SynthesisMaxTokens
	}
	generated, err := provider.Generate(ctx, cfg, GenerationRequest{
		Prompt:         systemPrompt,
		UserPayload:    evidenceJSON,
		Language:       lang,
		ResponseSchema: insightBundleResponseSchema,
	})
	if err != nil {
		return InsightBundleResult{GenerationResult: generated}, err
	}
	var envelope insightBundleEnvelope
	if err := json.Unmarshal([]byte(generated.Text), &envelope); err != nil {
		return InsightBundleResult{GenerationResult: generated}, fmt.Errorf("decode insight bundle: %w", err)
	}
	candidates := map[string]string{
		BlockSynthesis:      envelope.Overview,
		BlockSleep:          envelope.Sleep,
		BlockYesterday:      envelope.Activity,
		BlockRecovery:       envelope.Recovery,
		BlockRecommendation: envelope.Recommendation,
	}
	result := InsightBundleResult{
		GenerationResult: generated,
		Blocks:           make(map[string]string, len(candidates)),
		InvalidBlocks:    make(map[string]string),
	}
	for _, block := range GeneratedBlockOrder {
		value, validateErr := validateInsightText(candidates[block])
		if validateErr != nil {
			result.InvalidBlocks[block] = validateErr.Error()
			continue
		}
		result.Blocks[block] = value
	}
	if len(result.Blocks) == 0 {
		return result, fmt.Errorf("all insight bundle blocks failed validation")
	}
	return result, nil
}

func validateSynthesisExplanation(value string) (string, error) {
	return validateInsightText(value)
}

func validateInsightText(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("insight text is empty")
	}
	if words := len(strings.Fields(value)); words > 60 {
		return "", fmt.Errorf("insight text is too long: %d words", words)
	}
	if htmlTagPattern.MatchString(value) {
		return "", fmt.Errorf("insight text contains forbidden content")
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{
		"```", "**",
		"diagnos", "диагноз", "klinički znač", "клинически знач",
	} {
		if strings.Contains(lower, forbidden) {
			return "", fmt.Errorf("insight text contains forbidden content")
		}
	}
	return value, nil
}
