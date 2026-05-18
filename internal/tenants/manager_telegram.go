package tenants

import (
	"context"
	"log"

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
// multi-tenant mode it snapshots AllDBs() once and walks it; the
// snapshot is the source of truth for both the chat_id lookup and
// the returned *storage.DB so two tenants with the same chat_id
// can't race-rebind between the two reads.
//
// When two tenants share a chat_id (operator misconfiguration), the
// conflict is logged and the lookup returns (nil, "", false) rather
// than silently routing the callback to a non-deterministic tenant.
// Empty chat_id always returns (nil, "", false).
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
	// Snapshot once so the schema we resolve and the DB we return
	// come from the same map view.
	snapshot := m.AllDBs()
	var matched []string
	var matchedDB *storage.DB
	for schema, db := range snapshot {
		cfg := db.GetNotifyConfig(defaults)
		if cfg.ChatID == chat {
			matched = append(matched, schema)
			matchedDB = db
		}
	}
	switch len(matched) {
	case 0:
		return nil, "", false
	case 1:
		return matchedDB, matched[0], true
	default:
		// Duplicate chat_id across tenants — webhook routing would be
		// non-deterministic. Refuse to route rather than guess.
		log.Printf("tenants: duplicate Telegram chat_id %q across schemas %v; webhook will not route until resolved", chat, matched)
		return nil, "", false
	}
}
