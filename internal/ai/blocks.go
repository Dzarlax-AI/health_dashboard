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
				"description": "Repeat the supplied verdict_reason exactly. The server replaces this field with its authoritative value before persistence.",
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
				"description": "Repeat the supplied action exactly. The server replaces this field with its authoritative value before persistence.",
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

// GenerateInsightBundle makes one provider call and validates the complete
// narrative bundle. A malformed or unsafe field rejects the whole generation,
// so callers never persist a mix of old and newly generated sections.
func GenerateInsightBundle(ctx context.Context, provider Provider, cfg ProviderConfig, evidenceJSON []byte, lang string) (InsightBundleResult, error) {
	if cfg.MaxOutputTokens <= 0 || cfg.MaxOutputTokens > SynthesisMaxTokens {
		cfg.MaxOutputTokens = SynthesisMaxTokens
	}
	var evidence health.MorningInsightEvidence
	if err := json.Unmarshal(evidenceJSON, &evidence); err != nil {
		return InsightBundleResult{}, fmt.Errorf("decode insight evidence: %w", err)
	}
	preliminarySleep := health.NormalizeMorningInsightEvidence(&evidence, lang)
	providerPayload, err := json.Marshal(evidence)
	if err != nil {
		return InsightBundleResult{}, fmt.Errorf("encode insight evidence: %w", err)
	}
	generated, err := provider.Generate(ctx, cfg, GenerationRequest{
		Prompt:         systemPrompt,
		UserPayload:    providerPayload,
		Language:       lang,
		ResponseSchema: insightBundleSchema(preliminarySleep, &evidence),
	})
	if err != nil {
		return InsightBundleResult{GenerationResult: generated}, err
	}
	var envelope insightBundleEnvelope
	if err := json.Unmarshal([]byte(generated.Text), &envelope); err != nil {
		return InsightBundleResult{GenerationResult: generated}, fmt.Errorf("decode insight bundle: %w", err)
	}
	sleepText, activityText, recoveryText := envelope.Sleep, envelope.Activity, envelope.Recovery
	var invalidPreliminary map[string]string
	if preliminarySleep {
		invalidPreliminary = make(map[string]string)
		sleepText = selectedSleepExplanation(envelope.Sleep, evidence.NightSleep, lang)
		if sleepText == "" {
			invalidPreliminary[BlockSleep] = "unsupported preliminary sleep explanation"
		}
		activityText = selectedPreliminaryExplanation(envelope.Activity, evidence.PreliminaryOptions.Activity)
		if activityText == "" {
			invalidPreliminary[BlockYesterday] = "unsupported preliminary activity explanation"
		}
		recoveryText = selectedPreliminaryExplanation(envelope.Recovery, evidence.PreliminaryOptions.Recovery)
		if recoveryText == "" {
			invalidPreliminary[BlockRecovery] = "unsupported preliminary recovery explanation"
		}
		if len(invalidPreliminary) > 0 {
			return InsightBundleResult{GenerationResult: generated, InvalidBlocks: invalidPreliminary}, fmt.Errorf("insight bundle contains unsupported preliminary explanation")
		}
	}
	candidates := map[string]string{
		BlockSynthesis:      firstNonEmptyInsight(evidence.VerdictReason, evidence.VerdictLabel, evidence.Action),
		BlockSleep:          sleepText,
		BlockYesterday:      activityText,
		BlockRecovery:       recoveryText,
		BlockRecommendation: firstNonEmptyInsight(evidence.Action, evidence.VerdictReason),
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
	if len(result.InvalidBlocks) > 0 {
		return result, fmt.Errorf("%d insight bundle blocks failed validation", len(result.InvalidBlocks))
	}
	return result, nil
}

func insightBundleSchema(preliminary bool, evidence *health.MorningInsightEvidence) *ResponseSchema {
	if !preliminary || evidence == nil || evidence.NightSleep == nil || evidence.PreliminaryOptions == nil {
		return insightBundleResponseSchema
	}
	properties := make(map[string]any, len(insightBundleResponseSchema.Schema["properties"].(map[string]any)))
	for key, value := range insightBundleResponseSchema.Schema["properties"].(map[string]any) {
		properties[key] = value
	}
	choices := []string{health.MorningSleepChoiceRecordedDuration}
	if evidence.NightSleep.Capture == health.NightCapturePartial {
		choices = append(choices, health.MorningSleepChoiceAwaitingCompletion)
	} else {
		choices = append(choices, health.MorningSleepChoiceAwaitingFinalization)
	}
	properties["sleep"] = map[string]any{
		"type":        "string",
		"enum":        choices,
		"description": "Choose the safe interpretation that best fits the preliminary sleep record. The server localizes the choice; do not return prose.",
	}
	properties["activity"] = preliminaryOptionSchema(evidence.PreliminaryOptions.Activity, "Pick the most relevant server-authored activity/cardio statement ID. Return only its ID.")
	properties["recovery"] = preliminaryOptionSchema(evidence.PreliminaryOptions.Recovery, "Pick the most relevant server-authored recovery statement ID. Return only its ID.")
	schema := make(map[string]any, len(insightBundleResponseSchema.Schema))
	for key, value := range insightBundleResponseSchema.Schema {
		schema[key] = value
	}
	schema["properties"] = properties
	return &ResponseSchema{Name: insightBundleResponseSchema.Name, Schema: schema}
}

func preliminaryOptionSchema(options []health.MorningInsightOption, description string) map[string]any {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	return map[string]any{"type": "string", "enum": ids, "description": description}
}

func allowedPreliminarySleepChoice(choice string, sleep *health.MorningReportSleep) bool {
	if sleep == nil {
		return false
	}
	if choice == health.MorningSleepChoiceRecordedDuration {
		return true
	}
	return (sleep.Capture == health.NightCapturePartial && (sleep.Finalization == health.NightFinalProvisional || sleep.Finalization == health.NightFinalFinal) && choice == health.MorningSleepChoiceAwaitingCompletion) ||
		(sleep.Capture == health.NightCaptureComplete && sleep.Finalization == health.NightFinalProvisional && choice == health.MorningSleepChoiceAwaitingFinalization)
}

func selectedSleepExplanation(choice string, sleep *health.MorningReportSleep, lang string) string {
	choice = strings.TrimSpace(choice)
	if !allowedPreliminarySleepChoice(choice, sleep) {
		return ""
	}
	value, _ := health.MorningSleepExplanation(choice, lang)
	return value
}

func selectedPreliminaryExplanation(id string, options []health.MorningInsightOption) string {
	id = strings.TrimSpace(id)
	for _, option := range options {
		if id == option.ID {
			return option.Text
		}
	}
	return ""
}

func firstNonEmptyInsight(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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
