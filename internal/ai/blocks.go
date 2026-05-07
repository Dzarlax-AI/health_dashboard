package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"health-receiver/internal/health"
)

// Block names used as cache keys. Stable, language-independent — the
// per-language text is stored alongside in ai_briefing_blocks.
const (
	BlockSleep          = "SLEEP"
	BlockYesterday      = "YESTERDAY"
	BlockRecovery       = "RECOVERY"
	BlockRecommendation = "RECOMMENDATION"
)

// LeafBlocks are the three independent blocks generated in parallel.
// RECOMMENDATION is the root, consumes leaf texts, runs after them.
var LeafBlocks = []string{BlockSleep, BlockYesterday, BlockRecovery}

// blockInstructions holds the per-block prompt fragment injected into the
// shared template at {{BLOCK_INSTRUCTIONS}}. Headers tell the model which
// block it is generating; bodies carry the instructions previously embedded
// in the all-in-one prompt.
var blockInstructions = map[string]string{
	BlockSleep: `SLEEP — Interpret last night's sleep relative to the personal 7-day baseline. Focus on: deviation from typical Deep/REM ratios, fragmentation patterns (Awake spikes), or carry-over from prior nights. Do not just restate hours — the bullets above already do that. Add what the user cannot see: e.g. "Deep below baseline 3 nights in a row" or "high Awake fraction suggests interrupted sleep, not just short sleep".`,

	BlockYesterday: `YESTERDAY — Interpret yesterday's load. Focus on: deviation from typical day-of-week patterns (use HRVWithDates/StepsWithDates), unexpected SpO2/Resp/WristTemp drift versus their 7-day baselines, or interaction between exercise minutes and recovery markers. Do not list raw step/calorie numbers — the bullets do that. Add: "First low-step day after a 4-day streak" or "Resp rate rose 3 breaths above baseline despite light activity — early illness signal".`,

	BlockRecovery: `RECOVERY — Interpret HRV[0]/RHR[0] versus 7-day baseline AS A PATTERN, not a single-day percentage. Focus on: multi-day trajectories (3+ consecutive HRV declines), divergences (HRV up while RHR also up = unusual), or causal links (e.g. "HRV recovered overnight after suppressed value yesterday"). VO2 only if 3+ values and a clear monthly trend. Do not just compute the percentage deviation — the bullets do that.`,

	BlockRecommendation: `RECOMMENDATION — One concrete action for today, stated as "[Pattern observed] -> [Action with numerical target]". Must align with EnergyBank.action_verdict if present. Examples: "HRV trending down 3 days, RHR elevated -> replace planned strength session with 30 min Z2 cycling, max HR 130" or "Sleep adequate, EnergyBank push_hard, no abnormal markers -> proceed with planned workout, monitor RPE". Do NOT restate metrics — translate the leaf-block findings (provided below) into a single decision the user can act on in the next hour.`,
}

// BuildBlockPrompt returns the full system prompt for one block by injecting
// the block-specific fragment into the shared template.
func BuildBlockPrompt(block string) string {
	frag, ok := blockInstructions[block]
	if !ok {
		frag = "Generate a short clinical health observation for the day."
	}
	return strings.Replace(systemPrompt, "{{BLOCK_INSTRUCTIONS}}", frag, 1)
}

// ─── input hashing ────────────────────────────────────────────────────────

