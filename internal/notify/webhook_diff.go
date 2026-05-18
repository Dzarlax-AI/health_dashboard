package notify

import "health-receiver/internal/storage"

// TelegramDiff is the pure-function output of DetectTelegramDiff. The
// service layer uses it to pick which webhook actions to perform
// after writing new tenant config.
//
// The flag combinations expressible:
//   - both false           → no webhook impact (e.g. chat_id-only change)
//   - NeedsRegister only   → token added (old was empty)
//   - NeedsDelete only     → token removed (new is empty)
//   - both true (rotation) → register new + delete old; OldToken and
//                            NewToken both non-empty
//
// The previous design treated rotation as register-only (skipping the
// old-bot cleanup with the argument that we don't control the old
// bot's API surface). That was wrong — the OLD token IS the API key,
// and we still hold it long enough to call deleteWebhook on it. Not
// cleaning up leaves the old bot configured to keep posting callbacks
// to our endpoint until someone else takes over the bot or it's
// re-registered manually. Best-effort delete fixes that.
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
	case old.Token == "":
		// Token added (old was empty). Register the new bot only.
		return TelegramDiff{NeedsRegister: true, NewToken: new.Token}
	default:
		// Rotation: old and new both non-empty and different. Register
		// the new bot AND delete the old bot's webhook — both actions
		// are independent and run best-effort in the dispatcher.
		return TelegramDiff{
			NeedsRegister: true,
			NeedsDelete:   true,
			OldToken:      old.Token,
			NewToken:      new.Token,
		}
	}
}
