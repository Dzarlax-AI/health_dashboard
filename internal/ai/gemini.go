package ai

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

//go:embed prompt.txt
var systemPrompt string

// Model describes a single Gemini model available for content generation.
type Model struct {
	ID          string `json:"id"`           // e.g. "gemini-2.5-flash"
	DisplayName string `json:"display_name"` // e.g. "Gemini 2.5 Flash"
}

// ListModels returns all Gemini models that support generateContent,
// sorted as returned by the API. Returns an error if the key is invalid.
func ListModels(apiKey string) ([]Model, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	url := "https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google API error (%d): %s", resp.StatusCode, string(body))
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

// GenerateMorningBriefing calls the Gemini API to produce a morning health insight.
// model defaults to gemini-2.5-flash if empty; maxTokens defaults to 1000 if <= 0.
func GenerateMorningBriefing(apiKey, model string, maxTokens int, rawMetricsJSON []byte) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("gemini API key is not configured")
	}
	if model == "" {
		model = defaultModel
	}
	if maxTokens <= 0 {
		maxTokens = 1000
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model, apiKey,
	)

	payload := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": systemPrompt},
			},
		},
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": "Данные Apple Health (JSON):\n\n" + string(rawMetricsJSON)},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature":     0.2,
			"maxOutputTokens": maxTokens,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w (%s)", err, string(respBody))
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("unexpected gemini response format: %s", string(respBody))
	}

	return result.Candidates[0].Content.Parts[0].Text, nil
}
