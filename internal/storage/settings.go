package storage

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"health-receiver/internal/ai"
)

const (
	SettingTodayInsightsB0Enabled     = "today_insights_b0_enabled"
	SettingTodayInsightsB1Enabled     = "today_insights_b1_enabled"
	SettingTodayInsightsB1QualityGate = "today_insights_b1_quality_gate_v2"
	TodayInsightsB1QualityGateVersion = "today-insights-b1-quality-gate-v2"
)

// TodayInsightsB1QualityGateApproval is durable, tenant-scoped evidence that
// the exact reviewed B1 corpus passed the product gate. It intentionally keeps
// only release metadata: the anonymized corpus and human review notes remain
// in their external review artifact and are never copied into tenant settings.
type TodayInsightsB1QualityGateApproval struct {
	Version            string `json:"version"`
	CorpusHash         string `json:"corpus_hash"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Reasoning          string `json:"reasoning"`
	PromptRevision     string `json:"prompt_revision"`
	ClaimPacketVersion string `json:"claim_packet_version"`
	NarrativeVersion   string `json:"narrative_version"`
	ReviewFingerprint  string `json:"review_fingerprint"`
	ApprovedAt         string `json:"approved_at"`
}

func ValidateTodayInsightsB1QualityGateApproval(approval TodayInsightsB1QualityGateApproval) error {
	if approval.Version != TodayInsightsB1QualityGateVersion {
		return fmt.Errorf("unsupported B1 quality-gate version %q", approval.Version)
	}
	if len(approval.CorpusHash) != 64 {
		return fmt.Errorf("B1 quality-gate corpus hash must be SHA-256")
	}
	if _, err := hex.DecodeString(approval.CorpusHash); err != nil {
		return fmt.Errorf("decode B1 quality-gate corpus hash: %w", err)
	}
	if approval.CorpusHash != strings.ToLower(approval.CorpusHash) {
		return fmt.Errorf("B1 quality-gate corpus hash must be lowercase")
	}
	if strings.TrimSpace(approval.Provider) == "" || strings.TrimSpace(approval.Model) == "" {
		return fmt.Errorf("B1 quality-gate provider and model are required")
	}
	canonicalReasoning, err := TodayInsightsB1QualityGateReasoning(approval.Provider, approval.Reasoning)
	if err != nil {
		return err
	}
	if approval.Reasoning != canonicalReasoning {
		return fmt.Errorf("B1 quality-gate reasoning must match the provider capability")
	}
	if strings.TrimSpace(approval.PromptRevision) == "" || strings.TrimSpace(approval.ClaimPacketVersion) == "" || strings.TrimSpace(approval.NarrativeVersion) == "" {
		return fmt.Errorf("B1 quality-gate prompt and narrative contract versions are required")
	}
	if len(approval.ReviewFingerprint) != 64 {
		return fmt.Errorf("B1 quality-gate review fingerprint must be SHA-256")
	}
	if _, err := hex.DecodeString(approval.ReviewFingerprint); err != nil || approval.ReviewFingerprint != strings.ToLower(approval.ReviewFingerprint) {
		return fmt.Errorf("B1 quality-gate review fingerprint must be lowercase SHA-256")
	}
	if _, err := time.Parse(time.RFC3339, approval.ApprovedAt); err != nil {
		return fmt.Errorf("parse B1 quality-gate approval time: %w", err)
	}
	return nil
}

// TodayInsightsB1QualityGateReasoning produces the stable review identity for
// a provider's reasoning setting. Providers without reasoning support must
// persist an empty value: a UI's stale "none" setting does not affect their
// output and must not make a valid Gemini review impossible to reuse.
func TodayInsightsB1QualityGateReasoning(providerID, reasoning string) (string, error) {
	provider, err := ai.GetProvider(providerID)
	if err != nil {
		return "", fmt.Errorf("resolve B1 quality-gate provider: %w", err)
	}
	return canonicalTodayInsightsB1Reasoning(provider.Descriptor(), reasoning)
}

func canonicalTodayInsightsB1Reasoning(descriptor ai.ProviderDescriptor, reasoning string) (string, error) {
	if !descriptor.SupportsReasoning {
		return "", nil
	}
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		reasoning = descriptor.DefaultReasoning
	}
	if reasoning == "" {
		return "", fmt.Errorf("B1 quality-gate reasoning is required for provider %q", descriptor.ID)
	}
	return reasoning, nil
}

// ResolveTodayInsightsB1ProviderConfig is the single configuration resolver
// for B1's review identity, cache fingerprint, and provider request. Keeping
// these values identical prevents an approval for a provider default from
// accidentally being reused with a differently normalized runtime request.
func ResolveTodayInsightsB1ProviderConfig(cfg AIConfig) (ai.Provider, ai.ProviderConfig, error) {
	provider, err := ai.GetProvider(cfg.Provider)
	if err != nil {
		return nil, ai.ProviderConfig{}, fmt.Errorf("resolve B1 provider: %w", err)
	}
	descriptor := provider.Descriptor()
	active := cfg.ActiveSettings()
	model := strings.TrimSpace(active.Model)
	if model == "" {
		model = descriptor.DefaultModel
	}
	if model == "" {
		return nil, ai.ProviderConfig{}, fmt.Errorf("B1 model is required for provider %q", cfg.Provider)
	}
	reasoning, err := canonicalTodayInsightsB1Reasoning(descriptor, active.ReasoningEffort)
	if err != nil {
		return nil, ai.ProviderConfig{}, err
	}
	maxOutputTokens := cfg.MaxOutputTokens
	if maxOutputTokens <= 0 || maxOutputTokens > ai.DailyInsightMaxTokens {
		maxOutputTokens = ai.DailyInsightMaxTokens
	}
	return provider, ai.ProviderConfig{
		APIKey:          active.APIKey,
		Model:           model,
		ReasoningEffort: reasoning,
		MaxOutputTokens: maxOutputTokens,
	}, nil
}

// TodayInsightsB1QualityGateApproval returns only a validated approval. A
// malformed or stale-looking setting is fail-closed: the factual Today
// snapshot remains available but no provider call is allowed.
func TodayInsightsB1QualityGateApprovalFor(s *DB) (TodayInsightsB1QualityGateApproval, bool) {
	var approval TodayInsightsB1QualityGateApproval
	if err := json.Unmarshal([]byte(s.GetSetting(SettingTodayInsightsB1QualityGate, "")), &approval); err != nil {
		return TodayInsightsB1QualityGateApproval{}, false
	}
	if err := ValidateTodayInsightsB1QualityGateApproval(approval); err != nil {
		return TodayInsightsB1QualityGateApproval{}, false
	}
	return approval, true
}

func (s *DB) SaveTodayInsightsB1QualityGateApproval(approval TodayInsightsB1QualityGateApproval) error {
	if err := ValidateTodayInsightsB1QualityGateApproval(approval); err != nil {
		return err
	}
	encoded, err := json.Marshal(approval)
	if err != nil {
		return fmt.Errorf("encode B1 quality-gate approval: %w", err)
	}
	return s.SaveSettings(map[string]string{SettingTodayInsightsB1QualityGate: string(encoded)})
}

// TodayInsightsB1QualityGateMatchesConfig prevents a review of one model from
// being silently reused after an Admin changes the active provider, model or
// reasoning. That change can alter the prose without changing a health fact,
// so it requires its own frozen-corpus review.
func TodayInsightsB1QualityGateMatchesConfig(approval TodayInsightsB1QualityGateApproval, cfg AIConfig) bool {
	if err := ValidateTodayInsightsB1QualityGateApproval(approval); err != nil || !cfg.Enabled() {
		return false
	}
	_, resolved, err := ResolveTodayInsightsB1ProviderConfig(cfg)
	if err != nil {
		return false
	}
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	return approval.Provider == cfg.Provider && approval.Model == resolved.Model && approval.Reasoning == resolved.ReasoningEffort &&
		approval.PromptRevision == identity.PromptRevision &&
		approval.ClaimPacketVersion == identity.ClaimPacketVersion &&
		approval.NarrativeVersion == identity.NarrativeVersion &&
		approval.ReviewFingerprint == identity.Fingerprint
}

func TodayInsightsB1ApprovedForConfig(s *DB, cfg AIConfig) bool {
	approval, approved := TodayInsightsB1QualityGateApprovalFor(s)
	return approved && TodayInsightsB1QualityGateMatchesConfig(approval, cfg)
}

// TodayInsightsB0Enabled controls only the new canonical sleep claim/action.
// The factual answer ladder remains available regardless of this flag.
func TodayInsightsB0Enabled(s *DB) bool {
	return getSettingBool(s, SettingTodayInsightsB0Enabled, false)
}

// TodayInsightsB1Enabled is deliberately opt-in and additionally requires a
// durable approval of the frozen-corpus quality gate. A bare boolean cannot
// accidentally activate a provider after deployment.
func TodayInsightsB1Enabled(s *DB) bool {
	if !getSettingBool(s, SettingTodayInsightsB1Enabled, false) {
		return false
	}
	_, approved := TodayInsightsB1QualityGateApprovalFor(s)
	return approved
}

// NotifyConfig holds Telegram credentials and per-weekday report schedule.
// It mirrors notify.Config but lives in storage to avoid import cycles.
type NotifyConfig struct {
	Token                string
	ChatID               string
	Lang                 string
	Timezone             string
	MorningWeekdayHour   int
	MorningWeekendHour   int
	EveningWeekdayHour   int
	EveningWeekendHour   int
	TelegramRichMessages bool
	// MorningCapHour is the deadline (24h clock, in Timezone) for the smart-retry
	// loop. Past this hour the morning report fires regardless of whether sleep
	// data has settled, with a stale-data banner. Defaults to MorningHour+4 with
	// a floor of 11 if unset.
	MorningCapHour int
}

// Enabled returns true when Telegram credentials are present.
func (c NotifyConfig) Enabled() bool {
	return c.Token != "" && c.ChatID != ""
}

// GetSettingExists reports whether a row for `key` exists in the
// settings table — regardless of whether the stored value is empty.
// Use when you need to distinguish "never set, fallback active" from
// "explicitly cleared to empty". The plain GetSetting helper collapses
// both into the fallback return, which is wrong for transitions where
// the empty-vs-absent distinction is the operator's intent (e.g.
// clearing a Telegram token that was sourced from env).
func (s *DB) GetSettingExists(key string) bool {
	var exists bool
	ctx, cancel := queryCtx()
	defer cancel()
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settings WHERE key = $1)`, key).Scan(&exists); err != nil {
		return false
	}
	return exists
}

