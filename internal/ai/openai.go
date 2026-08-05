package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const defaultOpenAIModel = "gpt-5.6-luna"
const defaultOpenAITimeout = 120 * time.Second

type OpenAIProvider struct {
	client  *http.Client
	baseURL string
}

func NewOpenAIProvider(client *http.Client, baseURL string) *OpenAIProvider {
	if client == nil {
		client = &http.Client{Timeout: defaultOpenAITimeout}
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		client:  client,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (p *OpenAIProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{
		ID:                ProviderOpenAI,
		DisplayName:       "OpenAI",
		DefaultModel:      defaultOpenAIModel,
		SupportsReasoning: true,
		APIKeyPlaceholder: "sk-...",
		DefaultReasoning:  "none",
	}
}

func (p *OpenAIProvider) ListModels(ctx context.Context, apiKey string) ([]Model, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai API error (status %d)", resp.StatusCode)
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	out := make([]Model, 0, len(result.Data))
	for _, model := range result.Data {
		// /models does not expose a reliable text-output capability map. Return
		// the complete server result as editable suggestions rather than hiding
		// future models behind a name heuristic.
		out = append(out, Model{ID: model.ID, DisplayName: model.ID})
	}
	sort.Slice(out, func(i, j int) bool {
		iText := openAITextModelRank(out[i].ID)
		jText := openAITextModelRank(out[j].ID)
		if iText != jText {
			return iText < jText
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (p *OpenAIProvider) Generate(ctx context.Context, cfg ProviderConfig, generation GenerationRequest) (GenerationResult, error) {
	if cfg.APIKey == "" {
		return GenerationResult{}, fmt.Errorf("openai API key is not configured")
	}
	if cfg.Model == "" {
		cfg.Model = defaultOpenAIModel
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = DefaultMaxOutputTokens
	}
	langName := langNames[generation.Language]
	if langName == "" {
		langName = "English"
	}
	payload := map[string]any{
		"model": cfg.Model,
		"instructions": generation.Prompt +
			"\n\nRESPONSE LANGUAGE: Write the entire response in " + langName +
			". All numbers and text must be in " + langName + ".",
		"input": fmt.Sprintf(
			"Use the evaluation date from the supplied health data; never infer it from server time.\n\nApple Health data (JSON):\n\n%s",
			string(generation.UserPayload),
		),
		"max_output_tokens": cfg.MaxOutputTokens,
		"store":             false,
	}
	textConfig := map[string]any{"verbosity": "low"}
	payload["text"] = textConfig
	if isOpenAIReasoningModel(cfg.Model) {
		if cfg.ReasoningEffort == "" {
			cfg.ReasoningEffort = "none"
		}
		if !ValidReasoningEffort(cfg.ReasoningEffort) {
			return GenerationResult{}, fmt.Errorf("invalid OpenAI reasoning effort %q", cfg.ReasoningEffort)
		}
		payload["reasoning"] = map[string]string{"effort": cfg.ReasoningEffort}
	}
	if generation.ResponseSchema != nil {
		textConfig["format"] = map[string]any{
			"type":   "json_schema",
			"name":   generation.ResponseSchema.Name,
			"strict": true,
			"schema": generation.ResponseSchema.Schema,
		}
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("marshal payload: %w", err)
	}
	buildRequest := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
	resp, attempts, latency, err := doRequestWithRetry(ctx, p.client, buildRequest)
	if err != nil {
		return GenerationResult{RequestPayload: bodyBytes, Attempts: attempts, Latency: latency}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	baseResult := GenerationResult{
		RequestPayload: bodyBytes,
		RequestID:      resp.Header.Get("x-request-id"),
		Attempts:       attempts,
		Latency:        latency,
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return baseResult, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return baseResult, fmt.Errorf("openai API error (status %d)", resp.StatusCode)
	}

	var result struct {
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return baseResult, fmt.Errorf("unmarshal response: %w", err)
	}
	baseResult.FinishReason = result.Status
	baseResult.InputTokens = result.Usage.InputTokens
	baseResult.OutputTokens = result.Usage.OutputTokens
	baseResult.TotalTokens = result.Usage.TotalTokens
	if result.Status == "incomplete" {
		baseResult.FinishReason = result.IncompleteDetails.Reason
		return baseResult, fmt.Errorf("openai response incomplete: %s", result.IncompleteDetails.Reason)
	}
	for _, output := range result.Output {
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "refusal" || content.Refusal != "" {
				baseResult.FinishReason = "refusal"
				return baseResult, fmt.Errorf("openai response refused")
			}
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				baseResult.Text = content.Text
				return baseResult, nil
			}
		}
	}
	return baseResult, fmt.Errorf("unexpected openai response format")
}

func ValidReasoningEffort(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func isOpenAIReasoningModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(model, "gpt-5") {
		return true
	}
	return len(model) > 1 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9'
}

func openAITextModelRank(model string) int {
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(model, "gpt-") || isOpenAIReasoningModel(model) {
		return 0
	}
	return 1
}

func init() {
	RegisterProvider(NewOpenAIProvider(nil, ""))
}
