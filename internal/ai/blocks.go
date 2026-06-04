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

// RecoveryInputs covers autonomic recovery markers. HRV changes invalidate
// this block; SLEEP and YESTERDAY are unaffected.
//
// Note: EnergyBank is deliberately NOT in the hash. Its intra-day fields
// (Current, DrainSoFar, Strain, Components.Note) rotate as steps accumulate
// during the morning, which would invalidate the cache on every smart-retry
// tick even when HRV/RHR/VO2/Sleep are unchanged — defeating the per-block
// design. The RECOVERY prompt itself focuses on autonomic trajectories and
// does not reference EnergyBank.
type RecoveryInputs struct {
	LastDate string
	HRV      []float64
	RHR      []float64
	VO2      []float64
	Sleep    []float64
	Context  InsightContext
}

type InsightContext struct {
	ReadinessScore      int    `json:"readiness_score,omitempty"`
	ReadinessRawScore   int    `json:"readiness_raw_score,omitempty"`
	ReadinessConfidence string `json:"readiness_confidence,omitempty"`
	ReadinessCapReason  string `json:"readiness_cap_reason,omitempty"`
	AIAdviceMode        string `json:"ai_advice_mode,omitempty"`
	CheckinStatus       string `json:"checkin_status,omitempty"`
	CheckinAnswer       string `json:"checkin_answer,omitempty"`
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

func HashRecovery(r *health.RawMetrics, eb *health.EnergyBank, ctx InsightContext) string {
	if r == nil {
		return ""
	}
	_ = eb // see RecoveryInputs doc — intra-day fields would defeat the cache
	return hashInputs(RecoveryInputs{
		LastDate: r.LastDate,
		HRV:      r.HRV,
		RHR:      r.RHR,
		VO2:      r.VO2,
		Sleep:    r.Sleep,
		Context:  ctx,
	})
}

// HashRecommendation derives the recommendation hash from the three leaf
// texts (which themselves rotate when their underlying metrics change) plus
// EnergyBank.ActionVerdict — covers "leaves stable but verdict flipped from
// push_hard to active_recovery at noon" as well as the more common
// "leaf changed -> recommendation must change". Other EnergyBank fields are
// intentionally excluded (they rotate intra-day, see RecoveryInputs).
//
// `verdictHistory` is the EOD verdict sequence over the last ~7 days
// (oldest→newest, e.g. ["rest","rest","moderate"]). Past verdicts are
// frozen once a day rolls over so they're safe to hash — unlike intra-day
// EnergyBank fields. Passing the sequence lets RECOMMENDATION pick up
// patterns like "3 rest days in a row" without re-hitting Gemini just
// because today's verdict is unchanged.
func HashRecommendation(sleepText, yesterdayText, recoveryText string, eb *health.EnergyBank, verdictHistory []string, ctx InsightContext) string {
	verdict := ""
	var flags []string
	if eb != nil {
		verdict = eb.ActionVerdict
		flags = eb.Flags
	}
	type rec struct {
		Sleep, Yesterday, Recovery string
		ActionVerdict              string
		VerdictHistory             []string
		// StressFlags invalidates the recommendation cache when the
		// §4.3 multi-channel signals flip — e.g. illness_signature
		// firing partway through the day must re-run RECOMMENDATION
		// to surface the rest guidance, even if leaf texts and the
		// verdict label are unchanged.
		StressFlags []string
		Context     InsightContext
	}
	return hashInputs(rec{sleepText, yesterdayText, recoveryText, verdict, verdictHistory, flags, ctx})
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
func GenerateLeafBlocks(apiKey, model string, maxTokens int, payloadForBlock func(block string) []byte, lang string,
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
			payload := []byte(nil)
			if payloadForBlock != nil {
				payload = payloadForBlock(block)
			}
			text, _, err := generateWithPrompt(apiKey, model, maxTokens, prompt, payload, lang)
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
//
// `verdictHistory` is the EOD verdict sequence over the last ~7 days
// (oldest→newest), surfaced to the model so it can recognise multi-day
// patterns like "3 rest days in a row → accumulated fatigue, push for
// proper rest" instead of treating each day in isolation. Empty slice
// is fine — the line is simply omitted from the prompt context.
func GenerateRecommendation(apiKey, model string, maxTokens int, rawMetricsJSON []byte, lang string,
	sleepText, yesterdayText, recoveryText string, verdictHistory []string, stressFlags []string, ctx InsightContext) (string, error) {
	prompt := BuildBlockPrompt(BlockRecommendation)
	context := BuildRecommendationContext(sleepText, yesterdayText, recoveryText, verdictHistory, stressFlags, ctx)
	text, _, err := generateWithPrompt(apiKey, model, maxTokens, prompt, append(rawMetricsJSON, []byte(context)...), lang)
	return text, err
}

func BuildRecommendationContext(sleepText, yesterdayText, recoveryText string, verdictHistory []string, stressFlags []string, ctx InsightContext) string {
	leafSummary := fmt.Sprintf(
		"\n\nLEAF BLOCKS (already generated for the user):\n\nSLEEP\n%s\n\nYESTERDAY\n%s\n\nRECOVERY\n%s",
		sleepText, yesterdayText, recoveryText)
	if len(verdictHistory) > 0 {
		leafSummary += "\n\nENERGYBANK_VERDICT_HISTORY (oldest→newest, last 7 EOD snapshots): " +
			strings.Join(verdictHistory, ", ")
	}
	if ctx.AIAdviceMode != "" {
		leafSummary += "\n\nREADINESS_EVIDENCE_CONTEXT: advice_mode=" + ctx.AIAdviceMode +
			fmt.Sprintf(", readiness=%d, raw_readiness=%d, confidence=%s, cap_reason=%s",
				ctx.ReadinessScore, ctx.ReadinessRawScore, ctx.ReadinessConfidence, ctx.ReadinessCapReason)
		if ctx.CheckinStatus != "" || ctx.CheckinAnswer != "" {
			leafSummary += fmt.Sprintf(", checkin_status=%s, checkin_answer=%s", ctx.CheckinStatus, ctx.CheckinAnswer)
		}
	}
	switch ctx.AIAdviceMode {
	case "withheld":
		leafSummary += "\nINSTRUCTION: Do not provide a training or recovery recommendation. Explain briefly that the system is waiting for today's recovery data."
	case "provisional_explanation_only", "needs_regeneration_after_sync":
		leafSummary += "\nINSTRUCTION: Treat today's readiness as provisional. Explain what is known, what is missing, and how the user's check-in aligns or conflicts with objective signals. Do NOT give confident push-hard advice."
	}
	// v2.2 §4.3 hard guard for the recommendation prose — the model
	// has a known failure mode where verdict=active_recovery but the
	// prose still says "you can push if you feel up to it". When
	// physiology contraindicates intensity, inject an explicit
	// instruction. Flags also surfaced for context so the model can
	// reference them ("body fighting infection — rest aligns").
	if health.AISuppressPushHard(stressFlags) {
		leafSummary += "\n\nSTRESS_FLAGS (active multi-channel signals): " +
			strings.Join(stressFlags, ", ") +
			"\nINSTRUCTION: Physiology indicates illness or recovery debt. " +
			"Recommend rest or light active recovery only. " +
			"Do NOT recommend high-intensity training, intervals, or push_hard sessions today."
	} else if len(stressFlags) > 0 {
		// Non-suppressive flags (parasympathetic_rebound, acute_stress
		// — interpretation flags) still surface to the model for
		// context but without the hard guardrail.
		leafSummary += "\n\nSTRESS_FLAGS (active multi-channel signals): " +
			strings.Join(stressFlags, ", ")
	}
	return leafSummary
}
