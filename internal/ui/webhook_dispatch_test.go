package ui

import "testing"

// shouldFirstClearDelete is the pure branch that decides whether a
// settings POST should synthesise a deleteWebhook diff against the
// env/default-effective old token. The four-condition policy is
// load-bearing: dropping any of them re-opens a class of
// reviewer-caught bugs.
func TestShouldFirstClearDelete(t *testing.T) {
	cases := []struct {
		name              string
		tokenPosted       bool
		oldTokenRowExists bool
		newRawToken       string
		oldEffectiveToken string
		want              bool
	}{
		{
			name:              "first clear from env-fallback — happy path",
			tokenPosted:       true,
			oldTokenRowExists: false,
			newRawToken:       "",
			oldEffectiveToken: "env_tok",
			want:              true,
		},
		{
			// Round-3 review bug: partial POST that omits telegram_token
			// (e.g. only timezone) must NOT trigger delete on an env-
			// backed tenant.
			name:              "partial POST without telegram_token — must not delete",
			tokenPosted:       false,
			oldTokenRowExists: false,
			newRawToken:       "",
			oldEffectiveToken: "env_tok",
			want:              false,
		},
		{
			// Subsequent clears: row already exists with "" from the
			// previous explicit clear. Plain raw diff handles this as
			// no-op; first-clear branch must not re-fire and spam
			// deleteWebhook.
			name:              "subsequent clear (row already empty)",
			tokenPosted:       true,
			oldTokenRowExists: true,
			newRawToken:       "",
			oldEffectiveToken: "env_tok",
			want:              false,
		},
		{
			name:              "operator setting a new token, not clearing",
			tokenPosted:       true,
			oldTokenRowExists: false,
			newRawToken:       "abc",
			oldEffectiveToken: "env_tok",
			want:              false,
		},
		{
			name:              "no env fallback exists — nothing to delete",
			tokenPosted:       true,
			oldTokenRowExists: false,
			newRawToken:       "",
			oldEffectiveToken: "",
			want:              false,
		},
		{
			// Partial POST on a tenant that already has a per-tenant
			// token (oldTokenRowExists=true). Even if every other
			// condition aligned, the raw diff path is the right one.
			name:              "partial POST on tenant with stored token",
			tokenPosted:       false,
			oldTokenRowExists: true,
			newRawToken:       "stored_tok",
			oldEffectiveToken: "stored_tok",
			want:              false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldFirstClearDelete(tc.tokenPosted, tc.oldTokenRowExists, tc.newRawToken, tc.oldEffectiveToken)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
