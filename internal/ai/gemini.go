package ai

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed prompt.txt
var systemPrompt string

// ListModels returns all Gemini models that support generateContent,
// sorted as returned by the API. Returns an error if the key is invalid.
func ListModels(apiKey string) ([]Model, error) {
	return listModels(context.Background(), apiKey)
}

func listModels(ctx context.Context, apiKey string) ([]Model, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://generativelanguage.googleapis.com/v1beta/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("x-goog-api-key", apiKey)
	resp, err := geminiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google API error (status %d)", resp.StatusCode)
	}

	var result struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	var out []Model
	for _, m := range result.Models {
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				id := strings.TrimPrefix(m.Name, "models/")
				out = append(out, Model{ID: id, DisplayName: m.DisplayName})
				break
			}
		}
	}
	return out, nil
}

const defaultModel = "gemini-2.5-flash"

type GeminiProvider struct{}

func (GeminiProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{
		ID:                ProviderGemini,
		DisplayName:       "Gemini",
		DefaultModel:      defaultModel,
		SupportsReasoning: true,
		ReasoningEfforts:  []string{"minimal", "low", "medium", "high"},
		APIKeyPlaceholder: "AIza...",
		// This is the cross-model safe default for the editable Admin form.
		// Generate and B1 resolution refine it for known Flash models.
		DefaultReasoning: "low",
	}
}

func (GeminiProvider) ListModels(ctx context.Context, apiKey string) ([]Model, error) {
	return listModels(ctx, apiKey)
}

func (GeminiProvider) Generate(ctx context.Context, cfg ProviderConfig, req GenerationRequest) (GenerationResult, error) {
	return generateWithPrompt(
		ctx,
		cfg.APIKey,
		cfg.Model,
		cfg.MaxOutputTokens,
		cfg.ReasoningEffort,
		req.Prompt,
		req.UserPayload,
		req.Language,
		req.ResponseSchema,
	)
}

func init() {
	RegisterProvider(GeminiProvider{})
}

// geminiClient bounds Gemini calls so a hung remote can't pin a goroutine
// forever. Insight v2 makes one call per regeneration, but every request must
// still be independently bounded.
var geminiClient = &http.Client{Timeout: 60 * time.Second}

var langNames = map[string]string{
	"ru": "Russian",
	"en": "English",
	"sr": "Serbian",
}

