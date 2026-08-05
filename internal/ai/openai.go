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

type OpenAIProvider struct {
	client  *http.Client
	baseURL string
}

func NewOpenAIProvider(client *http.Client, baseURL string) *OpenAIProvider {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
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
		// The models endpoint does not expose an input/output capability map.
		// Offer likely text-generation models as suggestions while leaving the
		// Admin model field editable.
		if strings.HasPrefix(model.ID, "gpt-") || strings.HasPrefix(model.ID, "o") {
			out = append(out, Model{ID: model.ID, DisplayName: model.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = "none"
	}
	if !ValidReasoningEffort(cfg.ReasoningEffort) {
		return GenerationResult{}, fmt.Errorf("invalid OpenAI reasoning effort %q", cfg.ReasoningEffort)
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
		"reasoning": map[string]string{
			"effort": cfg.ReasoningEffort,
		},
		"store": false,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(bodyBytes))
	if err != nil {
		return GenerationResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return GenerationResult{RequestPayload: bodyBytes}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return GenerationResult{RequestPayload: bodyBytes}, fmt.Errorf("openai API error (status %d)", resp.StatusCode)
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
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return GenerationResult{RequestPayload: bodyBytes}, fmt.Errorf("unmarshal response: %w", err)
	}
	if result.Status == "incomplete" {
		return GenerationResult{RequestPayload: bodyBytes}, fmt.Errorf("openai response incomplete: %s", result.IncompleteDetails.Reason)
	}
	for _, output := range result.Output {
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "refusal" || content.Refusal != "" {
				return GenerationResult{RequestPayload: bodyBytes}, fmt.Errorf("openai response refused")
			}
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				return GenerationResult{Text: content.Text, RequestPayload: bodyBytes}, nil
			}
		}
	}
	return GenerationResult{RequestPayload: bodyBytes}, fmt.Errorf("unexpected openai response format")
}

func ValidReasoningEffort(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func init() {
	RegisterProvider(NewOpenAIProvider(nil, ""))
}
