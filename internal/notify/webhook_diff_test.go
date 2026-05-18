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
			// Rotation: both Register AND Delete fire so the old bot's
			// webhook is cleaned up rather than left dangling. Old
			// design rationale ("we don't control old bot") was wrong:
			// the old token is still our API key, deleteWebhook on it
			// is straightforward.
			name:     "token changed (rotation) — register new + delete old",
			oldToken: "abc", oldChat: "111",
			newToken: "xyz", newChat: "111",
			wantRegister: true,
			wantDelete:   true,
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
			wantDelete:   true,
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
			// Register and Delete can BOTH fire on rotation (old token
			// non-empty, new token non-empty, both different). Both
			// false means "nothing to do" (chat_id-only change). The
			// service layer dispatches whichever combination is set.
		})
	}
}
