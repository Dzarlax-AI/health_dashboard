package storage

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

// EnsureTodayAIInsight regenerates the four AI blocks (SLEEP, YESTERDAY,
// RECOVERY, RECOMMENDATION) selectively: each leaf is keyed by an
// inputs_hash over the metrics it depends on, so a late HRV update only
// invalidates the blocks that actually read HRV. Returns the joined
// insight (legacy callers still expect a single string), or "" if AI is
// disabled / no metrics exist.
//
// Safe to call repeatedly — cached rows whose inputs_hash matches the
// current data are skipped, and only the leaves whose hashes diverged hit
// the Gemini API. RECOMMENDATION re-runs whenever any leaf text changes
// or when EnergyBank.action_verdict has rotated.
func (s *DB) EnsureTodayAIInsight(aiCfg AIConfig, lang string) string {
	if !aiCfg.Enabled() {
		return ""
	}
	today := time.Now().Format("2006-01-02")

	raw := s.GetRawMetrics()
	if raw == nil {
		log.Println("EnsureTodayAIInsight: no raw metrics available")
		return ""
	}

	// EnergyBank lives on the briefing response — fetch it for the recovery
	// hash and to give RECOMMENDATION the action_verdict context it must
	// align with.
	briefing, err := s.GetHealthBriefing(lang)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: briefing: %v", err)
		return ""
	}
	var eb *health.EnergyBank
	var readiness *float64
	if briefing != nil {
		eb = briefing.EnergyBank
		r := float64(briefing.ReadinessScore)
		readiness = &r
	}

	rawJSON, err := json.Marshal(raw)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: marshal: %v", err)
		return ""
	}

	hashes := map[string]string{
		ai.BlockSleep:     ai.HashSleep(raw),
		ai.BlockYesterday: ai.HashYesterday(raw),
		ai.BlockRecovery:  ai.HashRecovery(raw, eb, readiness),
	}

	cached := s.GetAIBlocksFull(today, lang)

	skip := func(block string) bool {
		row := cached[block]
		return row != nil && row.InputsHash == hashes[block] && strings.TrimSpace(row.Text) != ""
	}

	results := ai.GenerateLeafBlocks(aiCfg.APIKey, aiCfg.Model, aiCfg.MaxOutputTokens, rawJSON, lang, skip)
	for _, r := range results {
		if r.Err != nil {
			log.Printf("EnsureTodayAIInsight: gemini %s: %v", r.Block, r.Err)
			continue
		}
		if strings.TrimSpace(r.Text) == "" {
			log.Printf("EnsureTodayAIInsight: gemini %s returned empty content, not caching", r.Block)
			continue
		}
		if err := s.SaveAIBlock(today, lang, r.Block, r.Text, hashes[r.Block]); err != nil {
			log.Printf("EnsureTodayAIInsight: save %s: %v", r.Block, err)
			continue
		}
		cached[r.Block] = &AIBlock{Block: r.Block, Text: r.Text, InputsHash: hashes[r.Block]}
	}

	textOf := func(block string) string {
		if b := cached[block]; b != nil {
			return b.Text
		}
		return ""
	}
	sleepText := textOf(ai.BlockSleep)
	yesterdayText := textOf(ai.BlockYesterday)
	recoveryText := textOf(ai.BlockRecovery)
	recHash := ai.HashRecommendation(sleepText, yesterdayText, recoveryText, eb)
	recRow := cached[ai.BlockRecommendation]
	if recRow == nil || recRow.InputsHash != recHash || strings.TrimSpace(recRow.Text) == "" {
		recText, err := ai.GenerateRecommendation(aiCfg.APIKey, aiCfg.Model, aiCfg.MaxOutputTokens, rawJSON, lang,
			sleepText, yesterdayText, recoveryText)
		if err != nil {
			log.Printf("EnsureTodayAIInsight: gemini RECOMMENDATION: %v", err)
		} else if strings.TrimSpace(recText) == "" {
			log.Println("EnsureTodayAIInsight: gemini RECOMMENDATION returned empty content, not caching")
		} else if err := s.SaveAIBlock(today, lang, ai.BlockRecommendation, recText, recHash); err != nil {
			log.Printf("EnsureTodayAIInsight: save RECOMMENDATION: %v", err)
		}
	}

	return s.GetAIInsightCombined(today, lang)
}

// EnsureTodayAIInsightAsync fires EnsureTodayAIInsight in a goroutine and
// dedupes concurrent calls per (date, lang). Returns true when the caller's
// invocation actually started a regen (caller can use this to log / set a
// "generating" flag in the response). False means a regen is already running
// from another caller; the cache will populate when that one finishes.
func (s *DB) EnsureTodayAIInsightAsync(aiCfg AIConfig, lang string) bool {
	if !aiCfg.Enabled() {
		return false
	}
	key := time.Now().Format("2006-01-02") + "|" + lang
	if _, loaded := s.aiRegenInFlight.LoadOrStore(key, true); loaded {
		return false
	}
	go func() {
		defer s.aiRegenInFlight.Delete(key)
		s.EnsureTodayAIInsight(aiCfg, lang)
	}()
	return true
}

// AIRegenInFlight reports whether a regen is currently running for (today, lang).
// Used by the /api/ai-briefing handler to set a "generating" flag in the
// response when the cache is still warming up.
func (s *DB) AIRegenInFlight(lang string) bool {
	key := time.Now().Format("2006-01-02") + "|" + lang
	_, ok := s.aiRegenInFlight.Load(key)
	return ok
}