// generateWithPrompt is the shared HTTP path for any Gemini call. Callers
// supply the system prompt and the user-facing payload bytes.
//
//nolint:revive // keep arg order stable for the orchestrator callsite
func generateWithPrompt(ctx context.Context, apiKey, model string, maxTokens int, reasoningEffort, prompt string, userPayload []byte, lang string, responseSchema *ResponseSchema) (GenerationResult, error) {
	if apiKey == "" {
		return GenerationResult{}, fmt.Errorf("gemini API key is not configured")
	}
	if model == "" {
		model = defaultModel
	}
	if maxTokens <= 0 {
		maxTokens = DefaultMaxOutputTokens
	}

	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", url.PathEscape(model))

	langName := langNames[lang]
	if langName == "" {
		langName = "English"
	}

	// Build the payload without the API key — we store it for auditing.
	generationConfig := map[string]any{
		"maxOutputTokens": maxTokens,
	}
	// Gemini 3 spends maxOutputTokens on both hidden thinking and visible JSON.
	// B1 uses a tiny structured response, so make the lowest supported thinking
	// level explicit instead of inheriting Gemini 3's high default. Gemini 2.5
	// uses a different thinkingBudget contract, so leave it unchanged here.
	if isGemini3Model(model) {
		level, err := geminiThinkingLevel(model, reasoningEffort)
		if err != nil {
			return GenerationResult{}, err
		}
		generationConfig["thinkingConfig"] = map[string]string{"thinkingLevel": level}
	}
	payload := map[string]any{
		"model": model,
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": prompt + "\n\nRESPONSE LANGUAGE: Write the entire response in " + langName + ". All numbers and text must be in " + langName + "."},
			},
		},
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": fmt.Sprintf("Use the evaluation date from the supplied health data; never infer it from server time.\n\nApple Health data (JSON):\n\n%s", string(userPayload))},
				},
			},
		},
		"generationConfig": generationConfig,
	}
	if responseSchema != nil {
		config := payload["generationConfig"].(map[string]any)
		config["responseMimeType"] = "application/json"
		config["responseJsonSchema"] = responseSchema.Schema
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("marshal payload: %w", err)
	}

	buildRequest := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", apiKey)
		return req, nil
	}
	resp, attempts, latency, err := doRequestWithRetry(ctx, geminiClient, buildRequest)
	if err != nil {
		return GenerationResult{RequestPayload: bodyBytes, Attempts: attempts, Latency: latency}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	baseResult := GenerationResult{
		RequestPayload: bodyBytes,
		RequestID:      firstHeader(resp.Header, "x-request-id", "x-goog-request-id"),
		Attempts:       attempts,
		Latency:        latency,
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return baseResult, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return baseResult, fmt.Errorf("gemini error (status %d)", resp.StatusCode)
	}

	var result struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
			TotalTokenCount      int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return baseResult, fmt.Errorf("unmarshal response: %w", err)
	}
	baseResult.InputTokens = result.UsageMetadata.PromptTokenCount
	baseResult.OutputTokens = result.UsageMetadata.CandidatesTokenCount
	baseResult.TotalTokens = result.UsageMetadata.TotalTokenCount
	if result.PromptFeedback.BlockReason != "" {
		baseResult.FinishReason = result.PromptFeedback.BlockReason
		return baseResult, fmt.Errorf("gemini prompt blocked: %s", result.PromptFeedback.BlockReason)
	}

	if len(result.Candidates) == 0 {
		return baseResult, fmt.Errorf("unexpected gemini response format")
	}
	baseResult.FinishReason = result.Candidates[0].FinishReason
	if baseResult.FinishReason != "" && baseResult.FinishReason != "STOP" {
		return baseResult, fmt.Errorf("gemini response did not finish cleanly: %s", baseResult.FinishReason)
	}
	if len(result.Candidates[0].Content.Parts) == 0 {
		return baseResult, fmt.Errorf("unexpected gemini response format")
	}
	var textParts []string
	for _, part := range result.Candidates[0].Content.Parts {
		if strings.TrimSpace(part.Text) != "" {
			textParts = append(textParts, part.Text)
		}
	}
	if len(textParts) == 0 {
		return baseResult, fmt.Errorf("unexpected gemini response format")
	}
	baseResult.Text = strings.Join(textParts, "")
	return baseResult, nil
}

func isGemini3Model(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-3")
}

func geminiThinkingLevel(model, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = GeminiDefaultThinkingLevel(model)
	}
	if !ValidGeminiThinkingLevel(model, value) {
		return "", fmt.Errorf("invalid Gemini thinking level %q for model %q", value, model)
	}
	return value, nil
}

// GeminiDefaultThinkingLevel selects the least expensive level which is valid
// for a known Gemini 3 family. Unknown future Gemini 3 names take the safe
// shared level rather than inheriting a possibly unsupported Flash-only value.
func GeminiDefaultThinkingLevel(model string) string {
	if supportsGeminiThinkingLevel(model, "minimal") {
		return "minimal"
	}
	return "low"
}

// ValidGeminiThinkingLevel captures the documented per-model Gemini 3
// differences. A conservative low/high intersection protects custom future
// model names until their capabilities are explicitly added here.
func ValidGeminiThinkingLevel(model, value string) bool {
	return supportsGeminiThinkingLevel(model, value)
}

func supportsGeminiThinkingLevel(model, value string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	var levels []string
	switch {
	case strings.HasPrefix(model, "gemini-3-pro-preview"):
		levels = []string{"low", "high"}
	case strings.HasPrefix(model, "gemini-3.1-pro"):
		levels = []string{"low", "medium", "high"}
	case strings.HasPrefix(model, "gemini-3.1-flash-lite"):
		levels = []string{"minimal", "high"}
	case strings.HasPrefix(model, "gemini-3.7-flash"), strings.HasPrefix(model, "gemini-3.8-flash"):
		levels = []string{"low", "medium", "high"}
	case strings.HasPrefix(model, "gemini-3-flash-preview"), strings.HasPrefix(model, "gemini-3.5-flash"), strings.HasPrefix(model, "gemini-3.6-flash"):
		levels = []string{"minimal", "low", "medium", "high"}
	default:
		levels = []string{"low", "high"}
	}
	for _, allowed := range levels {
		if value == allowed {
			return true
		}
	}
	return false
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := headers.Get(name); value != "" {
			return value
		}
	}
	return ""
}
