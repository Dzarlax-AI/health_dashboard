package storage

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"
)

// AIBlock holds cached provider output for a single (date, lang, block)
// triple. AI insight v2 writes SYNTHESIS only. Historical leaf blocks remain
// readable as a compatibility fallback until a synthesis exists.
type AIBlock struct {
	Block      string
	Text       string
	InputsHash string
}

// EnsureAIBriefingBlocksTable creates the AI cache. Called on startup alongside
// EnsureAIBriefingsTable. SYNTHESIS is the v2 source of truth; the existing
// block-shaped table avoids a schema migration and preserves rollback data.
//
// Also runs a one-shot migration: existing ai_briefings.insight blobs are
// split by SLEEP/YESTERDAY/RECOVERY/RECOMMENDATION headers and inserted as
// per-block rows with inputs_hash='legacy'. Idempotent — ON CONFLICT DO
// NOTHING ensures re-runs don't overwrite freshly-generated blocks. The
// 'legacy' sentinel never matches a real hash, so the next briefing tick
// regenerates blocks naturally as their inputs change.
func (s *DB) EnsureAIBriefingBlocksTable() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.EnsureAIBriefingBlocksTableContext(ctx); err != nil {
		log.Printf("EnsureAIBriefingBlocksTable: %v", err)
	}
}

func (s *DB) EnsureAIBriefingBlocksTableContext(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS ai_briefing_blocks (
			date         TEXT NOT NULL,
			lang         TEXT NOT NULL,
			block        TEXT NOT NULL,
			text         TEXT NOT NULL,
			inputs_hash  TEXT NOT NULL,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (date, lang, block)
		)
	`); err != nil {
		return err
	}
	return s.migrateLegacyAIBriefings(ctx)
}

// migrateLegacyAIBriefings copies any ai_briefings rows that don't yet have
// matching per-block rows. Skips rows with empty lang (legacy pre-i18n
// inserts) — they predate the new orchestrator's lang requirement.
func (s *DB) migrateLegacyAIBriefings(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT date, lang, insight FROM ai_briefings
		 WHERE insight <> '' AND lang <> ''`)
	if err != nil {
		return err
	}
	type row struct{ date, lang, insight string }
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.date, &r.lang, &r.insight); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range batch {
		blocks := splitLegacyBriefing(r.insight)
		for block, text := range blocks {
			if text == "" {
				continue
			}
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO ai_briefing_blocks (date, lang, block, text, inputs_hash, created_at)
				VALUES ($1, $2, $3, $4, 'legacy', NOW())
				ON CONFLICT (date, lang, block) DO NOTHING`,
				r.date, r.lang, block, text); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitLegacyBriefing parses the joined Gemini blob using the same header
// recognition as the now-deleted notify/aiparse.go. Headers: SLEEP /
// YESTERDAY / RECOVERY / RECOMMENDATION (en + ru/sr equivalents,
// case-insensitive on a trimmed line).
func splitLegacyBriefing(text string) map[string]string {
	headerTokens := map[string][]string{
		"SLEEP":          {"sleep", "сон", "san"},
		"YESTERDAY":      {"yesterday", "вчера", "juče", "juce"},
		"RECOVERY":       {"recovery", "восстановление", "oporavak"},
		"RECOMMENDATION": {"recommendation", "рекомендация", "preporuka"},
	}
	headerKey := func(line string) string {
		l := strings.TrimSpace(line)
		l = strings.Trim(l, ":：—-•")
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" || len(l) > 30 {
			return ""
		}
		for k, toks := range headerTokens {
			for _, t := range toks {
				if l == t {
					return k
				}
			}
		}
		return ""
	}

	out := map[string]string{}
	current := ""
	var buf strings.Builder
	flush := func() {
		body := strings.TrimSpace(buf.String())
		buf.Reset()
		if current == "" || body == "" {
			return
		}
		out[current] = body
	}
	for _, line := range strings.Split(text, "\n") {
		if k := headerKey(line); k != "" {
			flush()
			current = k
			continue
		}
		if current == "" {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return out
}

// GetAIBlock returns the cached block for (date, lang, block) or nil.
func (s *DB) GetAIBlock(date, lang, block string) *AIBlock {
	ctx, cancel := queryCtx()
	defer cancel()
	var b AIBlock
	b.Block = block
	err := s.pool.QueryRow(ctx,
		`SELECT text, inputs_hash FROM ai_briefing_blocks
		  WHERE date = $1 AND lang = $2 AND block = $3`,
		date, lang, block).Scan(&b.Text, &b.InputsHash)
	if err != nil {
		return nil
	}
	return &b
}

// SaveAIBlock upserts one block.
func (s *DB) SaveAIBlock(date, lang, block, text, inputsHash string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_briefing_blocks (date, lang, block, text, inputs_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (date, lang, block) DO UPDATE
			SET text = excluded.text,
			    inputs_hash = excluded.inputs_hash,
			    created_at = NOW()
	`, date, lang, block, text, inputsHash)
	return err
}

// GetAIBlocks returns the canonical text view for (date, lang). SYNTHESIS
// suppresses historical leaf rows; without it the old blocks are returned for
// compatibility with cached pre-v2 reports.
func (s *DB) GetAIBlocks(date, lang string) map[string]string {
	return canonicalAIBlockTexts(s.GetAIBlocksFull(date, lang))
}

func canonicalAIBlockTexts(full map[string]*AIBlock) map[string]string {
	if synthesis := full["SYNTHESIS"]; synthesis != nil && strings.TrimSpace(synthesis.Text) != "" {
		return map[string]string{"SYNTHESIS": synthesis.Text}
	}
	out := make(map[string]string, len(full))
	for k, v := range full {
		out[k] = v.Text
	}
	return out
}

// GetAIBlocksFull returns every stored row including inputs_hash. Always
// returns an initialized map (empty on query error).
func (s *DB) GetAIBlocksFull(date, lang string) map[string]*AIBlock {
	out := make(map[string]*AIBlock)
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT block, text, inputs_hash FROM ai_briefing_blocks WHERE date = $1 AND lang = $2`,
		date, lang)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		b := &AIBlock{}
		if err := rows.Scan(&b.Block, &b.Text, &b.InputsHash); err == nil {
			out[b.Block] = b
		}
	}
	return out
}

// GetAIInsightCombined returns SYNTHESIS directly when present. For dates that
// have not regenerated under v2, it joins the four historical blocks so
// dashboard and MCP clients keep working during rollout and rollback.
func (s *DB) GetAIInsightCombined(date, lang string) string {
	blocks := s.GetAIBlocks(date, lang)
	if len(blocks) == 0 {
		return ""
	}
	// AI insight v2 stores one validated explanation. Once present it is the
	// canonical narrative; legacy leaf rows remain only as rollback data and
	// must not leak back into current clients.
	if synthesis := strings.TrimSpace(blocks["SYNTHESIS"]); synthesis != "" {
		return synthesis
	}
	order := []string{"SLEEP", "YESTERDAY", "RECOVERY", "RECOMMENDATION"}
	seen := map[string]bool{}
	var sb strings.Builder
	for _, k := range order {
		if t := blocks[k]; t != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(k)
			sb.WriteByte('\n')
			sb.WriteString(strings.TrimSpace(t))
			seen[k] = true
		}
	}
	// Surface any unexpected block names appended at the end (defensive).
	extras := make([]string, 0)
	for k := range blocks {
		if !seen[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		sb.WriteString("\n\n")
		sb.WriteString(k)
		sb.WriteByte('\n')
		sb.WriteString(strings.TrimSpace(blocks[k]))
	}
	return sb.String()
}
