package notify

import (
	"testing"

	"health-receiver/internal/storage"
)

// DetectTelegramDiff is the pure decision the settings service uses to
// pick a webhook action after writing new tenant config. Table-driven
// over every (old, new) shape so the policy is auditable in one read.
func TestDetectTelegramDiff(t *testing.T) {
	cases := []struct {
		name           string
		oldToken       string
		oldChat        string
		newToken       string
		newChat        string
		wantRegister   bool
		wantDelete     bool
		wantOldToken   string
		wantNewToken   string
	}{
		{
			name:     "no change — same token + chat",
			oldToken: "abc", oldChat: "111",
			newToken: "abc", newChat: "111",
		},
		{
			name:     "both empty",
			oldToken: "", oldChat: "",
			newToken: "", newChat: "",
		},
		{
			name:     "token added (empty → set)",
			oldToken: "", oldChat: "",
			newToken: "abc", newChat: "111",
			wantRegister: true,
			wantNewToken: "abc",
		},
		{
			name:     "token changed (rotation)",
			oldToken: "abc", oldChat: "111",
			newToken: "xyz", newChat: "111",
			wantRegister: true,
			wantOldToken: "abc",
			wantNewToken: "xyz",
		},
		{
			name:     "token removed (set → empty)",
			oldToken: "abc", oldChat: "111",
			newToken: "", newChat: "",
			wantDelete:   true,
			wantOldToken: "abc",
		},
		{
			name:     "chat_id changed, token same — no webhook action",
			oldToken: "abc", oldChat: "111",
			newToken: "abc", newChat: "222",
		},
		{
			name:     "chat_id changed AND token rotated",
			oldToken: "abc", oldChat: "111",
			newToken: "xyz", newChat: "222",
			wantRegister: true,
			wantOldToken: "abc",
			wantNewToken: "xyz",
		},
		{
			name:     "token added with empty chat — still register (webhook is bot-scoped)",
			oldToken: "", oldChat: "",
			newToken: "abc", newChat: "",
			wantRegister: true,
			wantNewToken: "abc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := DetectTelegramDiff(
				storage.NotifyConfig{Token: tc.oldToken, ChatID: tc.oldChat},
				storage.NotifyConfig{Token: tc.newToken, ChatID: tc.newChat},
			)
			if diff.NeedsRegister != tc.wantRegister {
				t.Errorf("NeedsRegister = %v, want %v", diff.NeedsRegister, tc.wantRegister)
			}
			if diff.NeedsDelete != tc.wantDelete {
				t.Errorf("NeedsDelete = %v, want %v", diff.NeedsDelete, tc.wantDelete)
			}
			if diff.OldToken != tc.wantOldToken {
				t.Errorf("OldToken = %q, want %q", diff.OldToken, tc.wantOldToken)
			}
			if diff.NewToken != tc.wantNewToken {
				t.Errorf("NewToken = %q, want %q", diff.NewToken, tc.wantNewToken)
			}
			// Register and Delete are mutually exclusive — a single transition
			// is either "we have a new token to register" or "we have an old
			// token to clean up", never both. Token rotation is "register
			// new"; the old bot's webhook on Telegram side is implicitly
			// invalidated because it's a different bot now.
			if diff.NeedsRegister && diff.NeedsDelete {
				t.Errorf("NeedsRegister and NeedsDelete must be mutually exclusive")
			}
		})
	}
}
