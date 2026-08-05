package ai

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

const (
	ProviderGemini = "gemini"
	ProviderOpenAI = "openai"

	DefaultMaxOutputTokens = 5000
	PromptRevision         = "health-briefing-v1"
)

// Model describes a model exposed by an AI provider.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ProviderDescriptor contains non-secret provider metadata used by the Admin
// UI. Installation-specific configuration never lives in the registry.
type ProviderDescriptor struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name"`
	DefaultModel      string `json:"default_model"`
	SupportsReasoning bool   `json:"supports_reasoning"`
	APIKeyPlaceholder string `json:"api_key_placeholder"`
	DefaultReasoning  string `json:"default_reasoning,omitempty"`
}

// ProviderConfig is the active provider's resolved configuration. Adapters
// ignore fields they do not support.
type ProviderConfig struct {
	APIKey          string
	Model           string
	MaxOutputTokens int
	ReasoningEffort string
}

// GenerationRequest is deliberately provider-neutral. Each adapter translates
// it to the request shape required by its upstream API.
type GenerationRequest struct {
	Prompt      string
	UserPayload []byte
	Language    string
}

type GenerationResult struct {
	Text           string
	RequestPayload []byte
}

// Provider is the only vendor-specific boundary used by the block
// orchestrator.
type Provider interface {
	Descriptor() ProviderDescriptor
	ListModels(ctx context.Context, apiKey string) ([]Model, error)
	Generate(ctx context.Context, cfg ProviderConfig, req GenerationRequest) (GenerationResult, error)
}

var providerRegistry = struct {
	sync.RWMutex
	byID map[string]Provider
}{byID: make(map[string]Provider)}

// RegisterProvider installs an adapter. Duplicate IDs panic at process start
// because silently replacing a provider would make routing ambiguous.
func RegisterProvider(provider Provider) {
	if provider == nil {
		panic("ai: register nil provider")
	}
	id := provider.Descriptor().ID
	if id == "" {
		panic("ai: register provider with empty ID")
	}
	providerRegistry.Lock()
	defer providerRegistry.Unlock()
	if _, exists := providerRegistry.byID[id]; exists {
		panic("ai: duplicate provider ID " + id)
	}
	providerRegistry.byID[id] = provider
}

func GetProvider(id string) (Provider, error) {
	providerRegistry.RLock()
	defer providerRegistry.RUnlock()
	provider, ok := providerRegistry.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown AI provider %q", id)
	}
	return provider, nil
}

func ProviderDescriptors() []ProviderDescriptor {
	providerRegistry.RLock()
	defer providerRegistry.RUnlock()
	out := make([]ProviderDescriptor, 0, len(providerRegistry.byID))
	for _, provider := range providerRegistry.byID {
		out = append(out, provider.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GenerationFingerprint invalidates a cached block whenever output-affecting
// configuration changes, even if its health metrics are unchanged.
type GenerationFingerprint struct {
	Provider        string
	Model           string
	ReasoningEffort string
	MaxOutputTokens int
	PromptRevision  string
}

func HashForGeneration(inputHash string, fingerprint GenerationFingerprint) string {
	return hashInputs(struct {
		InputHash   string
		Fingerprint GenerationFingerprint
	}{inputHash, fingerprint})
}