// GetSetting returns the value for key, or fallback if not found.
func (s *DB) GetSetting(key, fallback string) string {
	var val *string
	ctx, cancel := queryCtx()
	defer cancel()
	if err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&val); err != nil || val == nil || *val == "" {
		return fallback
	}
	return *val
}

// SaveSettings upserts a map of key→value pairs into the settings table.
func (s *DB) SaveSettings(kv map[string]string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for k, v := range kv {
		if _, err := tx.Exec(ctx, `
			INSERT INTO settings (key, value, updated_at)
			VALUES ($1, $2, NOW()::TEXT)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			k, v); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type AIProviderSettings struct {
	APIKey          string
	Model           string
	ReasoningEffort string
}

// AIConfig holds provider-neutral generation parameters plus each adapter's
// saved credentials/model selection.
type AIConfig struct {
	Provider        string
	Providers       map[string]AIProviderSettings
	MaxOutputTokens int
}

func (c AIConfig) SettingsFor(provider string) AIProviderSettings {
	if c.Providers == nil {
		return AIProviderSettings{}
	}
	return c.Providers[provider]
}

func (c *AIConfig) SetSettingsFor(provider string, settings AIProviderSettings) {
	if c.Providers == nil {
		c.Providers = make(map[string]AIProviderSettings)
	}
	c.Providers[provider] = settings
}

func (c AIConfig) ActiveSettings() AIProviderSettings {
	return c.SettingsFor(c.Provider)
}

// Enabled returns true when the active provider has an API key.
func (c AIConfig) Enabled() bool {
	return c.Provider != "" && c.ActiveSettings().APIKey != ""
}

func cloneAIConfig(in AIConfig) AIConfig {
	out := in
	out.Providers = make(map[string]AIProviderSettings, len(in.Providers))
	for provider, settings := range in.Providers {
		out.Providers[provider] = settings
	}
	return out
}

func (c AIConfig) Clone() AIConfig {
	return cloneAIConfig(c)
}

// GetAIConfig builds an AIConfig from the settings table,
// falling back to the supplied env-derived defaults for any unset key.
func (s *DB) GetAIConfig(defaults AIConfig) AIConfig {
	out := cloneAIConfig(defaults)
	out.Provider = s.GetSetting("ai_provider", defaults.Provider)
	if out.Provider == "" {
		out.Provider = "gemini"
	}
	if s.GetSettingExists("ai_max_output_tokens") {
		out.MaxOutputTokens = getSettingInt(s, "ai_max_output_tokens", defaults.MaxOutputTokens)
	} else {
		out.MaxOutputTokens = getSettingInt(s, "gemini_max_tokens", defaults.MaxOutputTokens)
	}
	for provider, settings := range out.Providers {
		settings.APIKey = s.GetSetting(provider+"_api_key", settings.APIKey)
		settings.Model = s.GetSetting(provider+"_model", settings.Model)
		settings.ReasoningEffort = s.GetSetting(provider+"_reasoning_effort", settings.ReasoningEffort)
		out.SetSettingsFor(provider, settings)
	}
	return out
}

// GetNotifyConfig builds a NotifyConfig from the settings table,
// falling back to the supplied env-derived defaults for any unset key.
func (s *DB) GetNotifyConfig(defaults NotifyConfig) NotifyConfig {
	return NotifyConfig{
		Token:                s.GetSetting("telegram_token", defaults.Token),
		ChatID:               s.GetSetting("telegram_chat_id", defaults.ChatID),
		Lang:                 s.GetSetting("report_lang", defaults.Lang),
		Timezone:             s.GetSetting("timezone", defaults.Timezone),
		MorningWeekdayHour:   getSettingInt(s, "report_morning_weekday", defaults.MorningWeekdayHour),
		MorningWeekendHour:   getSettingInt(s, "report_morning_weekend", defaults.MorningWeekendHour),
		EveningWeekdayHour:   getSettingInt(s, "report_evening_weekday", defaults.EveningWeekdayHour),
		EveningWeekendHour:   getSettingInt(s, "report_evening_weekend", defaults.EveningWeekendHour),
		TelegramRichMessages: getSettingBool(s, "telegram_rich_messages", defaults.TelegramRichMessages),
		MorningCapHour:       getSettingInt(s, "report_morning_cap", defaults.MorningCapHour),
	}
}

// GetSettingInt is the exported variant of getSettingInt for callers outside
// this package (notify/digest.go uses it for weekly-digest day-of-week).
func (s *DB) GetSettingInt(key string, fallback int) int {
	return getSettingInt(s, key, fallback)
}

func getSettingInt(s *DB, key string, fallback int) int {
	v := s.GetSetting(key, "")
	if v == "" {
		return fallback
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return fallback
}

// getSettingBool reads a boolean setting. Accepts the standard
// `strconv.ParseBool` set ("true"/"false", "1"/"0", "TRUE"/"FALSE",
// etc.) — same flexibility as the rest of the Go toolchain. Falls
// back to the supplied `fallback` on missing key or unparseable
// value, NOT to zero-bool — important because callers like
// StressDrainEnabled default to false but other future bool flags
// might default to true.
func getSettingBool(s *DB, key string, fallback bool) bool {
	v := s.GetSetting(key, "")
	if v == "" {
		return fallback
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return fallback
}
