package storage

import (
	"strconv"
)

// NotifyConfig holds Telegram credentials and per-weekday report schedule.
// It mirrors notify.Config but lives in storage to avoid import cycles.
type NotifyConfig struct {
	Token              string
	ChatID             string
	Lang               string
	Timezone           string
	MorningWeekdayHour int
	MorningWeekendHour int
	EveningWeekdayHour int
	EveningWeekendHour int
}

// Enabled returns true when Telegram credentials are present.
func (c NotifyConfig) Enabled() bool {
	return c.Token != "" && c.ChatID != ""
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

// GetNotifyConfig builds a NotifyConfig from the settings table,
// falling back to the supplied env-derived defaults for any unset key.
func (s *DB) GetNotifyConfig(defaults NotifyConfig) NotifyConfig {
	return NotifyConfig{
		Token:              s.GetSetting("telegram_token", defaults.Token),
		ChatID:             s.GetSetting("telegram_chat_id", defaults.ChatID),
		Lang:               s.GetSetting("report_lang", defaults.Lang),
		Timezone:           s.GetSetting("timezone", defaults.Timezone),
		MorningWeekdayHour: getSettingInt(s, "report_morning_weekday", defaults.MorningWeekdayHour),
		MorningWeekendHour: getSettingInt(s, "report_morning_weekend", defaults.MorningWeekendHour),
		EveningWeekdayHour: getSettingInt(s, "report_evening_weekday", defaults.EveningWeekdayHour),
		EveningWeekendHour: getSettingInt(s, "report_evening_weekend", defaults.EveningWeekendHour),
	}
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
