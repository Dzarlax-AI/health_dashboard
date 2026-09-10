package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"health-receiver/internal/health"
)

// DailyInsightMaxTokens bounds one three-domain overlay. It is deliberately
// below the legacy five-block cap because the server already owns every
// factual field, action, state, and destination.
const DailyInsightMaxTokens = 900

const dailyInsightSystemPrompt = `You acknowledge a server-selected Today insight template.

The JSON input is authoritative. The server already selected every visible word, the primary, the three domains, all states, every allowed evidence ID, and the only permitted next step. You must not write any prose.

Safety and accuracy:
- For primary and every domain, return template exactly "server_default".
- Cite only the evidence IDs supplied for that exact section.
- Do not add, remove, or rename a domain, evidence ID, action, destination, state, title, date, measurement, trend, baseline, target, workout, restriction, observation, meaning, or advice.

Output JSON only. It must have primary and domains. Return one entry each for sleep, recovery, and energy, in that order.`

var dailyInsightNarrativeResponseSchema = &ResponseSchema{
	Name: "daily_insight_narrative",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"primary": dailyInsightNarrativeSectionSchema("The server-selected primary text. Use only its supplied evidence IDs."),
			"domains": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"key":          map[string]any{"type": "string", "enum": []string{"sleep", "recovery", "energy"}},
						"template":     map[string]any{"type": "string", "enum": []string{"server_default"}},
						"evidence_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required":             []string{"key", "template", "evidence_ids"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"primary", "domains"},
		"additionalProperties": false,
	},
}

func dailyInsightNarrativeSectionSchema(description string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": description,
		"properties": map[string]any{
			"template":     map[string]any{"type": "string", "enum": []string{"server_default"}},
			"evidence_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"template", "evidence_ids"},
		"additionalProperties": false,
	}
}

type DailyInsightNarrativeResult struct {
	GenerationResult
	Narrative      health.DailyInsightNarrative
	InvalidDomains map[string]string
}

// GenerateDailyInsightNarrative asks a provider to acknowledge a closed,
// server-authored template. It has no authority over text, state, actions, or
// destinations. Any bad or missing section rejects the acknowledgement so it
// cannot be persisted as ready.
func GenerateDailyInsightNarrative(ctx context.Context, provider Provider, cfg ProviderConfig, snapshot *health.DailyInsightSnapshot, lang string) (DailyInsightNarrativeResult, error) {
	if snapshot == nil {
		return DailyInsightNarrativeResult{}, fmt.Errorf("daily insight snapshot is nil")
	}
	if cfg.MaxOutputTokens <= 0 || cfg.MaxOutputTokens > DailyInsightMaxTokens {
		cfg.MaxOutputTokens = DailyInsightMaxTokens
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return DailyInsightNarrativeResult{}, fmt.Errorf("marshal daily insight snapshot: %w", err)
	}
	generated, err := provider.Generate(ctx, cfg, GenerationRequest{
		Prompt:         dailyInsightSystemPrompt,
		UserPayload:    payload,
		Language:       lang,
		ResponseSchema: dailyInsightNarrativeResponseSchema,
	})
	if err != nil {
		return DailyInsightNarrativeResult{GenerationResult: generated}, err
	}
	var candidate health.DailyInsightNarrative
	if err := json.Unmarshal([]byte(generated.Text), &candidate); err != nil {
		return DailyInsightNarrativeResult{GenerationResult: generated}, fmt.Errorf("decode daily insight narrative: %w", err)
	}
	narrative, invalidDomains, err := validateDailyInsightNarrative(snapshot, candidate)
	if err != nil {
		return DailyInsightNarrativeResult{GenerationResult: generated, InvalidDomains: invalidDomains}, err
	}
	return DailyInsightNarrativeResult{
		GenerationResult: generated,
		Narrative:        narrative,
		InvalidDomains:   invalidDomains,
	}, nil
}

func validateDailyInsightNarrative(snapshot *health.DailyInsightSnapshot, candidate health.DailyInsightNarrative) (health.DailyInsightNarrative, map[string]string, error) {
	invalidDomains := make(map[string]string)
	primary, err := validateDailyInsightNarrativeSection(candidate.Primary, snapshot.Primary.EvidenceIDs)
	if err != nil {
		return health.DailyInsightNarrative{}, invalidDomains, fmt.Errorf("primary: %w", err)
	}
	out := health.DailyInsightNarrative{Primary: primary, Domains: make([]health.DailyInsightNarrativeDomain, 0, len(snapshot.Domains))}
	expected := make(map[string]health.DailyInsightDomain, len(snapshot.Domains))
	for _, domain := range snapshot.Domains {
		expected[domain.Key] = domain
	}
	seen := make(map[string]struct{}, len(candidate.Domains))
	for _, domain := range candidate.Domains {
		serverDomain, known := expected[domain.Key]
		if !known {
			return health.DailyInsightNarrative{}, invalidDomains, fmt.Errorf("unknown domain %q", domain.Key)
		}
		if _, duplicate := seen[domain.Key]; duplicate {
			return health.DailyInsightNarrative{}, invalidDomains, fmt.Errorf("duplicate domain %q", domain.Key)
		}
		seen[domain.Key] = struct{}{}
		section, sectionErr := validateDailyInsightNarrativeSection(domain.DailyInsightNarrativeSection, serverDomain.Insight.EvidenceIDs)
		if sectionErr != nil {
			invalidDomains[domain.Key] = sectionErr.Error()
			return health.DailyInsightNarrative{}, invalidDomains, fmt.Errorf("domain %q: %w", domain.Key, sectionErr)
		}
		out.Domains = append(out.Domains, health.DailyInsightNarrativeDomain{Key: domain.Key, DailyInsightNarrativeSection: section})
	}
	for _, domain := range snapshot.Domains {
		if _, ok := seen[domain.Key]; !ok {
			invalidDomains[domain.Key] = "missing from provider response"
			return health.DailyInsightNarrative{}, invalidDomains, fmt.Errorf("missing domain %q", domain.Key)
		}
	}
	return out, invalidDomains, nil
}

func validateDailyInsightNarrativeSection(candidate health.DailyInsightNarrativeSection, allowedEvidenceIDs []string) (health.DailyInsightNarrativeSection, error) {
	if candidate.Template != "server_default" {
		return health.DailyInsightNarrativeSection{}, fmt.Errorf("unapproved narrative template %q", candidate.Template)
	}
	if len(candidate.EvidenceIDs) == 0 || len(candidate.EvidenceIDs) > 2 {
		return health.DailyInsightNarrativeSection{}, fmt.Errorf("expected one or two evidence IDs")
	}
	allowed := make(map[string]struct{}, len(allowedEvidenceIDs))
	for _, id := range allowedEvidenceIDs {
		allowed[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(candidate.EvidenceIDs))
	for _, id := range candidate.EvidenceIDs {
		if _, ok := allowed[id]; !ok {
			return health.DailyInsightNarrativeSection{}, fmt.Errorf("unapproved evidence ID %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return health.DailyInsightNarrativeSection{}, fmt.Errorf("duplicate evidence ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return health.DailyInsightNarrativeSection{Template: candidate.Template, EvidenceIDs: append([]string(nil), candidate.EvidenceIDs...)}, nil
}
