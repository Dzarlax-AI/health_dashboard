package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGeminiProviderStructuredOutputMultipartAndTelemetry(t *testing.T) {
	original := geminiClient
	defer func() { geminiClient = original }()

	var got map[string]any
	geminiClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := jsonResponse(http.StatusOK, `{
			"candidates":[{
				"finishReason":"STOP",
				"content":{"parts":[{"text":"{\"explanation\":"},{"text":"\"Aligned explanation.\"}"}]}
			}],
			"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":12,"totalTokenCount":112}
		}`)
		resp.Header.Set("x-goog-request-id", "google_req_123")
		return resp, nil
	})

	result, err := (GeminiProvider{}).Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gemini-2.5-flash", MaxOutputTokens: 300,
	}, GenerationRequest{
		Prompt: "p",
		ResponseSchema: &ResponseSchema{
			Name:   "briefing",
			Schema: map[string]any{"type": "object"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	config, ok := got["generationConfig"].(map[string]any)
	if !ok || config["responseMimeType"] != "application/json" {
		t.Fatalf("generationConfig = %#v", got["generationConfig"])
	}
	if _, ok := config["responseJsonSchema"].(map[string]any); !ok {
		t.Fatalf("responseJsonSchema = %#v", config["responseJsonSchema"])
	}
	if result.Text != `{"explanation":"Aligned explanation."}` {
		t.Fatalf("text = %q", result.Text)
	}
	if result.RequestID != "google_req_123" || result.InputTokens != 100 || result.OutputTokens != 12 || result.TotalTokens != 112 {
		t.Fatalf("telemetry = %#v", result)
	}
	if result.FinishReason != "STOP" || result.Attempts != 1 {
		t.Fatalf("finish/attempts = %q/%d", result.FinishReason, result.Attempts)
	}
}

func TestGemini3ProviderSetsExplicitMinimalThinkingLevel(t *testing.T) {
	original := geminiClient
	defer func() { geminiClient = original }()

	var got map[string]any
	geminiClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}]}`), nil
	})

	_, err := (GeminiProvider{}).Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gemini-3-flash-preview", ReasoningEffort: "minimal", MaxOutputTokens: 2048,
	}, GenerationRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	config := got["generationConfig"].(map[string]any)
	thinking := config["thinkingConfig"].(map[string]any)
	if got := thinking["thinkingLevel"]; got != "minimal" {
		t.Fatalf("thinkingLevel = %#v, want minimal", got)
	}
}

func TestGemini25ProviderDoesNotSendGemini3ThinkingConfig(t *testing.T) {
	original := geminiClient
	defer func() { geminiClient = original }()

	var got map[string]any
	geminiClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}]}`), nil
	})
	_, err := (GeminiProvider{}).Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gemini-2.5-flash", ReasoningEffort: "minimal",
	}, GenerationRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	config := got["generationConfig"].(map[string]any)
	if _, found := config["thinkingConfig"]; found {
		t.Fatalf("Gemini 2.5 request unexpectedly includes Gemini 3 thinkingConfig: %#v", config)
	}
}

func TestGemini3ProviderRejectsUnsupportedThinkingLevel(t *testing.T) {
	_, err := (GeminiProvider{}).Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gemini-3-flash-preview", ReasoningEffort: "none",
	}, GenerationRequest{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "invalid Gemini thinking level") {
		t.Fatalf("error = %v", err)
	}
}

func TestGeminiProviderRejectsBlockedAndTruncatedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "prompt blocked",
			body: `{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[]}`,
			want: "prompt blocked",
		},
		{
			name: "max tokens",
			body: `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"partial"}]}}]}`,
			want: "did not finish cleanly",
		},
		{
			name: "max tokens without parts",
			body: `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[]}}]}`,
			want: "did not finish cleanly",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := geminiClient
			defer func() { geminiClient = original }()
			geminiClient = testHTTPClient(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, tt.body), nil
			})
			result, err := (GeminiProvider{}).Generate(context.Background(), ProviderConfig{
				APIKey: "secret", Model: "gemini-2.5-flash",
			}, GenerationRequest{Prompt: "p"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if tt.name == "max tokens without parts" && result.FinishReason != "MAX_TOKENS" {
				t.Fatalf("finish reason = %q, want MAX_TOKENS", result.FinishReason)
			}
		})
	}
}