// hashInputs marshals the per-block subset of metrics into stable JSON and
// returns a sha256 hex digest. Stable across runs because Go's encoding/json
// emits map keys in sorted order, and we package the subset as a struct.
func hashInputs(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SleepInputs is the metric subset that drives the SLEEP block. Late HRV
// updates leak in only via the recovery block — sleep depends on the night's
// stage breakdown, not on autonomic readouts.
type SleepInputs struct {
	LastDate string
	Sleep    []float64
	Deep     []float64
	REM      []float64
	Awake    []float64
}

// YesterdayInputs covers behavioural and respiratory markers of the prior day.
type YesterdayInputs struct {
	LastDate       string
	Steps          []float64
	Cal            []float64
	Exercise       []float64
	SpO2           []float64
	Resp           []float64
	WristTemp      []float64
	StepsWithDates []health.DatedValue
	HRVWithDates   []health.DatedValue
}

// RecoveryInputs covers autonomic recovery markers + EnergyBank verdict. HRV
// changes invalidate this block; SLEEP and YESTERDAY are unaffected.
type RecoveryInputs struct {
	LastDate    string
	HRV         []float64
	RHR         []float64
	VO2         []float64
	Sleep       []float64
	EnergyBank  *health.EnergyBank
	Readiness   *float64
}

// HashSleep / HashYesterday / HashRecovery extract and hash per-block subsets.
func HashSleep(r *health.RawMetrics) string {
	if r == nil {
		return ""
	}
	return hashInputs(SleepInputs{
		LastDate: r.LastDate,
		Sleep:    r.Sleep,
		Deep:     r.Deep,
		REM:      r.REM,
		Awake:    r.Awake,
	})
}

func HashYesterday(r *health.RawMetrics) string {
	if r == nil {
		return ""
	}
	return hashInputs(YesterdayInputs{
		LastDate:       r.LastDate,
		Steps:          r.Steps,
		Cal:            r.Cal,
		Exercise:       r.Exercise,
		SpO2:           r.SpO2,
		Resp:           r.Resp,
		WristTemp:      r.WristTemp,
		StepsWithDates: r.StepsWithDates,
		HRVWithDates:   r.HRVWithDates,
	})
}

func HashRecovery(r *health.RawMetrics, eb *health.EnergyBank, readiness *float64) string {
	if r == nil {
		return ""
	}
	return hashInputs(RecoveryInputs{
		LastDate:   r.LastDate,
		HRV:        r.HRV,
		RHR:        r.RHR,
		VO2:        r.VO2,
		Sleep:      r.Sleep,
		EnergyBank: eb,
		Readiness:  readiness,
	})
}

// HashRecommendation derives the recommendation hash from the three leaf
// texts (which themselves rotate when their underlying metrics change) plus
// the EnergyBank verdict — covers "leaves stable but verdict changed at noon"
// as well as the more common "leaf changed -> recommendation must change".
func HashRecommendation(sleepText, yesterdayText, recoveryText string, eb *health.EnergyBank) string {
	type rec struct {
		Sleep, Yesterday, Recovery string
		EnergyBank                 *health.EnergyBank
	}
	return hashInputs(rec{sleepText, yesterdayText, recoveryText, eb})
}

// ─── orchestrator ─────────────────────────────────────────────────────────

// BlockResult is the outcome of one Gemini call.
type BlockResult struct {
	Block string
	Text  string
	Hash  string
	Err   error
}

// GenerateLeafBlocks runs SLEEP, YESTERDAY, RECOVERY in parallel.
// Each goroutine builds its own prompt (block-specific instructions injected
// into the shared template) and calls Gemini with the same raw metrics JSON.
//
// hashes carry the inputs_hash for each block so callers can compare against
// the cache and skip Gemini when nothing has changed since last generation.
func GenerateLeafBlocks(apiKey, model string, maxTokens int, rawMetricsJSON []byte, lang string,
	skipBlock func(block string) bool) []BlockResult {

	results := make([]BlockResult, 0, len(LeafBlocks))
	resultsMu := sync.Mutex{}
	wg := sync.WaitGroup{}
	for _, b := range LeafBlocks {
		if skipBlock != nil && skipBlock(b) {
			continue
		}
		wg.Add(1)
		go func(block string) {
			defer wg.Done()
			prompt := BuildBlockPrompt(block)
			text, _, err := generateWithPrompt(apiKey, model, maxTokens, prompt, rawMetricsJSON, lang)
			resultsMu.Lock()
			results = append(results, BlockResult{Block: block, Text: text, Err: err})
			resultsMu.Unlock()
		}(b)
	}
	wg.Wait()
	return results
}

// GenerateRecommendation runs the root block, given the three leaf texts.
// The leaf texts are appended to the user-content payload (not re-marshalled
// as part of rawMetricsJSON) so the model sees the leaf prose alongside the
// raw metrics — useful when EnergyBank.action_verdict says "rest" but the
// leaves note one positive signal that the recommendation must override.
func GenerateRecommendation(apiKey, model string, maxTokens int, rawMetricsJSON []byte, lang string,
	sleepText, yesterdayText, recoveryText string) (string, error) {
	prompt := BuildBlockPrompt(BlockRecommendation)
	leafSummary := fmt.Sprintf(
		"\n\nLEAF BLOCKS (already generated for the user):\n\nSLEEP\n%s\n\nYESTERDAY\n%s\n\nRECOVERY\n%s",
		sleepText, yesterdayText, recoveryText)
	text, _, err := generateWithPrompt(apiKey, model, maxTokens, prompt, append(rawMetricsJSON, []byte(leafSummary)...), lang)
	return text, err
}
