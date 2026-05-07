package storage

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

// aiRegenFailBackoff is how long we wait after a failed regen before
// retrying. Keeps a sustained Gemini outage from amplifying into one
// regen attempt per polling tick.
const aiRegenFailBackoff = 5 * time.Minute

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
//
// Concurrency: only one EnsureTodayAIInsight per (date, lang) runs at a
// time across the process. Concurrent calls (sync morning-retry vs async
// poller-driven regen) return the current cache instead of duplicating
// Gemini work. After a failure, retries are throttled to once per
// aiRegenFailBackoff so a Gemini outage doesn't compound.
func (s *DB) EnsureTodayAIInsight(aiCfg AIConfig, lang string) string {
	if !aiCfg.Enabled() {
		return ""
	}
	today := time.Now().Format("2006-01-02")
	key := today + "|" + lang

	// Single-flight gate. If another caller is already regenerating, return
	// the (possibly empty) cache rather than fanning out. Caller will see
	// the cache populate on the next /api/ai-briefing poll.
	if _, loaded := s.aiRegenInFlight.LoadOrStore(key, true); loaded {
		return s.GetAIInsightCombined(today, lang)
	}
	defer s.aiRegenInFlight.Delete(key)

	// Failure backoff: skip regen for `aiRegenFailBackoff` after the last
	// run produced zero usable blocks (Gemini outage / quota / auth fail).
	if v, ok := s.aiRegenLastFailAt.Load(key); ok {
		if t, ok := v.(time.Time); ok && time.Since(t) < aiRegenFailBackoff {
			return s.GetAIInsightCombined(today, lang)
		}
	}

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

	saved := 0
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
		saved++
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
		} else {
			saved++
		}
	}

	// Track sustained failures so the next aiRegenFailBackoff window short-
	// circuits Gemini calls. On success, clear the timestamp.
	if saved == 0 {
		s.aiRegenLastFailAt.Store(key, time.Now())
	} else {
		s.aiRegenLastFailAt.Delete(key)
	}

	return s.GetAIInsightCombined(today, lang)
}

// EnsureTodayAIInsightAsync fires EnsureTodayAIInsight in a goroutine.
// The single-flight gate (and failure backoff) lives inside
// EnsureTodayAIInsight, so concurrent callers — including sync ones from
// the morning-retry / test-notify / opportunistic-trigger paths — share
// the same dedup. Returns true when this call likely started a regen
// (best-effort signal for logging; not authoritative because the inner
// gate races with this fast-path Load).
func (s *DB) EnsureTodayAIInsightAsync(aiCfg AIConfig, lang string) bool {
	if !aiCfg.Enabled() {
		return false
	}
	key := time.Now().Format("2006-01-02") + "|" + lang
	if _, ok := s.aiRegenInFlight.Load(key); ok {
		return false
	}
	go s.EnsureTodayAIInsight(aiCfg, lang)
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
