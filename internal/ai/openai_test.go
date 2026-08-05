package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestOpenAIProviderGenerateResponsesContract(t *testing.T) {
	var got map[string]any
	client := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Fatalf("authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Clinical text"}]}]}`), nil
	})

	provider := NewOpenAIProvider(client, "https://example.test/v1")
	result, err := provider.Generate(context.Background(), ProviderConfig{
		APIKey:          "secret",
		Model:           "gpt-5.6-luna",
		MaxOutputTokens: 500,
		ReasoningEffort: "none",
	}, GenerationRequest{
		Prompt:      "System prompt",
		UserPayload: []byte(`{"date":"2026-08-05"}`),
		Language:    "ru",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.Text != "Clinical text" {
		t.Fatalf("text = %q", result.Text)
	}
	if got["model"] != "gpt-5.6-luna" || got["store"] != false {
		t.Fatalf("request model/store = %#v/%#v", got["model"], got["store"])
	}
	reasoning, ok := got["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "none" {
		t.Fatalf("reasoning = %#v", got["reasoning"])
	}
	if input, _ := got["input"].(string); !strings.Contains(input, `"date":"2026-08-05"`) {
		t.Fatalf("input missing payload: %q", input)
	}
}

func TestOpenAIProviderGenerateStructuredOutputAndTelemetry(t *testing.T) {
	var got map[string]any
	client := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := jsonResponse(http.StatusOK, `{
			"status":"completed",
			"usage":{"input_tokens":120,"output_tokens":18,"total_tokens":138},
			"output":[{"type":"message","content":[{"type":"output_text","text":"{\"explanation\":\"Aligned explanation.\"}"}]}]
		}`)
		resp.Header.Set("x-request-id", "req_test_123")
		return resp, nil
	})
	provider := NewOpenAIProvider(client, "https://example.test")
	result, err := provider.Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gpt-5.6-luna", ReasoningEffort: "none",
	}, GenerationRequest{
		Prompt: "p",
		ResponseSchema: &ResponseSchema{
			Name: "briefing",
			Schema: map[string]any{
				"type": "object",
			},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	textConfig, ok := got["text"].(map[string]any)
	if !ok {
		t.Fatalf("text config = %#v", got["text"])
	}
	format, ok := textConfig["format"].(map[string]any)
	if !ok || format["type"] != "json_schema" || format["name"] != "briefing" || format["strict"] != true {
		t.Fatalf("format = %#v", textConfig["format"])
	}
	if result.RequestID != "req_test_123" || result.InputTokens != 120 || result.OutputTokens != 18 || result.TotalTokens != 138 {
		t.Fatalf("telemetry = %#v", result)
	}
	if result.Attempts != 1 || result.FinishReason != "completed" {
		t.Fatalf("attempts/finish = %d/%q", result.Attempts, result.FinishReason)
	}
}

func TestOpenAIProviderOmitsReasoningForNonReasoningModel(t *testing.T) {
	var got map[string]any
	client := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Text"}]}]}`), nil
	})
	provider := NewOpenAIProvider(client, "https://example.test")
	_, err := provider.Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gpt-4.1", ReasoningEffort: "high",
	}, GenerationRequest{
		Prompt:      "p",
		UserPayload: []byte(`{}`),
		ResponseSchema: &ResponseSchema{
			Name:   "briefing",
			Schema: map[string]any{"type": "object"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := got["reasoning"]; ok {
		t.Fatalf("non-reasoning model request contains reasoning: %#v", got["reasoning"])
	}
	textConfig, ok := got["text"].(map[string]any)
	if !ok {
		t.Fatalf("structured output text config = %#v", got["text"])
	}
	if _, ok := textConfig["verbosity"]; ok {
		t.Fatalf("gpt-4.1 request contains unsupported verbosity: %#v", textConfig)
	}
	if _, ok := textConfig["format"].(map[string]any); !ok {
		t.Fatalf("gpt-4.1 request lost structured output format: %#v", textConfig)
	}
}

func TestOpenAIProviderDefaultTimeoutSupportsLongerReasoningRuns(t *testing.T) {
	provider := NewOpenAIProvider(nil, "")
	if provider.client.Timeout != 120*time.Second {
		t.Fatalf("timeout = %v, want 120s", provider.client.Timeout)
	}
}

func TestOpenAIProviderRejectsIncompleteAndRefusal(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, "incomplete"},
		{"refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`, "refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testHTTPClient(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, tt.body), nil
			})
			provider := NewOpenAIProvider(client, "https://example.test")
			_, err := provider.Generate(context.Background(), ProviderConfig{
				APIKey: "secret", Model: "gpt-5.6-luna", ReasoningEffort: "none",
			}, GenerationRequest{Prompt: "p", UserPayload: []byte(`{}`)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestOpenAIProviderRejectsUpstreamAndMalformedResponsesWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"upstream error", http.StatusUnauthorized, `{"error":{"message":"secret vendor detail"}}`, "status 401"},
		{"rate limited", http.StatusTooManyRequests, `{"error":{"message":"retry later"}}`, "status 429"},
		{"upstream unavailable", http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`, "status 503"},
		{"malformed JSON", http.StatusOK, `{`, "unmarshal response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testHTTPClient(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse(tt.status, tt.body), nil
			})
			provider := NewOpenAIProvider(client, "https://example.test")
			_, err := provider.Generate(context.Background(), ProviderConfig{
				APIKey: "secret", Model: "gpt-5.6-luna", ReasoningEffort: "none",
			}, GenerationRequest{Prompt: "p", UserPayload: []byte(`{}`)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret vendor detail") {
				t.Fatalf("error leaked upstream response body: %v", err)
			}
		})
	}
}

func TestOpenAIProviderPropagatesContextCancellation(t *testing.T) {
	client := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	provider := NewOpenAIProvider(client, "https://example.test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.Generate(ctx, ProviderConfig{
		APIKey: "secret", Model: "gpt-5.6-luna", ReasoningEffort: "none",
	}, GenerationRequest{Prompt: "p", UserPayload: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestOpenAIProviderRejectsInvalidReasoningEffort(t *testing.T) {
	provider := NewOpenAIProvider(testHTTPClient(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("invalid config must not make an HTTP request")
		return nil, nil
	}), "https://example.test")
	_, err := provider.Generate(context.Background(), ProviderConfig{
		APIKey: "secret", Model: "gpt-5.6-luna", ReasoningEffort: "automatic",
	}, GenerationRequest{Prompt: "p", UserPayload: []byte(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "invalid OpenAI reasoning effort") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIProviderListModelsReturnsAllAndRanksTextSuggestionsFirst(t *testing.T) {
	client := testHTTPClient(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"data":[{"id":"whisper-1"},{"id":"omni-moderation-latest"},{"id":"gpt-5.6-luna"},{"id":"o4-mini"},{"id":"gpt-4.1"}]}`), nil
	})
	provider := NewOpenAIProvider(client, "https://example.test")
	models, err := provider.ListModels(context.Background(), "secret")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"gpt-4.1", "gpt-5.6-luna", "o4-mini", "omni-moderation-latest", "whisper-1"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	got := make([]string, 0, len(models))
	for _, model := range models {
		got = append(got, model.ID)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}

func TestProviderRegistryContainsBuiltins(t *testing.T) {
	for _, id := range []string{ProviderGemini, ProviderOpenAI} {
		if _, err := GetProvider(id); err != nil {
			t.Fatalf("GetProvider(%q): %v", id, err)
		}
	}
	if _, err := GetProvider("missing"); err == nil {
		t.Fatal("GetProvider(missing) returned no error")
	}
}
