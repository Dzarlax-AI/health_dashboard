package api

import (
	"encoding/json"
	"testing"
)

func TestAIBriefingResponseKeepsReleasedIOSCompatibilityFields(t *testing.T) {
	tests := []struct {
		name   string
		blocks map[string]string
		want   map[string]string
	}{
		{
			name: "complete",
			blocks: map[string]string{
				"SLEEP":          "sleep body",
				"YESTERDAY":      "yesterday body",
				"RECOVERY":       "recovery body",
				"RECOMMENDATION": "recommendation body",
			},
			want: map[string]string{
				"sleep":          "sleep body",
				"yesterday":      "yesterday body",
				"recovery":       "recovery body",
				"recommendation": "recommendation body",
			},
		},
		{
			name:   "empty",
			blocks: map[string]string{},
			want:   map[string]string{"sleep": "", "yesterday": "", "recovery": "", "recommendation": ""},
		},
		{
			name:   "partial",
			blocks: map[string]string{"SLEEP": "sleep body"},
			want:   map[string]string{"sleep": "sleep body", "yesterday": "", "recovery": "", "recommendation": ""},
		},
		{
			name:   "v2 synthesis",
			blocks: map[string]string{"SYNTHESIS": "one aligned explanation"},
			want: map[string]string{
				"sleep": "", "yesterday": "", "recovery": "",
				"recommendation": "one aligned explanation",
				"summary":        "one aligned explanation",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := NewAIBriefingResponse(
				"2026-08-02",
				"en",
				"combined",
				[]AIBriefingSection{},
				tc.blocks,
				false,
				false,
			)
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{
				"date",
				"lang",
				"insight",
				"sections",
				"blocks",
				"sleep",
				"yesterday",
				"recovery",
				"recommendation",
				"summary",
				"generating",
				"disabled",
			} {
				if _, ok := payload[key]; !ok {
					t.Errorf("AI briefing compatibility field %q is missing", key)
				}
			}
			for key, want := range tc.want {
				if got := payload[key]; got != want {
					t.Errorf("%s = %#v, want %q", key, got, want)
				}
			}
		})
	}
}
