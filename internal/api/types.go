// Package api owns the stable transport envelopes shared by browser and
// native clients. Business logic stays in health/storage; this package only
// describes JSON sent over the public client API.
package api

import (
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// AIBriefingSection is one localized, ordered AI narrative block.
type AIBriefingSection struct {
	Key    string `json:"key"`
	Header string `json:"header"`
	Body   string `json:"body"`
}

// AIBriefingResponse is the non-blocking AI briefing transport shape.
//
// Sections is the canonical extensible representation. Insight, Blocks, and
// the four named block fields remain additive compatibility surfaces for
// already-released web and iOS clients.
type AIBriefingResponse struct {
	Date       string              `json:"date"`
	Lang       string              `json:"lang" jsonschema:"enum=en,enum=ru,enum=sr"`
	Insight    string              `json:"insight"`
	Sections   []AIBriefingSection `json:"sections"`
	Blocks     map[string]string   `json:"blocks"`
	Sleep      string              `json:"sleep,omitempty"`
	Yesterday  string              `json:"yesterday,omitempty"`
	Recovery   string              `json:"recovery,omitempty"`
	Recommend  string              `json:"recommendation,omitempty"`
	Generating bool                `json:"generating"`
	Disabled   bool                `json:"disabled"`
}

// NewAIBriefingResponse keeps every compatibility representation sourced from
// the same block map so canonical and legacy fields cannot drift.
func NewAIBriefingResponse(
	date string,
	lang string,
	insight string,
	sections []AIBriefingSection,
	blocks map[string]string,
	generating bool,
	disabled bool,
) AIBriefingResponse {
	return AIBriefingResponse{
		Date:       date,
		Lang:       lang,
		Insight:    insight,
		Sections:   sections,
		Blocks:     blocks,
		Sleep:      blocks["SLEEP"],
		Yesterday:  blocks["YESTERDAY"],
		Recovery:   blocks["RECOVERY"],
		Recommend:  blocks["RECOMMENDATION"],
		Generating: generating,
		Disabled:   disabled,
	}
}

// ReadinessHistoryResponse wraps readiness points so the response can grow
// additively without changing the top-level JSON kind.
type ReadinessHistoryResponse struct {
	Points []health.ReadinessPoint `json:"points"`
}

// EnergyHistoryDayResponse is the legacy day-level EnergyBank history shape.
type EnergyHistoryDayResponse struct {
	Granularity string                       `json:"granularity" jsonschema:"enum=day"`
	Points      []storage.EnergyHistoryPoint `json:"points"`
}

// EnergyHistoryHourResponse is the EnergyBank v2 intraday history shape.
type EnergyHistoryHourResponse struct {
	Granularity    string                        `json:"granularity" jsonschema:"enum=hour"`
	FormulaVersion int                           `json:"formula_version"`
	Points         []storage.EnergySnapshotPoint `json:"points"`
}
