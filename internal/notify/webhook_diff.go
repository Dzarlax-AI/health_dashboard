package notify

import "health-receiver/internal/storage"

// TelegramDiff is the pure-function output of DetectTelegramDiff. The
// service layer uses it to pick exactly one webhook action (or none)
// after writing new tenant config.
//
// NeedsRegister and NeedsDelete are mutually exclusive:
//   - NeedsRegister=true: token was added or rotated; call
//     webhookRegistrar.Register(NewToken). When rotating, the previous
//     bot's webhook on Telegram side is implicitly invalidated because
//     the new token belongs to a different bot — no separate
//     deleteWebhook call needed (and would be wasted, since we don't
//     control the old bot's API surface anymore).
//   - NeedsDelete=true: token was removed; call
//     webhookRegistrar.Delete(OldToken) to clean up the registration.
//
// Both false: nothing changed that affects the webhook. This includes
// the chat_id-changed-but-token-same case — Telegram's webhook is
// bot-scoped, not chat-scoped.
type TelegramDiff struct {
	NeedsRegister bool
	NeedsDelete   bool
	OldToken      string
	NewToken      string
}

// DetectTelegramDiff is the pure decision over (old, new) NotifyConfig.
// No DB, no HTTP. Exhaustively covered by TestDetectTelegramDiff —
// adding a new branch should always come with a new test row.
func DetectTelegramDiff(old, new storage.NotifyConfig) TelegramDiff {
	switch {
	case old.Token == new.Token:
		// Token unchanged. Even if chat_id flipped, the webhook URL on
		// Telegram's side is keyed by bot token, so no re-registration
		// is necessary. TenantFinder routes by chat_id at request time.
		return TelegramDiff{}
	case new.Token == "":
		// Token was removed. Clean up the old bot's webhook.
		return TelegramDiff{NeedsDelete: true, OldToken: old.Token}
	default:
		// Token added (old=="") or rotated (old!=new && both non-empty).
		// In both cases the action is the same: register the new bot.
		return TelegramDiff{NeedsRegister: true, OldToken: old.Token, NewToken: new.Token}
	}
}
