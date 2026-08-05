package storage

import (
	"strconv"
)

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
