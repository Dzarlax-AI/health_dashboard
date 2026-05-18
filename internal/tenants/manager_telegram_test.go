package tenants

import (
	"testing"
)

// Pure-map lookup is the only piece worth testing without a live DB.
// The DBForTelegramChatID wrapper just walks AllDBs() / legacyDB and
// invokes this — covered by the smoke test on prod.
func TestSchemaForChatID(t *testing.T) {
	chatIDs := map[string]string{
		"111": "health",
		"222": "health_mariia",
	}
	cases := []struct {
		name  string
		chat  string
		want  string
		found bool
	}{
		{"primary tenant", "111", "health", true},
		{"second tenant", "222", "health_mariia", true},
		{"unknown chat", "999", "", false},
		{"empty chat", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := schemaForChatID(chatIDs, tc.chat)
			if found != tc.found || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, found, tc.want, tc.found)
			}
		})
	}
}
