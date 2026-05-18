package tenants

import (
	"context"

	"health-receiver/internal/storage"
)

// schemaForChatID is the pure-map lookup, isolated so it can be tested
// without spinning up real DB pools. Returns the matched schema and
// whether a match was found.
func schemaForChatID(chatIDs map[string]string, chat string) (string, bool) {
	if chat == "" {
		return "", false
	}
	schema, ok := chatIDs[chat]
	return schema, ok
}

// DBForTelegramChatID walks every registered tenant's notification
// config (resolved against env defaults) and returns the first whose
// chat_id matches. Used by the Telegram webhook to route an inbound
// callback to the right tenant's DB pool.
//
// In legacy single-user mode the lookup checks the legacyDB only. In
// multi-tenant mode it walks AllDBs(). Empty chat_id always returns
// (nil, "", false).
//
// Returns (nil, "", false) when no tenant matches; the caller should
// reject the update without touching any pool.
func (m *Manager) DBForTelegramChatID(_ context.Context, defaults storage.NotifyConfig, chat string) (*storage.DB, string, bool) {
	if chat == "" {
		return nil, "", false
	}
	// Legacy mode: only one DB to check.
	if m.LegacyMode() {
		db := m.LegacyDB()
		if db == nil {
			return nil, "", false
		}
		if db.GetNotifyConfig(defaults).ChatID == chat {
			return db, "health", true
		}
		return nil, "", false
	}
	chatIDs := make(map[string]string, len(m.tenants))
	for schema, db := range m.AllDBs() {
		cfg := db.GetNotifyConfig(defaults)
		if cfg.ChatID != "" {
			chatIDs[cfg.ChatID] = schema
		}
	}
	schema, ok := schemaForChatID(chatIDs, chat)
	if !ok {
		return nil, "", false
	}
	return m.AllDBs()[schema], schema, true
}
