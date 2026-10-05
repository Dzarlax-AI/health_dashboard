package storage

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"health-receiver/internal/health"
)

// LastGoodAIInsight is display-only history, never current generation evidence.
// The settings namespace inherits the request pool's tenant isolation.
type LastGoodAIInsight struct {
	Version             string                   `json:"version"`
	Date                string                   `json:"date"`
	Lang                string                   `json:"lang"`
	Slot                string                   `json:"slot"`
	MaterialHash        string                   `json:"material_hash"`
	ProviderFingerprint string                   `json:"provider_fingerprint"`
	GeneratedAt         time.Time                `json:"generated_at"`
	Insight             *health.AIInsightSection `json:"insight"`
}

func lastGoodAIKey(lang, slot string) string { return "_last_good_ai_v1:" + lang + ":" + slot }

func (s *DB) GetLastGoodAIInsights(ctx context.Context, lang, throughDate string) (map[string]LastGoodAIInsight, error) {
	// Adopt compatible pre-release accepted prose once. Polling updated_at is
	// not its generation time, so adopted rows carry only the source date.
	_, err := s.pool.Exec(ctx, `
	 INSERT INTO settings(key,value)
	 SELECT '_last_good_ai_v1:' || lang || ':' || slot,
	  jsonb_build_object('version',$3,'date',date,'lang',lang,'slot',slot,
	   'material_hash',narrative_input_hash,'provider_fingerprint',provider_fingerprint,
	   'insight',narrative->'insight')::text
	 FROM (
	  SELECT DISTINCT ON (slot) * FROM daily_insight_narrative_slots
	  WHERE lang=$1 AND date<=$2 AND narrative->>'version'=$3
	   AND narrative->>'locale'=lang AND narrative->>'slot'=slot
	   AND COALESCE(narrative->'insight'->>'text','')<>''
	   AND narrative_input_hash IS NOT NULL
	  ORDER BY slot,date DESC
	 ) accepted_history
	 ON CONFLICT(key) DO NOTHING`, lang, throughDate, health.AIInsightVersion)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, slot := range []string{"overall", "sleep", "recovery", "energy"} {
		keys = append(keys, lastGoodAIKey(lang, slot))
	}
	rows, err := s.pool.Query(ctx, `SELECT key,value FROM settings WHERE key=ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]LastGoodAIInsight{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		var entry LastGoodAIInsight
		if json.Unmarshal([]byte(value), &entry) != nil || entry.Version != health.AIInsightVersion || entry.Lang != lang ||
			!healthDailyInsightNarrativeSlot(entry.Slot) || key != lastGoodAIKey(lang, entry.Slot) || entry.Date > throughDate ||
			entry.Insight == nil || strings.TrimSpace(entry.Insight.Text) == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", entry.Date); err != nil {
			continue
		}
		result[entry.Slot] = entry
	}
	return result, rows.Err()
}

func (entry LastGoodAIInsight) DisplayInsight() *health.DailyInsightAIInsight {
	if entry.Insight == nil {
		return nil
	}
	var generatedAt *time.Time
	if !entry.GeneratedAt.IsZero() {
		generatedAt = &entry.GeneratedAt
	}
	// Historical fact IDs are not evidence IDs, and cannot link into the current
	// snapshot. Do not fabricate current evidence references for retained prose.
	return &health.DailyInsightAIInsight{Text: entry.Insight.Text, Stance: entry.Insight.Stance,
		AlternativeAction: entry.Insight.AlternativeAction, FactIDs: entry.Insight.FactIDs,
		EvidenceIDs: []string{}, Stale: true, SourceDate: entry.Date, GeneratedAt: generatedAt}
}
